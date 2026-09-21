package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/media"
	"github.com/monitorall/monitorall/internal/store"
	"github.com/monitorall/monitorall/internal/web"
	"github.com/monitorall/monitorall/internal/ws"
)

// Deps 为全部 REST handler 的共享依赖（进程启动时装配一次）。
type Deps struct {
	// Cfg 为生效配置。
	Cfg *config.Config
	// Store 为持久化实现。
	Store *store.SQLite
	// Adm 为适配器管理器。
	Adm *adapter.Manager
	// Bus 为帧总线。
	Bus *bus.Bus
	// Hub 为 WS 连接池。
	Hub *ws.Hub
	// Cert 为证书信息（可能为 nil）。
	Cert *media.CertInfo
	// MTX 为 MediaMTX API 客户端。
	MTX media.Client
	// StartAt 为进程启动时刻（/healthz 计算 uptime）。
	StartAt time.Time
	// LANHosts 为探测到的局域网地址列表。
	LANHosts []string
	// Log 为 API 日志器。
	Log *slog.Logger
}

// NewRouter 构建 Gin 引擎并注册全部路由。
func NewRouter(cfg *config.Config, st *store.SQLite, adm *adapter.Manager,
	b *bus.Bus, hub *ws.Hub, cert *media.CertInfo, lanHosts []string) (*gin.Engine, *Deps, error) {
	if cfg == nil {
		return nil, nil, apperr.New(apperr.Internal, "配置未初始化")
	}
	deps := &Deps{
		Cfg:      cfg,
		Store:    st,
		Adm:      adm,
		Bus:      b,
		Hub:      hub,
		Cert:     cert,
		MTX:      media.NewClient(cfg.MediaMTX),
		StartAt:  time.Now(),
		LANHosts: lanHosts,
		Log:      logx.With("module", "api"),
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(RequestID())
	r.Use(Recover(deps.Log))
	r.Use(RequestLog(deps.Log))
	r.Use(CORS())

	// 健康检查（非 /api 前缀，供容器探针使用）
	r.GET("/healthz", deps.healthz)
	r.GET("/healthz/goroutines", deps.goroutines)
	r.GET("/readyz", deps.readyz)

	v1 := r.Group(config.APIPrefix)
	{
		// WebSocket 数据面（与 REST 同端口）
		v1.GET("/ws", func(c *gin.Context) {
			if deps.Hub == nil {
				c.JSON(http.StatusServiceUnavailable, Response{Code: 50301, Message: "ws hub 未就绪", Data: nil})
				return
			}
			deps.Hub.HandleWS(c.Writer, c.Request)
		})

		// 数据源与通道
		v1.GET("/datasources", deps.listDataSources)
		v1.POST("/datasources", deps.createDataSource)
		v1.POST("/datasources/test", deps.testDataSource)
		v1.GET("/datasources/:id", deps.getDataSource)
		v1.PUT("/datasources/:id", deps.updateDataSource)
		v1.DELETE("/datasources/:id", deps.deleteDataSource)
		v1.GET("/datasources/:id/channels", deps.listChannels)
		v1.POST("/datasources/:id/channels", deps.createChannel)

		// 通道（跨数据源）
		v1.GET("/channels/:id", deps.getChannel)
		v1.GET("/channels/:id/sample", deps.sampleChannel)
		v1.DELETE("/channels/:id", deps.deleteChannel)

		// 看板与卡片
		v1.GET("/dashboards", deps.listDashboards)
		v1.POST("/dashboards", deps.createDashboard)
		v1.GET("/dashboards/:id", deps.getDashboard)
		v1.PUT("/dashboards/:id", deps.updateDashboard)
		v1.DELETE("/dashboards/:id", deps.deleteDashboard)
		v1.PUT("/dashboards/:id/layout", deps.updateLayout)
		v1.GET("/dashboards/:id/cards", deps.listCards)
		v1.POST("/dashboards/:id/cards", deps.createCard)
		v1.PUT("/cards/:id", deps.updateCard)
		v1.DELETE("/cards/:id", deps.deleteCard)

		// MediaMTX 路径发现与健康
		v1.GET("/mediamtx/paths", deps.mediamtxPaths)

		// 系统接口
		v1.GET("/system/runtime", deps.runtime)
		v1.GET("/system/lan", deps.lan)
		v1.GET("/system/cert", deps.downloadCert)
	}

	// 静态资源（go:embed 前端产物）+ SPA fallback
	mountStatic(r)

	return r, deps, nil
}

// mountStatic 挂载前端静态资源；无 dist 内容时降级为「仅 API 模式」并记录日志。
func mountStatic(r *gin.Engine) {
	if !web.HasDist() {
		logx.With("module", "api").Warn("未检测到前端产物，已降级为仅 API 模式（前后端分离开发）")
		r.NoRoute(func(c *gin.Context) {
			c.JSON(http.StatusNotFound, Response{Code: 40004, Message: "no route", Data: nil})
		})
		return
	}
	fs := web.HTTPFS()
	r.NoRoute(func(c *gin.Context) {
		// 静态文件命中即返回；未命中则回退 index.html（SPA 路由）
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, Response{Code: 40004, Message: "no route", Data: nil})
			return
		}
		path := c.Request.URL.Path
		if path == "" || path == "/" {
			web.ServeIndex(c.Writer, c.Request)
			return
		}
		if f, err := fs.Open(path); err == nil {
			_ = f.Close()
			http.FileServer(fs).ServeHTTP(c.Writer, c.Request)
			return
		}
		web.ServeIndex(c.Writer, c.Request)
	})
}
