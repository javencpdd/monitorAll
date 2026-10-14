// Package logx 基于标准库 log/slog 提供结构化日志：支持 json/text 两种格式、
// 按大小滚动的文件输出，以及带 module 字段的日志构造器。
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/config"
)

// RedactURL 抹掉 URL 里的凭据，用于日志：`rtsp://admin:pw@host:554/x` → `rtsp://redacted@host:554/x`。
//
// 为什么必须有：数据源的 rtspUrl / rtmpUrl / httpUrl 常带内联凭据
// （摄像头尤其如此）。原样写进日志等于把口令落盘，而日志文件一旦被
// 提交/备份/外发，口令就跟着走。
// 本项目真实发生过：logs/monitorall.log 里出现 59 次明文摄像头口令，
// 且 logs/ 曾被 git 跟踪 → 口令进了仓库历史。
//
// 抹哪些、为什么不只抹 userinfo：凭据只可能藏在两处 ——
//  1. userinfo（`rtsp://admin:pw@host`）；
//  2. query / fragment（`http://host/api?token=...`），这是 token 最常待的地方。
//
// 两处都清掉。日志的用途是"这是哪个源"，host + path 足够定位；
// 为保留 query 的调试信息而承担泄露风险不值得。
//
// 保守原则：解析不了 URL 时返回占位符而不是原文 —— 猜错会泄露，占位只会丢信息。
func RedactURL(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<unparsable-url>"
	}
	if u.User != nil {
		// 用纯字母做占位符：`***` 会被 url.User 百分号编码成 %2A%2A%2A，日志里很难看
		u.User = url.User("redacted")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// levelVar 保存当前日志级别，便于运行期调整。
var (
	levelVar  = new(slog.LevelVar)
	base      *slog.Logger
	initOnce  sync.Once
	closeFile func()
	mu        sync.Mutex
)

// Init 初始化全局 slog；可重复调用（用于测试与运行期改级别）。
func Init(cfg config.LogConfig) {
	mu.Lock()
	defer mu.Unlock()

	levelVar.Set(parseLevel(cfg.Level))

	var w io.Writer = os.Stdout
	if cfg.File != "" {
		rw, err := newRotatingWriter(cfg.File, cfg.MaxSizeMb, cfg.MaxBackups)
		if err != nil {
			fmt.Fprintf(os.Stderr, "日志文件打开失败，回退 stdout: %v\n", err)
		} else {
			w = io.MultiWriter(os.Stdout, rw)
			closeFile = func() { _ = rw.Close() }
		}
	}

	opts := &slog.HandlerOptions{Level: levelVar}
	var h slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	base = slog.New(h)
	slog.SetDefault(base)
	initOnce.Do(func() {})
}

// Logger 返回根日志器（未初始化时返回一个可用的默认日志器）。
func Logger() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if base == nil {
		base = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar}))
		slog.SetDefault(base)
	}
	return base
}

// With 返回带附加字段的日志器，用法同 slog：logx.With("module", "ros")。
func With(args ...any) *slog.Logger {
	return Logger().With(args...)
}

// SetLevel 运行期调整日志级别。
func SetLevel(level string) {
	levelVar.Set(parseLevel(level))
}

// Close 关闭日志文件（若启用）。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if closeFile != nil {
		closeFile()
		closeFile = nil
	}
}

// parseLevel 把字符串级别映射为 slog.Level，未知值回落到 info。
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// ————————————————— 自研滚动文件 Writer（单文件按大小滚动 + 备份数上限） —————————————————

// rotatingWriter 按大小滚动的 io.WriteCloser，避免使用第三方依赖。
type rotatingWriter struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
}

// newRotatingWriter 创建滚动写文件器；maxSizeMb/maxBackups 非正值时使用默认值。
func newRotatingWriter(path string, maxSizeMb, maxBackups int) (*rotatingWriter, error) {
	if maxSizeMb <= 0 {
		maxSizeMb = config.DefaultMaxSizeMb
	}
	if maxBackups <= 0 {
		maxBackups = config.DefaultMaxBackups
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingWriter{
		path:       path,
		maxBytes:   int64(maxSizeMb) * 1024 * 1024,
		maxBackups: maxBackups,
		file:       f,
		size:       st.Size(),
	}, nil
}

// Write 实现 io.Writer，超过阈值时先滚动再写。
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭当前文件。
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// rotate 把当前文件改名备份并清理超出数量的旧备份。
func (w *rotatingWriter) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	ts := time.Now().Format("20060102-150405")
	backup := fmt.Sprintf("%s.%s.log", w.path, ts)
	if err := os.Rename(w.path, backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := w.trimBackups(); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}

// trimBackups 删除超出 maxBackups 的旧备份文件。
func (w *rotatingWriter) trimBackups() error {
	dir := filepath.Dir(w.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	base := filepath.Base(w.path)
	var backups []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == base {
			continue
		}
		if strings.HasPrefix(e.Name(), base+".") && strings.HasSuffix(e.Name(), ".log") {
			backups = append(backups, filepath.Join(dir, e.Name()))
		}
	}
	if len(backups) <= w.maxBackups {
		return nil
	}
	sort.Strings(backups)
	for _, p := range backups[:len(backups)-w.maxBackups] {
		_ = os.Remove(p)
	}
	return nil
}
