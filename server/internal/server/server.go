// Package server 负责 HTTP + HTTPS 双监听、优雅关闭与启动横幅打印。
// 约束：HTTP 永不跳转 HTTPS（保证任何环境下都能出画面）；HTTPS 启动失败不影响 HTTP。
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/media"
)

// Server 同时管理 HTTP 与 HTTPS 两个 http.Server。
type Server struct {
	cfg     *config.Config
	handler http.Handler
	log     *slog.Logger

	httpSrv  *http.Server
	httpsSrv *http.Server

	mu      sync.Mutex
	errCh   chan error
	started bool
	wg      sync.WaitGroup
}

// New 构造 Server（尚未监听）。
func New(cfg *config.Config, handler http.Handler) *Server {
	return &Server{
		cfg:     cfg,
		handler: handler,
		log:     logx.With("module", "server"),
		errCh:   make(chan error, 2),
	}
}

// Start 启动两个监听；任一失败不影响另一个（HTTPS 失败仅记日志）。
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}

	readTimeout := time.Duration(s.cfg.Server.ReadTimeoutMs) * time.Millisecond
	writeTimeout := time.Duration(s.cfg.Server.WriteTimeoutMs) * time.Millisecond

	s.httpSrv = &http.Server{
		Addr:         s.cfg.Server.HTTPAddr,
		Handler:      s.handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}
	httpLn, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("监听 HTTP %s 失败: %w", s.httpSrv.Addr, err)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.log.Info("HTTP 监听已启动", "addr", s.httpSrv.Addr)
		if err := s.httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errCh <- err
		}
	}()
	s.started = true

	if !s.cfg.Server.TLSEnabled {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(
		certPath(s.cfg), keyPath(s.cfg))
	if err != nil {
		s.log.Warn("证书加载失败，HTTPS 未启用（HTTP 仍可用）", "err", err)
		return nil
	}
	s.httpsSrv = &http.Server{
		Addr:         s.cfg.Server.HTTPSAddr,
		Handler:      s.handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		TLSConfig:    &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	httpsLn, err := net.Listen("tcp", s.httpsSrv.Addr)
	if err != nil {
		s.log.Warn("监听 HTTPS 失败（HTTP 仍可用）", "addr", s.httpsSrv.Addr, "err", err)
		s.httpsSrv = nil
		return nil
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.log.Info("HTTPS 监听已启动", "addr", s.httpsSrv.Addr)
		if err := s.httpsSrv.ServeTLS(httpsLn, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errCh <- err
		}
	}()
	return nil
}

// Wait 阻塞等待任一监听异常退出（正常关闭时 ctx 取消由调用方处理）。
func (s *Server) Wait() error {
	select {
	case err := <-s.errCh:
		return err
	case <-time.After(time.Hour):
		return nil
	}
}

// Shutdown 优雅关闭两个监听。
func (s *Server) Shutdown(ctx context.Context) error {
	var firstErr error
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if s.httpsSrv != nil {
		if err := s.httpsSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.wg.Wait()
	return firstErr
}

// PrintBanner 打印启动横幅（LAN 地址、证书信任指引、MediaMTX、SQLite）。
func (s *Server) PrintBanner(cert *media.CertInfo) {
	hosts := CollectLANHosts()
	primary := s.cfg.Server.LANHost
	if primary == "" && len(hosts) > 0 {
		primary = hosts[0]
	}
	if primary == "" {
		primary = "127.0.0.1"
	}
	httpPort := portOf(s.cfg.Server.HTTPAddr)
	httpsPort := portOf(s.cfg.Server.HTTPSAddr)

	fmt.Println()
	fmt.Printf("  MonitorAll v%s  (schema v%d)\n", config.Version, config.CurrentSchemaVersion)
	fmt.Println("  ────────────────────────────────────────────────")
	fmt.Printf("    HTTP   →  http://%s:%d\n", primary, httpPort)
	if s.cfg.Server.TLSEnabled && cert != nil && cert.Enabled {
		fmt.Printf("    HTTPS  →  https://%s:%d   (自签证书)\n", primary, httpsPort)
		fmt.Printf("    证书信任指引 → http://%s:%d/trust\n", primary, httpPort)
	}
	for _, h := range hosts {
		if h == primary {
			continue
		}
		fmt.Printf("    其它地址 → http://%s:%d\n", h, httpPort)
	}
	fmt.Printf("    MediaMTX     → %s @ %s\n", s.cfg.MediaMTX.Mode, s.cfg.MediaMTX.APIBase)
	fmt.Printf("    SQLite       → %s\n", s.cfg.Store.DSN)
	fmt.Println("  ────────────────────────────────────────────────")
	fmt.Println()
}

// ————————————————— LAN 地址探测（OP-01） —————————————————

// 需要跳过的网卡名前缀（虚拟网卡 / 容器网卡）。
var skipInterfacePrefixes = []string{"docker", "br-", "veth", "virbr", "vmnet", "lo", "tun", "tailscale", "zt"}

// CollectLANHosts 遍历网卡收集全部私有 IPv4 地址（跳过 loopback / 虚拟网卡）。
func CollectLANHosts() []string {
	hosts := make([]string, 0, 4)
	faces, err := net.Interfaces()
	if err != nil {
		return hosts
	}
	for _, face := range faces {
		if face.Flags&net.FlagUp == 0 || face.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(face.Name)
		if shouldSkipInterface(name) {
			continue
		}
		addrs, err := face.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			hosts = append(hosts, ip.String())
		}
	}
	sort.Strings(hosts)
	return hosts
}

// shouldSkipInterface 判断网卡名是否应跳过。
func shouldSkipInterface(name string) bool {
	for _, prefix := range skipInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// portOf 从监听地址提取端口。
func portOf(addr string) int {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 || idx+1 >= len(addr) {
		return 0
	}
	n := 0
	for _, ch := range addr[idx+1:] {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// certPath 返回证书文件路径。
func certPath(cfg *config.Config) string {
	return joinPath(cfg.Server.CertDir, media.CertFileName)
}

// keyPath 返回私钥文件路径。
func keyPath(cfg *config.Config) string {
	return joinPath(cfg.Server.CertDir, media.KeyFileName)
}

// joinPath 拼接目录与文件名（处理目录尾部分隔符）。
func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}
