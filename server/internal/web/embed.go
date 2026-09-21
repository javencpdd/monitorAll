// Package web 承载 go:embed 的前端产物（server/internal/web/dist）。
// 无 dist 内容时后端自动降级为「仅 API 模式」，便于前后端分离开发。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// distRoot 为前端产物在 embed 中的根目录。
const distRoot = "dist"

// indexFile 为 SPA 入口文件名。
const indexFile = "index.html"

// Dist 返回 embed 的文件系统（供上层直接使用）。
func Dist() embed.FS { return distFS }

// HasDist 判断已嵌入的前端产物是否包含 index.html（无则视为仅 API 模式）。
func HasDist() bool {
	f, err := distFS.Open(distRoot + "/" + indexFile)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// HTTPFS 返回可供 http.FileServer 使用的子文件系统。
func HTTPFS() http.FileSystem {
	sub, err := fs.Sub(distFS, distRoot)
	if err != nil {
		return http.FS(emptyFS{})
	}
	return http.FS(sub)
}

// ServeIndex 直接返回 index.html（SPA fallback；不可缓存，避免前端更新后仍取旧文件）。
func ServeIndex(w http.ResponseWriter, r *http.Request) {
	data, err := distFS.ReadFile(distRoot + "/" + indexFile)
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ContentTypeOf 依据扩展名给出 Content-Type（静态服务兜底用）。
func ContentTypeOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// emptyFS 为回退用的空文件系统（embed 子目录缺失时不 panic）。
type emptyFS struct{}

// Open 始终返回不存在，交由上层走 SPA fallback。
func (emptyFS) Open(name string) (fs.File, error) { return nil, fs.ErrNotExist }
