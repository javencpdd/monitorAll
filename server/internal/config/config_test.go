package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBrandingDefaults 锁死品牌默认值（前端首屏在没有 DB 值时渲染的就是这三个）。
func TestBrandingDefaults(t *testing.T) {
	c := DefaultConfig()
	if c.Branding.Title != "MonitorAll" {
		t.Fatalf("默认标题应为 MonitorAll，实际 %q", c.Branding.Title)
	}
	if c.Branding.Subtitle == "" {
		t.Fatal("默认副标题不应为空（顶栏会显示一个空行）")
	}
	if c.Branding.LogoURL != "" {
		t.Fatalf("默认不应有 Logo，实际 %q", c.Branding.LogoURL)
	}
}

// TestWeatherDefaultsClosed 天气默认必须关闭：开启会向第三方发请求，属于"改变了对外行为"。
func TestWeatherDefaultsClosed(t *testing.T) {
	c := DefaultConfig()
	if c.Weather.Enabled {
		t.Fatal("天气默认必须是关闭的（开启会向第三方发请求，必须显式开启）")
	}
	if c.Weather.Provider != WeatherProviderOpenMeteo {
		t.Fatalf("默认服务商应为 %s，实际 %q", WeatherProviderOpenMeteo, c.Weather.Provider)
	}
	if c.Weather.RefreshMin != DefaultWeatherRefreshMin || c.Weather.TimeoutMs != DefaultWeatherTimeoutMs {
		t.Fatalf("默认缓存/超时错误: refreshMin=%d timeoutMs=%d",
			c.Weather.RefreshMin, c.Weather.TimeoutMs)
	}
	if c.Weather.CoordsSet() {
		t.Fatal("默认坐标必须是「未设置」(0,0)，否则 UI 不会提示用户去填")
	}
}

// TestCoordsSet 校验 (0,0) 哨兵语义与合法坐标判定。
func TestCoordsSet(t *testing.T) {
	cases := []struct {
		lat, lon float64
		set      bool
		valid    bool
	}{
		{0, 0, false, true},
		{22.53, 113.93, true, true},
		{0, 113.93, true, true},
		{22.53, 0, true, true},
		{91, 0, true, false},
		{0, 181, true, false},
		{-90, -180, true, true},
	}
	for _, c := range cases {
		w := WeatherConfig{Lat: c.lat, Lon: c.lon}
		if got := w.CoordsSet(); got != c.set {
			t.Fatalf("CoordsSet(%v,%v)=%v 期望 %v", c.lat, c.lon, got, c.set)
		}
		if got := w.CoordsValid(); got != c.valid {
			t.Fatalf("CoordsValid(%v,%v)=%v 期望 %v", c.lat, c.lon, got, c.valid)
		}
	}
}

// TestApplyDefaultsFillsBrandingWeather yaml 里留空的字段必须被默认回填。
func TestApplyDefaultsFillsBrandingWeather(t *testing.T) {
	c := &Config{} // 全零值，模拟"配置文件里这两个段一个字都没写"
	c.applyDefaults()
	if c.Branding.Title != DefaultBrandingTitle || c.Branding.Subtitle != DefaultBrandingSubtitle {
		t.Fatalf("品牌默认值未回填: %+v", c.Branding)
	}
	if c.Weather.Provider != WeatherProviderOpenMeteo {
		t.Fatalf("服务商未回填: %q", c.Weather.Provider)
	}
	if c.Weather.RefreshMin != DefaultWeatherRefreshMin || c.Weather.TimeoutMs != DefaultWeatherTimeoutMs {
		t.Fatalf("缓存/超时未回填: %+v", c.Weather)
	}
	// Enabled 是 bool，不能被"回填"成 true —— 它只能由配置显式打开
	if c.Weather.Enabled {
		t.Fatal("Enabled 不应被 applyDefaults 改成 true")
	}
}

// TestWeatherProviderWhitelist 校验白名单只放行已实现的 provider。
func TestWeatherProviderWhitelist(t *testing.T) {
	if !IsWeatherProviderAllowed(WeatherProviderOpenMeteo) {
		t.Fatal("open-meteo 必须在白名单内")
	}
	for _, bad := range []string{"", "amap", "open-meteo ", "OPEN-METEO", "javascript:alert(1)"} {
		if IsWeatherProviderAllowed(bad) {
			t.Fatalf("%q 不应被白名单放行", bad)
		}
	}
}

// TestEnvOverridesBrandingWeather applyEnv 走反射，新增字段应自动支持环境变量覆盖。
func TestEnvOverridesBrandingWeather(t *testing.T) {
	t.Setenv("MONITORALL_BRANDING_TITLE", "机器人监控")
	t.Setenv("MONITORALL_WEATHER_ENABLED", "true")
	t.Setenv("MONITORALL_WEATHER_LAT", "22.53")
	t.Setenv("MONITORALL_WEATHER_LON", "113.93")
	t.Setenv("MONITORALL_WEATHER_REFRESHMIN", "30")

	c := DefaultConfig()
	if err := applyEnv(c); err != nil {
		t.Fatalf("应用环境变量失败: %v", err)
	}
	if c.Branding.Title != "机器人监控" {
		t.Fatalf("环境变量未覆盖标题: %q", c.Branding.Title)
	}
	if !c.Weather.Enabled {
		t.Fatal("环境变量未打开天气开关")
	}
	if c.Weather.Lat != 22.53 || c.Weather.Lon != 113.93 {
		t.Fatalf("环境变量未覆盖坐标: %v,%v", c.Weather.Lat, c.Weather.Lon)
	}
	if c.Weather.RefreshMin != 30 {
		t.Fatalf("环境变量未覆盖缓存分钟数: %d", c.Weather.RefreshMin)
	}
}

// TestEnsureDirsCreatesBrandingDir Logo 落盘目录必须由 EnsureDirs 建好，
// 否则"首次上传 Logo"会随机器的 dataDir 状态成功或失败。
func TestEnsureDirsCreatesBrandingDir(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.Server.DataDir = dir
	c.Server.CertDir = filepath.Join(dir, "cert")
	c.Store.DSN = filepath.Join(dir, "m.db")
	c.Server.SecretKeyFile = filepath.Join(dir, "secret.key")
	if err := EnsureDirs(c); err != nil {
		t.Fatalf("EnsureDirs 失败: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "branding"))
	if err != nil {
		t.Fatalf("branding 目录未创建: %v", err)
	}
	if !st.IsDir() {
		t.Fatal("branding 不是目录")
	}
}
