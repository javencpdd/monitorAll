package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/media"
	"github.com/monitorall/monitorall/internal/model"
)

// TestValidateConnParamsMediaMTXPath 表驱动校验：视频数据源的 mediaMtxPath 一进
// validateConnParams 就被卡住，非法值根本落不到库里。
//
// 背景（真实事故）：把完整 RTMP 地址 rtmp://172.31.68.227:1936/live/m20pro-f 填进
// mediaMtxPath，MediaMTX 回 500 "invalid path name"，前端只显示「HLS 协商超时」。
func TestValidateConnParamsMediaMTXPath(t *testing.T) {
	const goodURL = "rtmp://172.31.68.227:1936/live/m20pro-f"
	cases := []struct {
		name    string
		params  map[string]any
		wantErr bool
		code    apperr.Code // wantErr 时断言
		wantMsg string      // wantErr 时，报错信息必须包含该子串
	}{
		{
			name:   "publish 模式 + 合法路径",
			params: map[string]any{"mode": config.VideoModePublish, "mediaMtxPath": "live/m20pro-f"},
		},
		{
			name:   "pull 模式 + 合法路径 + 流地址",
			params: map[string]any{"rtmpUrl": goodURL, "mediaMtxPath": "live/m20pro-f"},
		},
		{
			name:   "路径留空仍按现状放行（由流地址推导）",
			params: map[string]any{"rtmpUrl": goodURL},
		},
		{
			name:    "publish 模式把完整地址填进路径",
			params:  map[string]any{"mode": config.VideoModePublish, "mediaMtxPath": goodURL},
			wantErr: true,
			code:    apperr.InvalidParam,
			wantMsg: `"live/m20pro-f"`,
		},
		{
			name:    "pull 模式把完整地址填进路径",
			params:  map[string]any{"rtmpUrl": goodURL, "mediaMtxPath": "rtsp://admin:pwd@192.168.2.69/Streaming/Channels/101"},
			wantErr: true,
			code:    apperr.InvalidParam,
			wantMsg: "只需流名",
		},
		{
			name:    "路径含连续斜杠",
			params:  map[string]any{"rtmpUrl": goodURL, "mediaMtxPath": "live//cam"},
			wantErr: true,
			code:    apperr.InvalidParam,
		},
		{
			name:    "路径含查询串",
			params:  map[string]any{"rtmpUrl": goodURL, "mediaMtxPath": "live/cam?x=1"},
			wantErr: true,
			code:    apperr.InvalidParam,
		},
		{
			name:    "路径含空格",
			params:  map[string]any{"rtmpUrl": goodURL, "mediaMtxPath": "live/cam 1"},
			wantErr: true,
			code:    apperr.InvalidParam,
		},
		// 既有语义必须保持不变：publish 模式下 rtmpUrl 与 mediaMtxPath 都空才是错
		{
			name:    "publish 模式两者皆空 → 仍报缺少流地址",
			params:  map[string]any{"mode": config.VideoModePublish},
			wantErr: true,
			code:    apperr.InvalidURL,
			wantMsg: "缺少流地址",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConnParams(model.KindVideo, tc.params)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("不应被拒绝: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("应被拒绝")
			}
			if apperr.CodeOf(err) != tc.code {
				t.Fatalf("错误码应为 %d，实际 %d (%v)", tc.code, apperr.CodeOf(err), err)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("报错信息缺少 %q: %s", tc.wantMsg, err.Error())
			}
		})
	}
}

// ————————————————— MediaMTX 同路径冲突检测 —————————————————

// newRef 从连接参数构造路径引用；构造不出来（路径为空）直接判失败。
func newRef(t *testing.T, id, name string, params map[string]any, cfg *config.Config) VideoPathRef {
	t.Helper()
	r, ok := videoPathRef(id, name, params, cfg)
	if !ok {
		t.Fatalf("构造路径引用失败（mediaMtxPath 为空？）: %+v", params)
	}
	return r
}

// TestCheckMediaMTXPathConflict 表驱动校验同路径冲突判定。
//
// 规则：同路径 + 同上游 → 放行（同一路相机挂多个看板是合法的）；
// 同路径 + 不同上游 → 拦截（MediaMTX 一个路径只能一条流，overridePublisher 会让
// 后来者静默顶掉前者，实测两个 ffmpeg 推同一路径时前者直接 Broken pipe 退出）。
func TestCheckMediaMTXPathConflict(t *testing.T) {
	const (
		pathCam  = "live/cam1"
		pathCam2 = "live/cam2"
		urlA     = "rtmp://172.31.68.227:1936/live/cam1"
		urlB     = "rtsp://admin:pwd@192.168.2.69:554/Streaming/Channels/101"
	)
	// 本机 MediaMTX 的 RTMP 推流地址（端口对齐 cfg.MediaMTX.PortRTMP）
	localCfg := config.DefaultConfig()
	localCfg.MediaMTX.PortRTMP = 1936
	localCfg.Server.LANHost = "172.31.68.100"
	localPush := "rtmp://172.31.68.100:1936/live/cam1"

	pub := map[string]any{"mode": config.VideoModePublish, "mediaMtxPath": pathCam}
	pullA := map[string]any{"rtmpUrl": urlA, "mediaMtxPath": pathCam}
	pullB := map[string]any{"rtmpUrl": urlB, "mediaMtxPath": pathCam}
	local := map[string]any{"rtmpUrl": localPush, "mediaMtxPath": pathCam}

	cases := []struct {
		name string
		// existing 为库里已存在的数据源
		existing []VideoPathRef
		// incoming 为本次要建/要改的数据源
		incoming VideoPathRef
		// wantName 为空表示期望放行；否则期望拦截且冲突源叫这个名字
		wantName string
	}{
		{
			name:     "1_同路径_都publish_放行",
			existing: []VideoPathRef{newRef(t, "ds-1", "m20Pro-前广角", pub, nil)},
			incoming: newRef(t, "ds-2", "m20Pro-后广角", pub, nil),
		},
		{
			name:     "2_同路径_都pull且rtmpUrl相同_放行",
			existing: []VideoPathRef{newRef(t, "ds-1", "相机A-看板1", pullA, nil)},
			incoming: newRef(t, "ds-2", "相机A-看板2", pullA, nil),
		},
		{
			name:     "3_同路径_publish_vs_pull_拦截",
			existing: []VideoPathRef{newRef(t, "ds-1", "m20Pro-前广角", pullA, nil)},
			incoming: newRef(t, "ds-2", "现场推流车", pub, nil),
			wantName: "m20Pro-前广角",
		},
		{
			name:     "4_同路径_两个不同rtmpUrl_拦截",
			existing: []VideoPathRef{newRef(t, "ds-1", "相机A", pullA, nil)},
			incoming: newRef(t, "ds-2", "相机B", pullB, nil),
			wantName: "相机A",
		},
		{
			name:     "5_不同路径_放行",
			existing: []VideoPathRef{newRef(t, "ds-1", "相机A", map[string]any{"rtmpUrl": urlA, "mediaMtxPath": pathCam2}, nil)},
			incoming: newRef(t, "ds-2", "相机B", pullB, nil),
		},
		{
			name:     "6_更新自己_放行",
			existing: []VideoPathRef{newRef(t, "ds-1", "相机A", pullA, nil)},
			incoming: newRef(t, "ds-1", "相机A-改名", pullA, nil),
		},
		{
			name:     "7_路径带首尾斜杠_视为同一路径_拦截",
			existing: []VideoPathRef{newRef(t, "ds-1", "相机A", map[string]any{"rtmpUrl": urlA, "mediaMtxPath": "/live/cam1"}, nil)},
			incoming: newRef(t, "ds-2", "现场推流车", pub, nil),
			wantName: "相机A",
		},
		{
			name:     "8_本机RTMP推流与publish同上游_放行",
			existing: []VideoPathRef{newRef(t, "ds-1", "现场推流车", pub, localCfg)},
			incoming: newRef(t, "ds-2", "本机推流", local, localCfg),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkMediaMTXPathConflict(tc.existing, tc.incoming)
			if tc.wantName == "" {
				if got != nil {
					t.Fatalf("应放行，实际拦截在「%s」（上游 %s vs %s）", got.Name, got.Upstream, tc.incoming.Upstream)
				}
				return
			}
			if got == nil {
				t.Fatal("应拦截，实际放行")
			}
			if got.Name != tc.wantName {
				t.Fatalf("冲突源应为「%s」，实际「%s」", tc.wantName, got.Name)
			}
		})
	}
}

// TestCheckMediaMTXPathConflictSkip 校验跳过条件：路径为空 / 非 video。
func TestCheckMediaMTXPathConflictSkip(t *testing.T) {
	// mediaMtxPath 为空 → 构造不出引用，调用方直接跳过不查
	for _, params := range []map[string]any{
		{"mode": config.VideoModePublish},
		{"rtmpUrl": "rtmp://172.31.68.227:1936/live/cam1"},
		{},
	} {
		if _, ok := videoPathRef("ds-1", "x", params, nil); ok {
			t.Fatalf("mediaMtxPath 为空时应跳过: %+v", params)
		}
	}
	// 即使强行传入空路径，判定函数也必须放行（双保险）
	empty := VideoPathRef{ID: "ds-2", Name: "x", Path: "", Upstream: media.SourcePublisher}
	other := VideoPathRef{ID: "ds-1", Name: "y", Path: "live/cam1", Upstream: "rtmp://x/live/cam1"}
	if got := checkMediaMTXPathConflict([]VideoPathRef{other}, empty); got != nil {
		t.Fatalf("incoming 路径为空时必须放行: %+v", got)
	}
	// 非 video：handler 在 checkMediaMTXPathConflictIn 里按 kind 提前返回，
	// 这里用 ROS 参数确认 videoPathRef 不会把它误判成有路径（connParams 里没有 mediaMtxPath）
	if _, ok := videoPathRef("ds-3", "ros源", map[string]any{"bridgeUrl": "ws://1.2.3.4:9090"}, nil); ok {
		t.Fatal("非 video 参数不应产出路径引用")
	}
}

// TestPathConflictErrorMessage 校验报错信息可行动且不含明文口令。
func TestPathConflictErrorMessage(t *testing.T) {
	const rtspWithCred = "rtsp://admin:your-password@192.168.2.69:554/Streaming/Channels/101"
	conflict := &VideoPathRef{ID: "ds-1", Name: "m20Pro-前广角", Path: "live/cam1", Upstream: rtspWithCred}
	incoming := VideoPathRef{ID: "ds-2", Name: "现场推流车", Path: "live/cam1", Upstream: media.SourcePublisher}

	err := pathConflictError(incoming, conflict)
	if apperr.CodeOf(err) != apperr.InvalidParam {
		t.Fatalf("错误码应为 40001，实际 %d", apperr.CodeOf(err))
	}
	msg := err.Error()
	for _, want := range []string{
		`"live/cam1"`,    // 冲突路径
		"m20Pro-前广角",     // 点名占用方
		"publisher",      // 当前上游
		"live/cam1-b",    // 建议的备选路径
		"live/<设备>-<机位>", // 命名建议
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("报错信息缺少 %s: %s", want, msg)
		}
	}
	// 脱敏硬要求：拉流地址常带内联凭据，报错要回显给用户，绝不能带明文口令
	if strings.Contains(msg, "your-password") {
		t.Fatalf("报错信息泄露明文口令: %s", msg)
	}
	if !strings.Contains(msg, "redacted") {
		t.Fatalf("带凭据的上游应脱敏后再展示: %s", msg)
	}
}

// TestDataSourcePathConflictHTTP 走真实 HTTP + 临时库，验证 handler 层的串联：
// 创建时拦截、同上游放行、更新自己不冲突、非 video 不受影响。
func TestDataSourcePathConflictHTTP(t *testing.T) {
	r, _ := newTestAPI(t)
	const base = "/api/v1/datasources"

	newVideo := func(name string, params map[string]any) map[string]any {
		return map[string]any{"name": name, "kind": "video", "protocol": "rtmp", "connParams": params}
	}

	// 1) 建第一个 publish 源
	code, out := doJSON(t, r, "POST", base, newVideo("m20Pro-前广角",
		map[string]any{"mode": "publish", "mediaMtxPath": "live/cam1"}))
	if code != http.StatusCreated {
		t.Fatalf("第一个源应创建成功，实际 %d: %v", code, out)
	}
	dsID := dataOf(t, out)["id"].(string)

	// 2) 同路径 + 不同上游（pull）→ 400 / 40001，且点名第一个源
	code, out = doJSON(t, r, "POST", base, newVideo("现场推流车",
		map[string]any{"rtmpUrl": "rtmp://172.31.68.227:1936/live/cam1", "mediaMtxPath": "live/cam1"}))
	if code != http.StatusBadRequest {
		t.Fatalf("同路径不同上游应被拒绝，实际 %d: %v", code, out)
	}
	if int(out["code"].(float64)) != int(apperr.InvalidParam) {
		t.Fatalf("错误码应为 40001，实际 %v", out["code"])
	}
	if msg := out["message"].(string); !strings.Contains(msg, "m20Pro-前广角") {
		t.Fatalf("报错应点名占用方: %s", msg)
	}

	// 3) 同路径 + 同上游（都 publish）→ 放行
	code, out = doJSON(t, r, "POST", base, newVideo("m20Pro-后广角",
		map[string]any{"mode": "publish", "mediaMtxPath": "live/cam1"}))
	if code != http.StatusCreated {
		t.Fatalf("同路径同上游应放行，实际 %d: %v", code, out)
	}

	// 4) 更新自己（只改名字，连接参数原样带回）→ 不能把自己判成冲突
	code, out = doJSON(t, r, "PUT", base+"/"+dsID, map[string]any{
		"name":       "m20Pro-前广角(改名)",
		"connParams": map[string]any{"mode": "publish", "mediaMtxPath": "live/cam1"},
	})
	if code != http.StatusOK {
		t.Fatalf("更新自己不应冲突，实际 %d: %v", code, out)
	}

	// 5) 非 video 数据源不受该检测影响（同名路径字段也不查）
	for i := 0; i < 2; i++ {
		code, out = doJSON(t, r, "POST", base, map[string]any{
			"name": "ros源" + strconv.Itoa(i), "kind": "ros", "protocol": "ros1",
			"connParams": map[string]any{"bridgeUrl": "ws://172.31.68.227:9090"},
		})
		if code != http.StatusCreated {
			t.Fatalf("非 video 源不应受路径检测影响，实际 %d: %v", code, out)
		}
	}
}
