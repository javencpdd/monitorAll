package replay

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/monitorall/monitorall/internal/model"
	"github.com/tidwall/gjson"
)

// ————————————————— 流式解析：绝不把整个文件读进内存 —————————————————

// iterateRecords 逐条遍历文件中的 JSON 记录并回调 cb。
// 支持三种形态（自动识别，无需调用方声明）：
//  1. 顶层 JSON 数组 —— dec.Token() 吃掉 '['，再按 dec.More() 逐元素解码
//  2. NDJSON —— 一行一个完整 JSON 值，Decoder 连续 Decode 即可
//  3. 单个 JSON 对象 —— 首次 Decode 拿到整个对象，第二次返回 io.EOF 自然结束
//
// 全程只有一个 64KiB 的 bufio.Reader，内存占用与文件大小无关。
func iterateRecords(r io.Reader, cb func(any) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	if err := skipSpace(br); err != nil {
		return err
	}
	head, err := br.Peek(1)
	if err != nil {
		return err
	}
	if head[0] == '[' {
		return iterateArray(br, cb)
	}
	return iterateSequence(br, cb)
}

// skipSpace 丢弃开头的空白字符（Peek 不消费，需显式 Discard）。
func skipSpace(br *bufio.Reader) error {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return err
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = br.Discard(1)
		default:
			return nil
		}
	}
}

// iterateArray 解析顶层数组形态。
func iterateArray(br *bufio.Reader, cb func(any) error) error {
	dec := json.NewDecoder(br)
	dec.UseNumber() // 保留数字精度（时间戳可能超出 float64 安全整数范围的边缘）
	if _, err := dec.Token(); err != nil {
		return err
	}
	for dec.More() {
		var v any
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if err := cb(v); err != nil {
			return err
		}
	}
	return nil
}

// iterateSequence 解析 NDJSON / 单对象形态。
func iterateSequence(br *bufio.Reader, cb func(any) error) error {
	dec := json.NewDecoder(br)
	dec.UseNumber()
	for {
		var v any
		err := dec.Decode(&v)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := cb(v); err != nil {
			return err
		}
	}
}

// ————————————————— 字段提取 —————————————————

// pickPath 按点分路径（如 "pose.header.stamp"）在嵌套 map 中取值。
// 命中即返回，零分配；未命中时由调用方决定是否回退到 gjson。
func pickPath(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// pick 先走零分配的点分路径，未命中再回退 gjson（支持完整 JSONPath 语法）。
func pick(v any, path string) (any, bool) {
	if val, ok := pickPath(v, path); ok {
		return val, true
	}
	if path == "" || v == nil {
		return nil, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	res := gjson.GetBytes(b, path)
	if !res.Exists() {
		return nil, false
	}
	return decodeGJSON(res), true
}

// decodeGJSON 把 gjson 结果转成 Go 原生值。
func decodeGJSON(res gjson.Result) any {
	switch {
	case res.IsArray():
		out := make([]any, 0)
		for _, item := range res.Array() {
			out = append(out, decodeGJSON(item))
		}
		return out
	case res.IsObject():
		out := map[string]any{}
		res.ForEach(func(key, value gjson.Result) bool {
			out[key.String()] = decodeGJSON(value)
			return true
		})
		return out
	case res.Type == gjson.Number:
		return json.Number(res.Raw)
	case res.Type == gjson.True:
		return true
	case res.Type == gjson.False:
		return false
	case res.Type == gjson.Null:
		return nil
	default:
		return res.String()
	}
}

// ————————————————— 时间戳归一化 —————————————————

// extractTs 从一条记录里取出毫秒时间戳；取不到返回 0（由调用方用虚拟时钟兜底）。
func extractTs(v any) int64 {
	switch t := v.(type) {
	case map[string]any:
		// ROS header：{"stamp":{"sec":..,"nanosec":..}}
		if ms := model.StampToMs(t); ms > 0 {
			return ms
		}
	case string:
		if tt, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return tt.UnixMilli()
		}
	}
	if f, ok := model.ParseFloat64(v); ok {
		return normalizeTs(f)
	}
	return 0
}

// normalizeTs 按量级判别时间单位：ns / us / ms / s。
func normalizeTs(f float64) int64 {
	switch {
	case f >= 1e17:
		return int64(f / 1e6) // 纳秒
	case f >= 1e14:
		return int64(f / 1e3) // 微秒
	case f >= 1e11:
		return int64(f) // 毫秒
	default:
		return int64(f * 1000) // 秒
	}
}

// ————————————————— 经纬度识别（地图轨迹） —————————————————

var (
	latKeys = []string{"lat", "latitude"}
	lonKeys = []string{"lon", "lng", "long", "longitude"}
	altKeys = []string{"alt", "altitude", "height"}
	yawKeys = []string{"yaw", "heading", "yaw_deg", "yawDeg"}
)

// findGeo 在嵌套 map 中（深度上限 3）寻找经纬度；找到返回纬度、经度、高度、朝向。
func findGeo(root map[string]any) (lat, lon, alt, yaw float64, ok bool) {
	foundLat, foundLon := false, false
	var walk func(m map[string]any, depth int)
	walk = func(m map[string]any, depth int) {
		if depth > 3 || (foundLat && foundLon) {
			return
		}
		for _, k := range latKeys {
			if v, hit := m[k]; hit && !foundLat {
				if f, ok2 := model.ParseFloat64(v); ok2 {
					lat, foundLat = f, true
					break
				}
			}
		}
		for _, k := range lonKeys {
			if v, hit := m[k]; hit && !foundLon {
				if f, ok2 := model.ParseFloat64(v); ok2 {
					lon, foundLon = f, true
					break
				}
			}
		}
		for _, k := range altKeys {
			if v, hit := m[k]; hit && alt == 0 {
				if f, ok2 := model.ParseFloat64(v); ok2 {
					alt = f
					break
				}
			}
		}
		for _, k := range yawKeys {
			if v, hit := m[k]; hit && yaw == 0 {
				if f, ok2 := model.ParseFloat64(v); ok2 {
					yaw = f
					break
				}
			}
		}
		if foundLat && foundLon {
			return
		}
		for _, v := range m {
			if child, isMap := v.(map[string]any); isMap {
				walk(child, depth+1)
			}
		}
	}
	walk(root, 0)
	return lat, lon, alt, yaw, foundLat && foundLon
}

// inferPayloadType 在通用推断（model.InferPayloadType）之上增加经纬度识别：
// 含 lat/lon 的记录判为 geo_pose，以便地图卡直接渲染轨迹。
func inferPayloadType(v any) model.PayloadType {
	if m, ok := v.(map[string]any); ok {
		if _, _, _, _, ok := findGeo(m); ok {
			return model.PayloadGeoPose
		}
	}
	return model.InferPayloadType(v)
}

// ————————————————— 帧载荷构造 —————————————————

// buildPayload 按通道固定的 payloadType 构造载荷与字段提示。
func buildPayload(pt model.PayloadType, value any, ts int64) (any, map[string]model.FieldHint) {
	switch pt {
	case model.PayloadGeoPose:
		lat, lon, alt, yaw, ok := 0.0, 0.0, 0.0, 0.0, false
		if m, isMap := value.(map[string]any); isMap {
			lat, lon, alt, yaw, ok = findGeo(m)
		}
		p := model.GeoPosePayload{Lat: lat, Lon: lon, CRS: model.DefaultCRS, T: ts}
		if alt != 0 {
			p.Alt = alt
		}
		if yaw != 0 {
			p.YawDeg = yaw
		}
		_ = ok
		return p, nil
	case model.PayloadScalar:
		f, _ := model.ParseFloat64(value)
		return model.ScalarPayload{Value: f}, nil
	case model.PayloadTimeSeries:
		m, _ := value.(map[string]any)
		fields, hints := model.FlattenNumericFields(m, "")
		return model.TimeSeriesPayload{T: ts, Fields: fields}, hints
	case model.PayloadTable:
		return model.TablePayload{
			Columns: columnsOf(value),
			Rows:    [][]any{rowOf(value)},
			Total:   1,
		}, nil
	default:
		return model.JSONPayload{Root: value}, model.BuildSchemaHint(value, "")
	}
}

// columnsOf 由首条记录的首层键推断表格列。
func columnsOf(v any) []model.TableColumn {
	m, ok := v.(map[string]any)
	if !ok {
		return []model.TableColumn{{Key: "value", Title: "value", Type: "string"}}
	}
	out := make([]model.TableColumn, 0, len(m))
	for _, k := range sortedKeysOf(m) {
		out = append(out, model.TableColumn{Key: k, Title: k, Type: columnTypeOf(m[k])})
	}
	return out
}

// rowOf 按 columnsOf 的列序取一行。
func rowOf(v any) []any {
	row := make([]any, 0)
	m, ok := v.(map[string]any)
	if !ok {
		return append(row, v)
	}
	for _, c := range columnsOf(v) {
		row = append(row, m[c.Key])
	}
	return row
}

// columnTypeOf 推断列类型。
func columnTypeOf(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case nil, string:
		return "string"
	default:
		if _, ok := model.ParseFloat64(v); ok {
			return "number"
		}
		return "string"
	}
}

// sortedKeysOf 返回排序后的键（保证列顺序稳定）。
func sortedKeysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
