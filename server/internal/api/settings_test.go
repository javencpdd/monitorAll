package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/store"
	"github.com/monitorall/monitorall/internal/weather"
)

// ————————————————— 脚手架 —————————————————

// newSettingsAPI 起一个带临时数据库【与临时 dataDir】的引擎。
//
// dataDir 必须换成临时目录：Logo 上传会往 <dataDir>/branding 落盘，
// 用 config.DefaultConfig() 的 "./data" 会让单元测试污染（甚至覆盖）真实运行数据。
func newSettingsAPI(t *testing.T) (*gin.Engine, *store.SQLite, *Deps) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	st, err := store.Open(config.StoreConfig{DSN: filepath.Join(dir, "t.db"), WAL: true},
		filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.DefaultConfig()
	cfg.Server.DataDir = dir
	cfg.Server.CertDir = filepath.Join(dir, "cert")
	cfg.Store.DSN = filepath.Join(dir, "t.db")
	cfg.Server.SecretKeyFile = filepath.Join(dir, "secret.key")

	r, deps, err := NewRouter(cfg, st, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("构建路由失败: %v", err)
	}
	return r, st, deps
}

// dataOf 取出统一响应体的 data 字段。
func dataOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	d, ok := out["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 data: %+v", out)
	}
	return d
}

// getSettingsPayload 读一次 /api/v1/settings 并解析成强类型。
func getSettingsPayload(t *testing.T, r *gin.Engine) SettingsPayload {
	t.Helper()
	code, out := doJSON(t, r, http.MethodGet, "/api/v1/settings", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /settings 失败: %d %+v", code, out)
	}
	raw, err := json.Marshal(dataOf(t, out))
	if err != nil {
		t.Fatalf("重新序列化 data 失败: %v", err)
	}
	var p SettingsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("data 不符合 SettingsPayload 契约: %v (%s)", err, raw)
	}
	return p
}

// putSettings 提交一次 PUT 并返回 (HTTP 码, 响应体)。
func putSettings(t *testing.T, r *gin.Engine, body map[string]any) (int, map[string]any) {
	t.Helper()
	return doJSON(t, r, http.MethodPut, "/api/v1/settings", body)
}

// weatherBody 构造一段合法天气设置（开启 + 深圳南山坐标）。
func weatherBody() map[string]any {
	return map[string]any{
		"enabled":    true,
		"provider":   "open-meteo",
		"lat":        22.53,
		"lon":        113.93,
		"label":      "深圳南山",
		"refreshMin": 15,
	}
}

// validBody 构造一段完整合法的设置请求体。
func validBody() map[string]any {
	return map[string]any{
		"branding": map[string]any{"title": "机器人监控", "subtitle": "四足机器人集群", "logoUrl": ""},
		"weather":  weatherBody(),
	}
}

// ————————————————— 读取与优先级 —————————————————

// TestSettingsDefaultsFromConfig DB 为空时，返回 config（yaml/默认）的值。
func TestSettingsDefaultsFromConfig(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	p := getSettingsPayload(t, r)
	if p.Branding.Title != config.DefaultBrandingTitle {
		t.Fatalf("默认标题错误: %q", p.Branding.Title)
	}
	if p.Weather.Enabled {
		t.Fatal("天气默认应关闭（开启会向第三方发请求）")
	}
	if p.Weather.Provider != config.WeatherProviderOpenMeteo {
		t.Fatalf("默认服务商错误: %q", p.Weather.Provider)
	}
	if p.Weather.RefreshMin != config.DefaultWeatherRefreshMin {
		t.Fatalf("默认缓存分钟数错误: %d", p.Weather.RefreshMin)
	}
}

// TestSettingsDBOverridesYAML 优先级必须是 DB > yaml > 默认。
//
// 关键是"只覆盖写过的字段"：DB 里只存 title，subtitle 必须仍来自 yaml。
// 若实现成"改一项就整段落库"，运维改 yaml 会永远失效且没有任何报错。
func TestSettingsDBOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(config.StoreConfig{DSN: filepath.Join(dir, "t.db"), WAL: true},
		filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer st.Close()

	cfg := config.DefaultConfig()
	cfg.Server.DataDir = dir
	cfg.Branding.Title = "yaml标题"
	cfg.Branding.Subtitle = "yaml副标题"
	cfg.Weather.Enabled = true
	cfg.Weather.Lat, cfg.Weather.Lon = 22.53, 113.93
	gin.SetMode(gin.TestMode)
	r, _, err := NewRouter(cfg, st, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("构建路由失败: %v", err)
	}

	// 先确认 yaml 生效
	p := getSettingsPayload(t, r)
	if p.Branding.Title != "yaml标题" || p.Weather.Enabled != true {
		t.Fatalf("yaml 未生效: %+v", p)
	}

	// 只在 DB 里写 title —— subtitle 必须继续用 yaml 的
	if err := st.SetSetting(testCtx(), settingKeyBranding, `{"title":"DB标题"}`); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	p = getSettingsPayload(t, r)
	if p.Branding.Title != "DB标题" {
		t.Fatalf("DB 未覆盖 yaml 的 title: %q", p.Branding.Title)
	}
	if p.Branding.Subtitle != "yaml副标题" {
		t.Fatalf("DB 没写的字段应回落 yaml，实际 %q", p.Branding.Subtitle)
	}

	// DB 里显式关闭天气（enabled=false）必须能盖住 yaml 的 true ——
	// 这是"用户在 UI 上关不掉天气"这个 bug 的回归测试。
	if err := st.SetSetting(testCtx(), settingKeyWeather, `{"enabled":false}`); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	p = getSettingsPayload(t, r)
	if p.Weather.Enabled {
		t.Fatal("DB 里的 enabled=false 未覆盖 yaml 的 true（用户在 UI 上将永远关不掉天气）")
	}
	if p.Weather.Lat != 22.53 {
		t.Fatalf("DB 没写的坐标应回落 yaml，实际 %v", p.Weather.Lat)
	}
}

// TestSettingsPutRoundTrip PUT 后 GET 必须拿到写入的值（且落库）。
func TestSettingsPutRoundTrip(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	code, out := putSettings(t, r, validBody())
	if code != http.StatusOK {
		t.Fatalf("PUT /settings 应成功，实际 %d %+v", code, out)
	}
	p := getSettingsPayload(t, r)
	if p.Branding.Title != "机器人监控" || p.Branding.Subtitle != "四足机器人集群" {
		t.Fatalf("品牌未落库: %+v", p.Branding)
	}
	if !p.Weather.Enabled || p.Weather.Lat != 22.53 || p.Weather.Lon != 113.93 {
		t.Fatalf("天气未落库: %+v", p.Weather)
	}
	if p.Weather.Label != "深圳南山" || p.Weather.RefreshMin != 15 {
		t.Fatalf("天气细节未落库: %+v", p.Weather)
	}
}

// ————————————————— PUT 校验 —————————————————

// TestSettingsPutRejectsInvalid 四类非法输入必须报 400，且 detail 指明是哪个字段。
func TestSettingsPutRejectsInvalid(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(b map[string]any)
		wantCode int
		wantMsg  string
	}{
		{
			name: "标题超长",
			mutate: func(b map[string]any) {
				b["branding"] = map[string]any{"title": strings.Repeat("名", 41), "subtitle": "", "logoUrl": ""}
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "branding.title",
		},
		{
			name: "副标题超长",
			mutate: func(b map[string]any) {
				b["branding"] = map[string]any{"title": "x", "subtitle": strings.Repeat("副", 61), "logoUrl": ""}
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "branding.subtitle",
		},
		{
			name: "Logo 用 javascript 协议",
			mutate: func(b map[string]any) {
				b["branding"] = map[string]any{"title": "x", "subtitle": "", "logoUrl": "javascript:alert(1)"}
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "branding.logoUrl",
		},
		{
			name: "Logo 用 data 协议",
			mutate: func(b map[string]any) {
				b["branding"] = map[string]any{"title": "x", "subtitle": "",
					"logoUrl": "data:image/svg+xml;base64,PHN2Zz48c2NyaXB0Pjwvc2NyaXB0Pjwvc3ZnPg=="}
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "branding.logoUrl",
		},
		{
			name: "服务商不在白名单",
			mutate: func(b map[string]any) {
				w := weatherBody()
				w["provider"] = "amap"
				b["weather"] = w
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "weather.provider",
		},
		{
			name: "开启天气但没坐标",
			mutate: func(b map[string]any) {
				w := weatherBody()
				w["lat"], w["lon"] = 0, 0
				b["weather"] = w
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "开启天气前请先填写坐标",
		},
		{
			name: "纬度越界",
			mutate: func(b map[string]any) {
				w := weatherBody()
				w["lat"] = 91
				b["weather"] = w
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "weather.lat",
		},
		{
			name: "经度越界",
			mutate: func(b map[string]any) {
				w := weatherBody()
				w["lon"] = -181
				b["weather"] = w
			},
			wantCode: http.StatusBadRequest,
			wantMsg:  "weather.lon",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _, _ := newSettingsAPI(t)
			body := validBody()
			c.mutate(body)
			code, out := putSettings(t, r, body)
			if code != c.wantCode {
				t.Fatalf("期望 %d，实际 %d，响应 %+v", c.wantCode, code, out)
			}
			blob, _ := json.Marshal(out)
			if !strings.Contains(string(blob), c.wantMsg) {
				t.Fatalf("响应未指明出错字段（期望包含 %q）: %s", c.wantMsg, blob)
			}
			// 校验失败绝不能落库：否则用户看到的是错误提示，DB 里却是被截断的值
			p := getSettingsPayload(t, r)
			if p.Branding.Title != config.DefaultBrandingTitle {
				t.Fatalf("校验失败却落库了: %+v", p.Branding)
			}
		})
	}
}

// TestSettingsPutNormalizes 能修的就地修：空白标题回落默认、缓存分钟数夹取、去空白。
func TestSettingsPutNormalizes(t *testing.T) {
	t.Run("空白标题回落默认", func(t *testing.T) {
		r, _, _ := newSettingsAPI(t)
		body := validBody()
		body["branding"] = map[string]any{"title": "   ", "subtitle": "  x  ", "logoUrl": ""}
		code, out := putSettings(t, r, body)
		if code != http.StatusOK {
			t.Fatalf("空白标题不算错误，实际 %d %+v", code, out)
		}
		p := getSettingsPayload(t, r)
		if p.Branding.Title != config.DefaultBrandingTitle {
			t.Fatalf("空白标题应回落 %q，实际 %q", config.DefaultBrandingTitle, p.Branding.Title)
		}
		if p.Branding.Subtitle != "x" {
			t.Fatalf("副标题应去空白，实际 %q", p.Branding.Subtitle)
		}
	})

	t.Run("缓存分钟数夹取", func(t *testing.T) {
		r, _, _ := newSettingsAPI(t)
		for _, tc := range []struct{ in, want int }{{0, 1}, {-5, 1}, {999, 120}, {15, 15}} {
			body := validBody()
			w := weatherBody()
			w["refreshMin"] = tc.in
			body["weather"] = w
			code, out := putSettings(t, r, body)
			if code != http.StatusOK {
				t.Fatalf("缓存分钟数只应夹取不应报错，实际 %d %+v", code, out)
			}
			if got := getSettingsPayload(t, r).Weather.RefreshMin; got != tc.want {
				t.Fatalf("refreshMin=%d 应夹为 %d，实际 %d", tc.in, tc.want, got)
			}
		}
	})
}

// TestSettingsPutAllowsEmptyCoordsWhenDisabled 天气关闭 + 坐标留空(0,0) 必须能保存。
//
// 这是前端依赖的一条语义：开关关闭时坐标输入框是空的，保存按钮必须放行，
// 否则用户"只想改个标题"会被一个自己没碰过的字段挡住（且该字段此时可能不可见）。
// 注意范围越界（如 lat=91）不在此豁免范围内，见下一个用例。
func TestSettingsPutAllowsEmptyCoordsWhenDisabled(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	body := validBody()
	body["weather"] = map[string]any{
		"enabled": false, "provider": "open-meteo", "lat": 0, "lon": 0, "label": "", "refreshMin": 10,
	}
	code, out := putSettings(t, r, body)
	if code != http.StatusOK {
		t.Fatalf("关闭天气时留空坐标应可保存，实际 %d %+v", code, out)
	}
	p := getSettingsPayload(t, r)
	if p.Weather.Enabled || p.Weather.Lat != 0 || p.Weather.Lon != 0 {
		t.Fatalf("落库值不符: %+v", p.Weather)
	}
}

// TestSettingsPutRejectsBadJSON 请求体不是合法 JSON 时报 400（不是 500）。
func TestSettingsPutRejectsBadJSON(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"branding":`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应返回 400，实际 %d: %s", w.Code, w.Body.String())
	}
}

// ————————————————— Logo 上传 —————————————————

// postLogo 发起一次 multipart 上传（字段名固定为 file）。
func postLogo(t *testing.T, r *gin.Engine, fileName string, content []byte) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part := textproto.MIMEHeader{}
	part.Set("Content-Disposition", `form-data; name="file"; filename="`+fileName+`"`)
	// 故意声明成 application/octet-stream：服务端必须靠内容嗅探判定，不能信客户端声明
	part.Set("Content-Type", "application/octet-stream")
	pw, err := mw.CreatePart(part)
	if err != nil {
		t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := pw.Write(content); err != nil {
		t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/logo", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON: %s", w.Body.String())
		}
	}
	return w.Code, out
}

// pngBytes 返回一段合法 PNG 头（嗅探足以判定为 image/png）。
func pngBytes() []byte {
	head := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	return append(head, bytes.Repeat([]byte{0x01}, 256)...)
}

func jpegBytes() []byte {
	head := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	return append(head, bytes.Repeat([]byte{0x02}, 256)...)
}

func webpBytes() []byte {
	head := append([]byte("RIFF"), []byte{0x40, 0x00, 0x00, 0x00}...)
	head = append(head, []byte("WEBPVP8 ")...)
	return append(head, bytes.Repeat([]byte{0x03}, 128)...)
}

func gifBytes() []byte { return append([]byte("GIF89a"), bytes.Repeat([]byte{0x04}, 128)...) }

func svgBytes() []byte {
	return []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
}

// TestLogoUploadAccepts 三种合法图片格式都要能上传，且落盘文件名由服务端定。
func TestLogoUploadAccepts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes []byte
		ext   string
	}{
		{"png", pngBytes(), "png"},
		{"jpeg", jpegBytes(), "jpg"},
		{"webp", webpBytes(), "webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, deps := newSettingsAPI(t)
			code, out := postLogo(t, r, "随便什么名字.exe", tc.bytes)
			if code != http.StatusOK {
				t.Fatalf("合法 %s 应上传成功，实际 %d %+v", tc.name, code, out)
			}
			url, _ := dataOf(t, out)["logoUrl"].(string)
			if !strings.HasPrefix(url, config.BrandingLogoPrefix) {
				t.Fatalf("logoUrl 应指向本站端点，实际 %q", url)
			}
			if !strings.Contains(url, "v=") {
				t.Fatalf("logoUrl 应带版本号以绕开缓存，实际 %q", url)
			}
			// 落盘路径必须是 logo.<ext>，绝不能是上传时的文件名
			want := filepath.Join(deps.Cfg.Server.DataDir, brandingRelDir, brandingLogoBase+"."+tc.ext)
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("Logo 未落到 %s: %v", want, err)
			}
			if _, err := os.Stat(filepath.Join(deps.Cfg.Server.DataDir, brandingRelDir, "随便什么名字.exe")); err == nil {
				t.Fatal("绝不能采用上传时的文件名（路径穿越风险）")
			}
			// 设置里的 logoUrl 同步更新
			if got := getSettingsPayload(t, r).Branding.LogoURL; got != url {
				t.Fatalf("branding.logoUrl 未同步: %q != %q", got, url)
			}
		})
	}
}

// TestLogoUploadRejects 四类非法上传必须被拒：SVG（XSS）、超大、类型不符、空文件。
func TestLogoUploadRejects(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content []byte
	}{
		{"SVG 内嵌 script", "logo.png", svgBytes()},
		{"纯文本伪装成 png", "logo.png", []byte("这不是图片，只是一段文字")},
		{"GIF 不在白名单", "logo.gif", gifBytes()},
		{"超过 200KB", "logo.png", append(pngBytes(), bytes.Repeat([]byte{0x09}, 201<<10)...)},
		{"空文件", "logo.png", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _, deps := newSettingsAPI(t)
			code, out := postLogo(t, r, c.file, c.content)
			if code != http.StatusBadRequest {
				t.Fatalf("%s 应被拒绝（400），实际 %d %+v", c.name, code, out)
			}
			// 被拒时不得落盘任何文件
			entries, _ := os.ReadDir(filepath.Join(deps.Cfg.Server.DataDir, brandingRelDir))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), brandingLogoBase+".") {
					t.Fatalf("%s 被拒了却仍落盘: %s", c.name, e.Name())
				}
			}
		})
	}
}

// TestLogoUploadRejectsPathTraversal 文件名带 ../ 也不能写到 dataDir 之外。
func TestLogoUploadRejectsPathTraversal(t *testing.T) {
	r, _, deps := newSettingsAPI(t)
	code, _ := postLogo(t, r, "../../evil.png", pngBytes())
	if code != http.StatusOK {
		t.Fatalf("合法 PNG 只是文件名可疑，应正常接受（落盘名由服务端定），实际 %d", code)
	}
	evil := filepath.Join(filepath.Dir(deps.Cfg.Server.DataDir), "evil.png")
	if _, err := os.Stat(evil); err == nil {
		t.Fatalf("发生了路径穿越，文件被写到 %s", evil)
	}
}

// TestLogoServeAndDelete 上传 → 可读 → 删除 → 404（前端回落纯文字）。
func TestLogoServeAndDelete(t *testing.T) {
	r, _, deps := newSettingsAPI(t)
	dir := filepath.Join(deps.Cfg.Server.DataDir, brandingRelDir)

	// 未上传时应 404
	if code, _ := doJSON(t, r, http.MethodGet, config.BrandingLogoPrefix, nil); code != http.StatusNotFound {
		t.Fatalf("未上传 Logo 时应 404，实际 %d", code)
	}

	if code, out := postLogo(t, r, "x.png", pngBytes()); code != http.StatusOK {
		t.Fatalf("上传失败: %d %+v", code, out)
	}
	req := httptest.NewRequest(http.MethodGet, config.BrandingLogoPrefix, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("应能读到 Logo，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type 错误: %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("Logo 应带 Cache-Control: no-cache（换图后浏览器才会更新），实际 %q", cc)
	}

	// 换格式上传：旧的 png 必须被清掉，否则服务端按固定顺序取图会取到旧图
	if code, out := postLogo(t, r, "x.jpg", jpegBytes()); code != http.StatusOK {
		t.Fatalf("换成 jpg 失败: %d %+v", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "logo.png")); err == nil {
		t.Fatal("换格式后旧的 logo.png 未清理（页面会一直显示旧图）")
	}

	// 删除
	code, out := doJSON(t, r, http.MethodDelete, "/api/v1/settings/logo", nil)
	if code != http.StatusOK {
		t.Fatalf("删除 Logo 失败: %d %+v", code, out)
	}
	if got := getSettingsPayload(t, r).Branding.LogoURL; got != "" {
		t.Fatalf("删除后 branding.logoUrl 应为空，实际 %q", got)
	}
	if code, _ := doJSON(t, r, http.MethodGet, config.BrandingLogoPrefix, nil); code != http.StatusNotFound {
		t.Fatalf("删除后应 404，实际 %d", code)
	}
}

// ————————————————— 天气接口 —————————————————

// TestWeatherDisabledReturnsReason 开关关闭时返回 ok=false + reason=disabled，HTTP 仍 200。
func TestWeatherDisabledReturnsReason(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	code, out := doJSON(t, r, http.MethodGet, "/api/v1/weather", nil)
	if code != http.StatusOK {
		t.Fatalf("天气关闭不是错误，HTTP 应 200，实际 %d", code)
	}
	d := dataOf(t, out)
	if d["ok"] != false {
		t.Fatalf("ok 应为 false: %+v", d)
	}
	if d["reason"] != weather.ReasonDisabled {
		t.Fatalf("reason 应为 %s，实际 %v", weather.ReasonDisabled, d["reason"])
	}
	if msg, _ := d["message"].(string); msg == "" {
		t.Fatal("message 不能为空（前端需要一个可显示的提示）")
	}
}

// TestWeatherEnabledProxiesBackend 开启后由后端代理第三方，返回中文天气描述。
//
// 顺带锁死"浏览器不直连第三方"：这里注入 httptest 服务器替换真实地址，
// 若哪天改成前端直连，这个用例就不可能通过。
func TestWeatherEnabledProxiesBackend(t *testing.T) {
	r, _, deps := newSettingsAPI(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"current":{"temperature_2m":28.4,"weather_code":95,"wind_speed_10m":9.9},` +
			`"current_units":{"temperature_2m":"°C","wind_speed_10m":"km/h"}}`))
	}))
	defer srv.Close()
	// 把服务商指向测试服务器（进程内唯一实例，缓存也复用它）
	deps.Weather.Register(weather.NewOpenMeteo(nil, srv.URL))

	code, out := putSettings(t, r, validBody())
	if code != http.StatusOK {
		t.Fatalf("开启天气失败: %d %+v", code, out)
	}
	code, out = doJSON(t, r, http.MethodGet, "/api/v1/weather", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /weather 失败: %d %+v", code, out)
	}
	d := dataOf(t, out)
	if d["ok"] != true {
		t.Fatalf("应查询成功: %+v", d)
	}
	if d["temp"] != 28.4 || d["code"] != float64(95) || d["wind"] != 9.9 {
		t.Fatalf("观测值错误: %+v", d)
	}
	if d["unit"] != "°C" || d["windUnit"] != "km/h" {
		t.Fatalf("单位错误: %+v", d)
	}
	text, _ := d["text"].(string)
	if !strings.Contains(text, "雷") {
		t.Fatalf("code 95 应给出含「雷」的中文描述，实际 %q", text)
	}
	if d["provider"] != config.WeatherProviderOpenMeteo {
		t.Fatalf("provider 错误: %v", d["provider"])
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("应只出网 1 次，实际 %d", n)
	}
	// 坐标不下发到前端（部署位置属于敏感信息）
	if _, exists := d["timeoutMs"]; exists {
		t.Fatal("天气快照不应下发服务端参数")
	}
}

// TestWeatherCoordsMissingIsExplicitAtRuntime 开了天气但坐标没填时，
// 运行期必须显式返回 coords_invalid，而不是返回空白或"0℃ 晴"。
//
// 这条状态 PUT 会拦住，但仍可能由 yaml 直接产生（enabled: true + 没填坐标），
// 所以运行期必须自己兜住。
func TestWeatherCoordsMissingIsExplicitAtRuntime(t *testing.T) {
	r, st, _ := newSettingsAPI(t)
	if err := st.SetSetting(testCtx(), settingKeyWeather,
		`{"enabled":true,"provider":"open-meteo","lat":0,"lon":0,"label":"","refreshMin":10}`); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	code, out := doJSON(t, r, http.MethodGet, "/api/v1/weather", nil)
	if code != http.StatusOK {
		t.Fatalf("天气不可用不是服务端错误，HTTP 应 200，实际 %d", code)
	}
	d := dataOf(t, out)
	if d["ok"] != false || d["reason"] != weather.ReasonCoordsInvalid {
		t.Fatalf("应返回 %s 失败，实际: %+v", weather.ReasonCoordsInvalid, d)
	}
	if msg, _ := d["message"].(string); msg == "" {
		t.Fatal("message 不能为空（前端要提示用户去填坐标）")
	}
}

// TestSettingsPutRejectsBadCoordsEvenWhenDisabled 坐标越界无论开关状态都应拒绝
// （否则"关着的时候能存个非法值，一打开就静默失败"）。
func TestSettingsPutRejectsBadCoordsEvenWhenDisabled(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	body := validBody()
	body["weather"] = map[string]any{
		"enabled": false, "provider": "open-meteo", "lat": 91, "lon": 0, "label": "", "refreshMin": 10,
	}
	if code, out := putSettings(t, r, body); code != http.StatusBadRequest {
		t.Fatalf("纬度 91 应被拒绝（即使开关关闭），实际 %d %+v", code, out)
	}
}

// ————————————————— RuntimeConfig —————————————————

// TestRuntimeIncludesVersionAndCommit runtime 必须下发二进制身份（前端「关于」分组靠它排障），
// 且【不得】下发 dataDir —— 本端点无鉴权，路径属于不必要的信息泄露。
func TestRuntimeIncludesVersionAndCommit(t *testing.T) {
	r, _, _ := newSettingsAPI(t)
	code, out := doJSON(t, r, http.MethodGet, "/api/v1/system/runtime", nil)
	if code != http.StatusOK {
		t.Fatalf("runtime 失败: %d %+v", code, out)
	}
	d := dataOf(t, out)

	v, ok := d["version"].(string)
	if !ok || v == "" {
		t.Fatalf("runtime 应下发非空 version，实际 %v（前端「关于」会整组不渲染）", d["version"])
	}
	if v != config.Version {
		t.Fatalf("version 应为 %q，实际 %q", config.Version, v)
	}
	// commit 允许是 "unknown"（非 git 环境构建），但必须是字符串且键存在 ——
	// 键缺失会让前端拿不到「关于」，而 "unknown" 恰好是正确答案（解压源码包构建）。
	c, ok := d["commit"].(string)
	if !ok || c == "" {
		t.Fatalf("runtime 应下发 commit（非 git 环境为 \"unknown\"），实际 %v", d["commit"])
	}
	if _, exists := d["dataDir"]; exists {
		t.Fatal("runtime 不应下发 dataDir（无鉴权端点上的路径泄露）")
	}
}

// TestRuntimeIncludesBrandingAndWeather 首屏 runtime 必须带上品牌与天气开关，
// 且取值同样遵循 DB > yaml > 默认（避免刷新时先闪一下默认的 MonitorAll）。
func TestRuntimeIncludesBrandingAndWeather(t *testing.T) {
	r, _, _ := newSettingsAPI(t)

	code, out := doJSON(t, r, http.MethodGet, "/api/v1/system/runtime", nil)
	if code != http.StatusOK {
		t.Fatalf("runtime 失败: %d %+v", code, out)
	}
	d := dataOf(t, out)
	b, ok := d["branding"].(map[string]any)
	if !ok {
		t.Fatalf("runtime 缺少 branding: %+v", d)
	}
	if b["title"] != config.DefaultBrandingTitle {
		t.Fatalf("默认品牌名错误: %v", b["title"])
	}
	wr, ok := d["weather"].(map[string]any)
	if !ok {
		t.Fatalf("runtime 缺少 weather: %+v", d)
	}
	if wr["enabled"] != false {
		t.Fatalf("天气开关默认应 false: %v", wr["enabled"])
	}

	// 写入 DB 后 runtime 必须立刻反映（前端首屏就不该再闪默认值）
	if code, out := putSettings(t, r, validBody()); code != http.StatusOK {
		t.Fatalf("PUT 失败: %d %+v", code, out)
	}
	_, out = doJSON(t, r, http.MethodGet, "/api/v1/system/runtime", nil)
	d = dataOf(t, out)
	b = d["branding"].(map[string]any)
	if b["title"] != "机器人监控" {
		t.Fatalf("runtime 未读到 DB 里的品牌名: %v", b["title"])
	}
	if d["weather"].(map[string]any)["enabled"] != true {
		t.Fatal("runtime 未反映天气开关已开启")
	}
	// 天气段只下发 enabled / provider，不下发坐标等部署信息
	if _, exists := d["weather"].(map[string]any)["lat"]; exists {
		t.Fatal("runtime 的 weather 段不应下发坐标")
	}
}

// testCtx 返回一个测试用 context（store 各方法都接受 ctx）。
func testCtx() context.Context { return context.Background() }
