package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
	"github.com/monitorall/monitorall/internal/weather"
)

// ————————————————— 全局设置：品牌 + 天气（v4 app_settings） —————————————————
//
// 优先级链：**DB > config.yaml > 代码默认值**。
//   - DB（app_settings 表）：用户在设置页改过的值，最高优先级；
//   - config.yaml：部署时下发的值（可被环境变量覆盖），供"无人值守部署"使用；
//   - 代码默认值：config.DefaultConfig() 里的常量，兜底。
//
// 为什么 DB 里只存「用户改过的字段」：yaml 的存在意义是"部署时统一下发"，
// 如果用户在 UI 里改了一项就把整段配置写进 DB，之后运维改 yaml 就永远不生效了
// （表现为"改了配置文件重启也没用"，且没有任何报错）。因此 DB 里缺的字段回落到 yaml。

const (
	// settingKeyBranding / settingKeyWeather 为 app_settings 表的两个 key。
	settingKeyBranding = "branding"
	settingKeyWeather  = "weather"

	// maxLogoSizeBytes 为 Logo 文件大小上限（200KB：顶栏图标，超出说明传错了图）。
	maxLogoSizeBytes = 200 << 10
	// brandingRelDir 为 Logo 落盘目录（相对 dataDir，由 config.EnsureDirs 创建）。
	brandingRelDir = "branding"
	// brandingLogoBase 为落盘文件名主干（扩展名按嗅探结果定，绝不采用上传时的文件名）。
	brandingLogoBase = "logo"
	// logoVQuery 为 Logo URL 的版本参数名（毫秒时间戳，绕开浏览器缓存）。
	logoVQuery = "v"
)

// logoContentTypes 为允许的 Logo 图片类型（按内容嗅探结果判定，不信客户端给的 Content-Type）。
//
// 明确拒绝 SVG：SVG 是 XML，可内嵌 <script>，被当作 <img> 之外的资源加载或在某些浏览器
// 直开时就是存储型 XSS。GIF 也不在列表内（顶栏图标不需要动画，且动图会一直占 CPU）。
var logoContentTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

// ————————————————— 对外 payload —————————————————

// BrandingPayload 为品牌设置（json 名与前端约定，勿改）。
type BrandingPayload struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	LogoURL  string `json:"logoUrl"`
}

// WeatherSettingsPayload 为天气设置（json 名与前端约定，勿改）。
type WeatherSettingsPayload struct {
	Enabled    bool    `json:"enabled"`
	Provider   string  `json:"provider"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Label      string  `json:"label"`
	RefreshMin int     `json:"refreshMin"`
}

// SettingsPayload 为设置页整体 payload。
type SettingsPayload struct {
	Branding BrandingPayload        `json:"branding"`
	Weather  WeatherSettingsPayload `json:"weather"`
}

// WeatherSnapshot 为 GET /api/v1/weather 的响应体（与 weather.Snapshot 同一结构，
// 用别名而不是复制一份：复制会让两边的 json tag 悄悄漂移）。
type WeatherSnapshot = weather.Snapshot

// brandingPatch / weatherPatch 为 DB 里 JSON 的反序列化目标。
//
// 用指针字段是刻意的：只有"字段在 JSON 里出现过"才算用户设置过，
// 未出现（nil）才回落到 yaml。用值类型无法区分"用户显式关掉了天气"和"用户没写过"——
// 前者必须取胜，否则 yaml 里 enabled=true 会让用户在 UI 上永远关不掉天气。
type brandingPatch struct {
	Title    *string `json:"title"`
	Subtitle *string `json:"subtitle"`
	LogoURL  *string `json:"logoUrl"`
}

type weatherPatch struct {
	Enabled    *bool    `json:"enabled"`
	Provider   *string  `json:"provider"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	Label      *string  `json:"label"`
	RefreshMin *int     `json:"refreshMin"`
}

// ————————————————— 读取与合并 —————————————————

// loadSettings 按 DB > yaml > 默认 的顺序合并出完整设置。
func (d *Deps) loadSettings(c *gin.Context) (SettingsPayload, error) {
	out := settingsFromConfig(d.Cfg)
	if d.Store == nil {
		return out, nil
	}
	ctx := requestContext(c)
	for _, key := range []string{settingKeyBranding, settingKeyWeather} {
		raw, err := d.Store.GetSetting(ctx, key)
		if err != nil {
			return out, err
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		switch key {
		case settingKeyBranding:
			var p brandingPatch
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				// DB 里的 JSON 坏了不能让整个设置页打不开：记日志 + 回落 yaml。
				// 但也不能静默吞掉 —— 否则用户永远不知道自己改过的值没生效。
				d.Log.Warn("app_settings.branding 解析失败，已回落配置文件", "err", err)
				continue
			}
			applyBrandingPatch(&out.Branding, p)
		case settingKeyWeather:
			var p weatherPatch
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				d.Log.Warn("app_settings.weather 解析失败，已回落配置文件", "err", err)
				continue
			}
			applyWeatherPatch(&out.Weather, p)
		}
	}
	return out, nil
}

// settingsFromConfig 用生效配置（yaml + 环境变量）铺一份基础值。
func settingsFromConfig(cfg *config.Config) SettingsPayload {
	return SettingsPayload{
		Branding: BrandingPayload{
			Title:    cfg.Branding.Title,
			Subtitle: cfg.Branding.Subtitle,
			LogoURL:  cfg.Branding.LogoURL,
		},
		Weather: WeatherSettingsPayload{
			Enabled:    cfg.Weather.Enabled,
			Provider:   cfg.Weather.Provider,
			Lat:        cfg.Weather.Lat,
			Lon:        cfg.Weather.Lon,
			Label:      cfg.Weather.Label,
			RefreshMin: cfg.Weather.RefreshMin,
		},
	}
}

// applyBrandingPatch 把 DB 里出现过的字段覆盖到基础值上。
func applyBrandingPatch(b *BrandingPayload, p brandingPatch) {
	if p.Title != nil {
		b.Title = *p.Title
	}
	if p.Subtitle != nil {
		b.Subtitle = *p.Subtitle
	}
	if p.LogoURL != nil {
		b.LogoURL = *p.LogoURL
	}
}

// applyWeatherPatch 同 applyBrandingPatch（天气段）。
func applyWeatherPatch(w *WeatherSettingsPayload, p weatherPatch) {
	if p.Enabled != nil {
		w.Enabled = *p.Enabled
	}
	if p.Provider != nil {
		w.Provider = *p.Provider
	}
	if p.Lat != nil {
		w.Lat = *p.Lat
	}
	if p.Lon != nil {
		w.Lon = *p.Lon
	}
	if p.Label != nil {
		w.Label = *p.Label
	}
	if p.RefreshMin != nil {
		w.RefreshMin = *p.RefreshMin
	}
}

// requestContext 返回 handler 用的 context（gin 的 Request.Context 在测试里可能为 nil）。
func requestContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil || c.Request.Context() == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// ————————————————— 校验 —————————————————

// fieldError 为一条字段级校验错误（响应用 detail 带出，前端做红字）。
type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// validateSettings 校验并归一化一份待写入的设置。
//
// 原则：能修的就修（去空白、回落默认、夹取区间），不能修的【明确报错并指出是哪个字段】。
// 静默截断会让用户以为保存成功了，实际显示的是被改过的值 —— 属于最难排查的那类问题。
func validateSettings(p *SettingsPayload) error {
	errs := validateBranding(&p.Branding)
	errs = append(errs, validateWeather(&p.Weather)...)
	if len(errs) > 0 {
		return apperr.New(apperr.InvalidParam, "设置校验未通过").WithDetail(errs)
	}
	return nil
}

// validateBranding 校验并归一化品牌段（就地修改，返回字段级错误）。
func validateBranding(b *BrandingPayload) []fieldError {
	errs := make([]fieldError, 0, 3)

	b.Title = strings.TrimSpace(b.Title)
	if b.Title == "" {
		// 空标题不算错误：回落默认值（顶栏总要有个名字）
		b.Title = config.DefaultBrandingTitle
	}
	b.Subtitle = strings.TrimSpace(b.Subtitle)
	b.LogoURL = strings.TrimSpace(b.LogoURL)

	if n := utf8.RuneCountInString(b.Title); n > config.BrandingTitleMaxRunes {
		errs = append(errs, fieldError{"branding.title",
			fmt.Sprintf("标题超长：%d 字，上限 %d 字", n, config.BrandingTitleMaxRunes)})
	}
	if n := utf8.RuneCountInString(b.Subtitle); n > config.BrandingSubtitleMaxRunes {
		errs = append(errs, fieldError{"branding.subtitle",
			fmt.Sprintf("副标题超长：%d 字，上限 %d 字", n, config.BrandingSubtitleMaxRunes)})
	}
	if err := validateLogoURL(b.LogoURL); err != nil {
		errs = append(errs, *err)
	}
	return errs
}

// validateWeather 校验并归一化天气段（就地修改，返回字段级错误）。
func validateWeather(w *WeatherSettingsPayload) []fieldError {
	errs := make([]fieldError, 0, 5)

	w.Provider = strings.TrimSpace(w.Provider)
	if w.Provider == "" {
		w.Provider = config.WeatherProviderOpenMeteo
	}
	w.Label = strings.TrimSpace(w.Label)

	if !config.IsWeatherProviderAllowed(w.Provider) {
		errs = append(errs, fieldError{"weather.provider",
			fmt.Sprintf("不支持的天气服务商 %q，可选：%s",
				w.Provider, strings.Join(config.WeatherProviders, " / "))})
	}
	if w.Lat < -90 || w.Lat > 90 {
		errs = append(errs, fieldError{"weather.lat",
			fmt.Sprintf("纬度超出范围：%v，应为 -90 ~ 90", w.Lat)})
	}
	if w.Lon < -180 || w.Lon > 180 {
		errs = append(errs, fieldError{"weather.lon",
			fmt.Sprintf("经度超出范围：%v，应为 -180 ~ 180", w.Lon)})
	}
	if n := utf8.RuneCountInString(w.Label); n > config.WeatherLabelMaxRunes {
		errs = append(errs, fieldError{"weather.label",
			fmt.Sprintf("地点名超长：%d 字，上限 %d 字", n, config.WeatherLabelMaxRunes)})
	}
	// 开关开着却拿不到坐标：必须报错，不能让它"开着但不显示"——
	// 那种状态用户只会看到挂件空白，而设置页显示"已开启"，两边都对不上。
	if w.Enabled && w.Lat == 0 && w.Lon == 0 {
		errs = append(errs, fieldError{"weather.lat",
			"开启天气前请先填写坐标（纬度与经度，WGS-84）"})
	}
	// 缓存分钟数夹取而不是报错：这是性能参数，没有"用户输错"的风险
	switch {
	case w.RefreshMin < config.WeatherRefreshMinMin:
		w.RefreshMin = config.WeatherRefreshMinMin
	case w.RefreshMin > config.WeatherRefreshMinMax:
		w.RefreshMin = config.WeatherRefreshMinMax
	}
	return errs
}

// validateLogoURL 校验 Logo 地址：只允许 http(s) 或后端自有端点。
//
// 拒绝 javascript: / data: / vbscript: 等一切其它 scheme —— LogoURL 会被前端直接写进
// <img src>，放行任意 scheme 就是存储型 XSS（且是"管理员自己存进去"的那种，最难发现）。
func validateLogoURL(raw string) *fieldError {
	if raw == "" {
		return nil // 空 = 无 Logo，合法
	}
	low := strings.ToLower(raw)
	if strings.HasPrefix(low, config.BrandingLogoPrefix) {
		return nil
	}
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return nil
	}
	return &fieldError{"branding.logoUrl",
		fmt.Sprintf("Logo 地址只允许 http(s) 链接或以 %s 开头的本站地址，当前值不合法", config.BrandingLogoPrefix)}
}

// ————————————————— HTTP handlers —————————————————

// getSettings 返回合并后的全量设置。
func (d *Deps) getSettings(c *gin.Context) {
	p, err := d.loadSettings(c)
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, p)
}

// putSettings 整体写入设置（校验不通过返回 400 + 字段级 detail）。
func (d *Deps) putSettings(c *gin.Context) {
	var p SettingsPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体不是合法的 JSON"))
		return
	}
	if err := validateSettings(&p); err != nil {
		Fail(c, err)
		return
	}
	if d.Store == nil {
		Fail(c, apperr.New(apperr.Internal, "持久化未就绪，无法保存设置"))
		return
	}
	ctx := requestContext(c)
	if err := d.saveSettings(ctx, &p); err != nil {
		Fail(c, err)
		return
	}
	d.Log.Info("全局设置已更新", "brandTitle", p.Branding.Title,
		"weatherEnabled", p.Weather.Enabled, "provider", p.Weather.Provider)
	// 回显合并后的结果，让前端拿到的就是"下一次 GET 会返回的东西"
	out, err := d.loadSettings(c)
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, out)
}

// saveSettings 把两个段分别序列化后写入 app_settings（同一个事务外的两次 upsert：
// 设置只有一份，两段之间没有一致性要求，分开写失败面更小）。
func (d *Deps) saveSettings(ctx context.Context, p *SettingsPayload) error {
	brandingJSON, err := json.Marshal(p.Branding)
	if err != nil {
		return apperr.Wrap(err, apperr.StoreError, "序列化品牌设置失败")
	}
	weatherJSON, err := json.Marshal(p.Weather)
	if err != nil {
		return apperr.Wrap(err, apperr.StoreError, "序列化天气设置失败")
	}
	if err := d.Store.SetSetting(ctx, settingKeyBranding, string(brandingJSON)); err != nil {
		return err
	}
	return d.Store.SetSetting(ctx, settingKeyWeather, string(weatherJSON))
}

// getWeather 返回当日天气快照；开关关闭时返回 ok=false + reason=disabled（HTTP 200）。
func (d *Deps) getWeather(c *gin.Context) {
	p, err := d.loadSettings(c)
	if err != nil {
		// 设置读取失败也要返回结构化的失败快照：这个接口挂在顶栏，
		// 返回裸 500 会让前端拿到非 JSON，挂件直接崩。
		d.Log.Warn("读取设置失败，天气按不可用返回", "err", err)
	}
	if !p.Weather.Enabled {
		OK(c, &WeatherSnapshot{
			OK:      false,
			Reason:  weather.ReasonDisabled,
			Message: "天气显示未开启",
		})
		return
	}
	if d.Weather == nil {
		OK(c, &WeatherSnapshot{
			OK:      false,
			Reason:  weather.ReasonProviderUnsupported,
			Message: "天气服务未初始化",
		})
		return
	}
	snap := d.Weather.Get(requestContext(c), weather.Options{
		Provider:   p.Weather.Provider,
		Lat:        p.Weather.Lat,
		Lon:        p.Weather.Lon,
		RefreshMin: p.Weather.RefreshMin,
		TimeoutMs:  d.Cfg.Weather.TimeoutMs,
	})
	if snap == nil {
		// 契约上 Get 永不返回 nil；这里再兜一层，避免 nil 被序列化成 null。
		snap = &WeatherSnapshot{OK: false, Reason: weather.ReasonProviderUnreachable, Message: "天气数据不可用"}
	}
	OK(c, snap)
}

// uploadLogo 接收 multipart 上传的 Logo，落盘为 <dataDir>/branding/logo.<ext>。
//
// 三条硬性约束（都是"上传功能"最容易出事的地方）：
//  1. 类型按【内容嗅探】判定，不信客户端给的 Content-Type 或文件名后缀 —— 后者完全可控；
//  2. 落盘文件名【固定】，绝不用上传时的文件名 —— 否则 ../../etc/x 就是路径穿越；
//  3. 拒绝 SVG（可内嵌 script，是存储型 XSS）。
func (d *Deps) uploadLogo(c *gin.Context) {
	fh, err := c.FormFile(uploadFieldName)
	if err != nil {
		Fail(c, apperr.Wrap(err, apperr.InvalidParam, "缺少上传文件字段 file"))
		return
	}
	if fh.Size <= 0 {
		Fail(c, apperr.New(apperr.InvalidParam, "上传文件为空"))
		return
	}
	if fh.Size > maxLogoSizeBytes {
		Fail(c, apperr.Newf(apperr.InvalidParam, "Logo 超过上限 %dKB（当前 %.0fKB）",
			maxLogoSizeBytes>>10, float64(fh.Size)/1024))
		return
	}

	f, err := fh.Open()
	if err != nil {
		Fail(c, apperr.Wrap(err, apperr.InvalidParam, "读取上传文件失败"))
		return
	}
	defer f.Close()

	// 只读前 512 字节做嗅探（net/http 的 sniff 上限），但整文件要校验大小后再落盘
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		Fail(c, apperr.Wrap(err, apperr.InvalidParam, "读取上传文件失败"))
		return
	}
	head = head[:n]
	detected := http.DetectContentType(head)
	ext, ok := logoContentTypes[detected]
	if !ok {
		Fail(c, apperr.Newf(apperr.InvalidParam,
			"Logo 只支持 PNG / JPEG / WebP，嗅探结果为 %q（不信任客户端声明的类型）", detected))
		return
	}
	// 回绕到文件头，保证落盘的是完整文件
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		Fail(c, apperr.Wrap(err, apperr.InvalidParam, "回绕上传文件失败"))
		return
	}

	dir := filepath.Join(d.Cfg.Server.DataDir, brandingRelDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		Fail(c, apperr.Wrap(err, apperr.StoreError, "创建 Logo 目录失败"))
		return
	}
	absPath := filepath.Join(dir, brandingLogoBase+"."+ext)

	// 完整读入内存后再写：先落盘再发现内容不对会留下一个坏文件，
	// 而这个坏文件会被 logoUrl 指向，表现为"顶栏图片裂了但设置页显示已上传"。
	buf := make([]byte, 0, fh.Size+1)
	buf, err = io.ReadAll(io.LimitReader(f, maxLogoSizeBytes+1))
	if err != nil {
		Fail(c, apperr.Wrap(err, apperr.InvalidParam, "读取上传文件失败"))
		return
	}
	if int64(len(buf)) > maxLogoSizeBytes {
		Fail(c, apperr.Newf(apperr.InvalidParam, "Logo 超过上限 %dKB", maxLogoSizeBytes>>10))
		return
	}
	if err := os.WriteFile(absPath, buf, 0o644); err != nil {
		Fail(c, apperr.Wrap(err, apperr.StoreError, "保存 Logo 失败"))
		return
	}
	// 清掉其它扩展名的旧 Logo：否则换格式后 logo.png 与 logo.jpg 同时存在，
	// 服务端按固定顺序取图会取到旧的，表现为"换了图但页面没变"。
	removeOtherLogos(dir, ext)

	// 写回 branding.logoUrl（带版本号，绕开浏览器缓存）
	url := config.BrandingLogoPrefix + "?" + logoVQuery + "=" + strconv.FormatInt(model.NowMs(), 10)
	if err := d.setBrandingLogoURL(c, url); err != nil {
		Fail(c, err)
		return
	}
	d.Log.Info("Logo 已更新", "ext", ext, "bytes", len(buf))
	OK(c, map[string]any{"logoUrl": url})
}

// deleteLogo 删除 Logo 文件并清空 branding.logoUrl。
func (d *Deps) deleteLogo(c *gin.Context) {
	dir := filepath.Join(d.Cfg.Server.DataDir, brandingRelDir)
	removed := removeOtherLogos(dir, "")
	if err := d.setBrandingLogoURL(c, ""); err != nil {
		Fail(c, err)
		return
	}
	d.Log.Info("Logo 已删除", "removed", removed)
	OK(c, map[string]any{"deleted": true, "logoUrl": ""})
}

// setBrandingLogoURL 只改 branding 段的 logoUrl：读出合并后的品牌段 → 改一个字段 → 整段写回。
//
// 为什么不是 UPDATE 单个字段：DB 里存的是 JSON blob，没有"改一个字段"的 SQL；
// 而直接 PUT 整个 payload 需要前端把品牌信息一起回传，多一次出错机会。
func (d *Deps) setBrandingLogoURL(c *gin.Context, url string) error {
	if d.Store == nil {
		return apperr.New(apperr.Internal, "持久化未就绪，无法保存设置")
	}
	cur, err := d.loadSettings(c)
	if err != nil {
		return err
	}
	cur.Branding.LogoURL = url
	// 只校验品牌段：Logo 上传不应该被天气段的现况挡住
	// （否则 yaml 里配了"天气开启但没坐标"时，用户会永远传不了 Logo，且报错文案完全不相关）。
	if errs := validateBranding(&cur.Branding); len(errs) > 0 {
		return apperr.New(apperr.InvalidParam, "设置校验未通过").WithDetail(errs)
	}
	blob, err := json.Marshal(cur.Branding)
	if err != nil {
		return apperr.Wrap(err, apperr.StoreError, "序列化品牌设置失败")
	}
	return d.Store.SetSetting(requestContext(c), settingKeyBranding, string(blob))
}

// serveBrandingLogo 返回 Logo 文件；不存在返回 404（前端回落成纯文字）。
func (d *Deps) serveBrandingLogo(c *gin.Context) {
	dir := filepath.Join(d.Cfg.Server.DataDir, brandingRelDir)
	for _, ext := range []string{"png", "jpg", "webp"} {
		p := filepath.Join(dir, brandingLogoBase+"."+ext)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// no-cache（不是 no-store）：必须每次回源确认，但允许 304 ——
		// 用 no-store 会让每次刷新都重新下载，顶栏图标白白多一次请求。
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, contentTypeOfExt(ext), data)
		return
	}
	Fail(c, apperr.New(apperr.NotFound, "尚未上传 Logo"))
}

// contentTypeOfExt 返回 Logo 文件的 Content-Type。
func contentTypeOfExt(ext string) string {
	switch ext {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// removeOtherLogos 删除 branding 目录下 logo.* 中扩展名不是 keepExt 的文件
// （keepExt 为空表示全部删除）。返回被删除的文件数。
func removeOtherLogos(dir string, keepExt string) int {
	removed := 0
	for _, ext := range []string{"png", "jpg", "jpeg", "webp"} {
		if keepExt != "" && ext == keepExt {
			continue
		}
		p := filepath.Join(dir, brandingLogoBase+"."+ext)
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed
}
