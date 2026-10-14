package server

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/config"
)

// TestWaitBlocksUntilShutdown 锁死 Wait 的契约：**只有 Shutdown 才能解除它的阻塞**。
//
// 这条用例是历史 bug 的回归守卫：Wait 曾用 `case <-time.After(time.Hour): return nil`
// 表达"永久阻塞"，于是
//  1. Shutdown 无法解除阻塞（本用例第二个 select 会超时失败）；
//  2. 服务跑满 1 小时必然自动退出 —— Wait 返回 nil，main 视作"服务自己停了"，
//     走完整套优雅关闭后进程正常退出，只留一条 INFO 日志，不崩不报错。
//     已实证三次、误差 0 秒：10-11 00:13→01:13、10-12 08:10:30→09:10:30、09:19:58→10:19:58。
func TestWaitBlocksUntilShutdown(t *testing.T) {
	cfg := config.DefaultConfig()
	// 用随机端口，避免与任何正在运行的实例抢 8080/8443。
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	cfg.Server.HTTPSAddr = "127.0.0.1:0"
	cfg.Server.TLSEnabled = false // 只起 HTTP，测试不需要证书

	srv := New(cfg, http.NotFoundHandler())
	if err := srv.Start(); err != nil {
		t.Fatalf("启动监听失败: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- srv.Wait() }()

	// ① Shutdown 之前，Wait 必须一直阻塞。
	select {
	case err := <-waited:
		t.Fatalf("Wait 在 Shutdown 之前就返回了（err=%v）—— 它是被定时器解除阻塞的，不是被 Shutdown", err)
	case <-time.After(200 * time.Millisecond):
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 失败: %v", err)
	}

	// ② Shutdown 之后，Wait 必须很快返回 nil。
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Shutdown 之后 Wait 应返回 nil，实际返回 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown 之后 Wait 仍未返回 —— 阻塞没被解除（历史上正是 time.After(time.Hour) 导致的）")
	}
}

// TestWaitSourceHasNoTimerSentinel 源码级守卫，与上面那条互补：
// 上一条只在"行为差异可观测"时才会红，这条直接把"Wait 里不许出现定时器"钉死。
//
// 为什么需要：这个 bug 的表现是"1 小时后静默退出"，改回去的人不会在 1 秒内看到任何异常，
// 很容易再次引入。源码级断言是唯一能在毫秒级发现它的手段。
func TestWaitSourceHasNoTimerSentinel(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("读取 server.go 失败: %v", err)
	}
	body := string(src)

	const sig = "func (s *Server) Wait()"
	i := strings.Index(body, sig)
	if i < 0 {
		t.Fatalf("在 server.go 里找不到 %q（方法被改名了？请同步更新本测试）", sig)
	}
	rest := body[i:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatal("无法定位 Wait 方法体的结尾")
	}
	waitBody := rest[:end]

	if strings.Contains(waitBody, "time.After") {
		t.Fatalf("Wait 里出现了 time.After —— 「永久阻塞」绝不能用定时器表达"+
			"（这会让服务跑满 1 小时后自动退出，且不报错）。当前实现：\n%s", waitBody)
	}
	if !strings.Contains(waitBody, "s.done") {
		t.Fatalf("Wait 没有等待 s.done —— 它必须由 Shutdown 关闭来解除阻塞。当前实现：\n%s", waitBody)
	}
}
