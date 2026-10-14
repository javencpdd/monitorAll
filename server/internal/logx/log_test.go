package logx

import "testing"

// TestRedactURL 锁死"日志里的 URL 不得泄露内联凭据"。
//
// 背景：摄像头数据源的 rtspUrl 形如 rtsp://admin:pw@host:554/x，
// 原样落日志等于把口令写盘。本项目历史上 logs/monitorall.log 里
// 出现过 59 次明文摄像头口令，且 logs/ 曾被 git 跟踪。
func TestRedactURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "rtsp 带用户名+口令",
			in:   "rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101",
			want: "rtsp://redacted@192.168.2.69:554/Streaming/Channels/101",
		},
		{
			name: "rtmp 无凭据、无 query 时原样返回",
			in:   "rtmp://172.31.68.227:1936/live/lite3",
			want: "rtmp://172.31.68.227:1936/live/lite3",
		},
		{
			// query 一并抹掉：token 最常待的地方，而日志只需要 host+path 就能定位源
			name: "http 带凭据与查询参数",
			in:   "http://user:s3cret@example.com/api/data?x=1",
			want: "http://redacted@example.com/api/data",
		},
		{
			name: "无凭据但有 query 时也抹 query",
			in:   "http://192.168.2.100:7999/sensor_data/base_data?token=abc",
			want: "http://192.168.2.100:7999/sensor_data/base_data",
		},
		{
			name: "只有用户名没有口令",
			in:   "rtsp://admin@192.168.2.69:554/x",
			want: "rtsp://redacted@192.168.2.69:554/x",
		},
		{name: "空串原样", in: "", want: ""},
		{
			// 保守原则：解析不了就给占位符，绝不回退原文（猜错会泄露）
			name: "无法解析的串给占位符",
			in:   "://not a url at all",
			want: "<unparsable-url>",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactURL(c.in)
			if got != c.want {
				t.Fatalf("RedactURL(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestRedactURLNeverLeaksPassword 是最重要的一条：不管输入多奇怪，
// 输出里都不允许出现原口令。比逐条比对更接近"安全属性"本身。
func TestRedactURLNeverLeaksPassword(t *testing.T) {
	const secret = "your-password"
	inputs := []string{
		"rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101",
		"rtsp://admin:your-password@host/x?a=your-password",
		"http://u:your-password@example.com/p?token=your-password",
		"rtmp://your-password@172.31.68.227:1936/live/lite3",
		"garbage://admin:your-password@host/x",
	}
	for _, in := range inputs {
		got := RedactURL(in)
		if got == in && in != "" {
			t.Fatalf("原样返回了含凭据的 URL：%q", in)
		}
		if contains(got, secret) {
			t.Fatalf("输出里仍含口令 %q：%q", secret, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
