// Package main 是 MonitorAll 后端的进程入口：解析命令行、加载配置、
// 装配各层组件（store / bus / adapter manager / ws hub / api）并启动 HTTP+HTTPS 双监听。
//
// 装配顺序（依赖决定，不可随意调整）：
//
//	config → logx → store → 默认看板 → 证书 → bus → adapter.Setup → manager → hub → router → server
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	// 注册三类适配器工厂（副作用导入，勿删除）
	_ "github.com/monitorall/monitorall/internal/adapter/http"
	_ "github.com/monitorall/monitorall/internal/adapter/replay"
	_ "github.com/monitorall/monitorall/internal/adapter/ros"
	_ "github.com/monitorall/monitorall/internal/adapter/video"
	"github.com/monitorall/monitorall/internal/api"
	"github.com/monitorall/monitorall/internal/bus"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/media"
	appsrv "github.com/monitorall/monitorall/internal/server"
	"github.com/monitorall/monitorall/internal/store"
	"github.com/monitorall/monitorall/internal/ws"
)

func main() {
	// ——— 命令行 ———
	var (
		configPath   = flag.String("config", "", "配置文件路径（默认依次尝试 ./config.yaml、./data/config.yaml）")
		logLevel     = flag.String("log-level", "", "覆盖日志级别：debug|info|warn|error")
		printVersion = flag.Bool("version", false, "打印版本信息后退出")
	)
	flag.Parse()

	if *printVersion {
		fmt.Printf("monitorall %s (schema v%d)\n", config.Version, config.CurrentSchemaVersion)
		return
	}

	// ——— 配置 ———
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}
	if err := config.EnsureDirs(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "初始化目录失败: %v\n", err)
		os.Exit(1)
	}

	// ——— 日志 ———
	logx.Init(cfg.Log)
	log := logx.With("module", "main")
	log.Info("配置加载完成",
		"httpAddr", cfg.Server.HTTPAddr,
		"httpsAddr", cfg.Server.HTTPSAddr,
		"tlsEnabled", cfg.Server.TLSEnabled,
		"dataDir", cfg.Server.DataDir,
	)

	// ——— 持久化 ———
	st, err := store.Open(cfg.Store, cfg.Server.SecretKeyFile)
	if err != nil {
		log.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}

	// 首次启动自动创建「默认看板」（D8）
	if err := store.EnsureDefaultDashboard(st); err != nil {
		log.Error("创建默认看板失败", "err", err)
	}

	// ——— 证书（LAN IP SAN） ———
	lanHosts := appsrv.CollectLANHosts()
	// 回填局域网 IP：publicHost / lanHost 都留空时，播放地址（HLS / WHEP）会一路回退到
	// 127.0.0.1（见 media.BuildPlayURLs），结果只有运行 MediaMTX 的本机能出画面，
	// 局域网其它主机拿到的播放地址也是 127.0.0.1（表现为 m3u8 500 / whep 400）。
	// 若 hosts[0] 不是其它主机可达的地址，请在配置里显式设置 mediamtx.publicHost。
	if cfg.Server.LANHost == "" && len(lanHosts) > 0 {
		cfg.Server.LANHost = lanHosts[0]
	}
	if cfg.MediaMTX.PublicHost == "" {
		cfg.MediaMTX.PublicHost = cfg.Server.LANHost
	}
	certInfo, err := media.EnsureCertificate(cfg, lanHosts)
	if err != nil {
		log.Warn("证书准备失败，HTTPS 将不可用", "err", err)
	}

	// ——— 总线 ———
	frameBus := bus.New(&cfg.Bus)
	frameBus.Start(context.Background())
	defer frameBus.Stop()

	// ——— 适配器管理器 ———
	adapter.Setup(&adapter.Env{Cfg: cfg, Bus: frameBus, Store: st})
	adm := adapter.NewManager(cfg, frameBus, st)

	// ——— WS Hub（数据面出口） ———
	hub := ws.NewHub(frameBus, adm, st, cfg)
	frameBus.SetSink(hub)

	// ——— REST + WS 路由 ———
	router, _, err := api.NewRouter(cfg, st, adm, frameBus, hub, certInfo, lanHosts)
	if err != nil {
		log.Error("构建路由失败", "err", err)
		os.Exit(1)
	}

	srv := appsrv.New(cfg, router)

	// ——— 优雅退出 ———
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(); err != nil {
		log.Error("启动监听失败", "err", err)
		os.Exit(1)
	}
	srv.PrintBanner(certInfo)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Wait()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("服务异常退出", "err", err)
		}
	case <-ctx.Done():
		log.Info("收到退出信号，开始优雅关闭")
	}

	shutdownTimeout := time.Duration(cfg.Server.ShutdownTimeoutMs) * time.Millisecond
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("优雅关闭失败", "err", err)
	}
	hub.Close()
	adm.CloseAll()
	frameBus.Stop()
	if err := st.Close(); err != nil {
		log.Error("关闭数据库失败", "err", err)
	}
	logx.Close()
	log.Info("已退出")
}
