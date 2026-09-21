package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/model"
)

// newRequestID 生成一个请求 ID（复用 model 的临时 ID 前缀规则）。
func newRequestID() string { return model.NewTempID() }

// 上下文键常量（禁止散落裸字符串）。
const (
	// ctxKeyRequestID 为请求 ID 的上下文键。
	ctxKeyRequestID = "requestId"
	// headerRequestID 为响应头名。
	headerRequestID = "X-Request-Id"
)

// RequestID 中间件：生成/透传请求 ID 并写入响应头与日志上下文。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = newRequestID()
		}
		c.Set(ctxKeyRequestID, id)
		c.Header(headerRequestID, id)
		c.Next()
	}
}

// RequestLog 中间件：记录访问日志（耗时、状态码、路径）。
func RequestLog(log *slog.Logger) gin.HandlerFunc {
	if log == nil {
		log = logx.With("module", "api")
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		log.Debug("HTTP 请求",
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"latencyMs", latency.Milliseconds(),
			"requestId", traceIDOf(c),
			"clientIP", c.ClientIP(),
		)
	}
}

// Recover 中间件：panic 不导致进程退出（故障隔离第 ② 层）。
func Recover(log *slog.Logger) gin.HandlerFunc {
	if log == nil {
		log = logx.With("module", "api")
	}
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("请求处理 panic",
					"panic", r,
					"path", c.FullPath(),
					"requestId", traceIDOf(c),
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, Response{
					Code:    50900,
					Message: "internal error",
					Data:    nil,
					TraceID: traceIDOf(c),
				})
			}
		}()
		c.Next()
	}
}

// CORS 中间件：局域网多地址访问场景下放宽跨域（仅允许常用方法与头）。
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization,X-Request-Id")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// NoCache 中间件：禁用静态资源缓存（前端迭代期便于刷新）。
func NoCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
