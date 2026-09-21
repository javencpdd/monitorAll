package ros

import (
	"sort"
	"strings"

	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— ROS msgType → payloadType 映射表（架构 §8.4） —————————————————

// MsgMapping 描述一种 ROS 消息类型的归一化方式。
type MsgMapping struct {
	// PayloadType 为归一化后的帧类型。
	PayloadType model.PayloadType
	// Renderer 为推荐渲染器。
	Renderer string
	// ScalarField 为标量提取路径（data / voltage 等），仅 scalar 使用。
	ScalarField string
	// Unit 为标量单位，仅 scalar 使用。
	Unit string
	// Label 为标量语义名，仅 scalar 使用。
	Label string
}

// msgTypeMapping 的 key 一律是**归一化后的 ROS1 形式**（nav_msgs/Odometry）。
var msgTypeMapping = map[string]MsgMapping{
	"std_msgs/Float32": {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/Float64": {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/Int32":   {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/Int64":   {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/UInt32":  {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/UInt64":  {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/Bool":    {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "data", Label: "value"},
	"std_msgs/String":  {PayloadType: model.PayloadScalar, Renderer: "json-tree"},

	"sensor_msgs/BatteryState": {PayloadType: model.PayloadScalar, Renderer: "gauge", ScalarField: "voltage", Unit: "V", Label: "voltage"},
	"sensor_msgs/Imu":          {PayloadType: model.PayloadTimeSeries, Renderer: "line-chart"},
	"nav_msgs/Odometry":        {PayloadType: model.PayloadTimeSeries, Renderer: "line-chart"},
	"sensor_msgs/NavSatFix":    {PayloadType: model.PayloadGeoPose, Renderer: "map"},
	"sensor_msgs/Image":        {PayloadType: model.PayloadImage, Renderer: "image"},
	"sensor_msgs/CompressedImage": {PayloadType: model.PayloadImage, Renderer: "image"},
	"sensor_msgs/LaserScan":       {PayloadType: model.PayloadHistogram, Renderer: "histogram"},
	"rosgraph_msgs/Log":           {PayloadType: model.PayloadLogLine, Renderer: "log-stream"},
}

// Lookup 查询归一化后的映射（未命中返回 false）。
func Lookup(msgType string) (MsgMapping, bool) {
	m, ok := msgTypeMapping[NormalizeMsgType(msgType)]
	return m, ok
}

// PayloadTypeFor 返回某 msgType 对应的 payloadType，未知类型返回 json（R5）。
func PayloadTypeFor(msgType string) model.PayloadType {
	if m, ok := Lookup(msgType); ok {
		return m.PayloadType
	}
	return model.PayloadJSON
}

// RecommendedRenderer 返回推荐渲染器；未知类型返回 json-tree。
func RecommendedRenderer(msgType string) string {
	if m, ok := Lookup(msgType); ok && m.Renderer != "" {
		return m.Renderer
	}
	return "json-tree"
}

// AllMappings 返回全部映射（按 msgType 排序，供调试接口使用）。
func AllMappings() map[string]MsgMapping {
	out := make(map[string]MsgMapping, len(msgTypeMapping))
	for k, v := range msgTypeMapping {
		out[k] = v
	}
	return out
}

// ————————————————— 归一化辅助（msg → payload） —————————————————

// BuildScalar 构造 scalar payload（从 msg 中取指定字段）。
func BuildScalar(msg map[string]any, m MsgMapping) model.ScalarPayload {
	p := model.ScalarPayload{Label: m.Label, Unit: m.Unit, RawPath: m.ScalarField}
	if v, ok := msg[m.ScalarField]; ok {
		if f, ok := model.ParseFloat64(v); ok {
			p.Value = f
		}
	} else if m.ScalarField == "" {
		if f, ok := model.ParseFloat64(msg["data"]); ok {
			p.Value = f
		}
	}
	// 量程：BatteryState 的 design_capacity 不可得时省略
	if min, ok := model.ParseFloat64(msg["min"]); ok {
		p.Min = min
	}
	if max, ok := model.ParseFloat64(msg["max"]); ok {
		p.Max = max
	}
	return p
}

// BuildTimeSeries 构造 time_series_sample payload：展平全部数值字段（R1）。
func BuildTimeSeries(msg map[string]any, t int64) model.TimeSeriesPayload {
	fields, _ := model.FlattenNumericFields(msg, "")
	return model.TimeSeriesPayload{T: t, Fields: fields}
}

// BuildGeoPose 构造 geo_pose payload。
// 🚩 合规硬红线：坐标值原样透传，crs 由数据源配置盖章（默认 WGS-84），绝不转换。
func BuildGeoPose(msg map[string]any, crs model.CRS, t int64, label string) model.GeoPosePayload {
	p := model.GeoPosePayload{CRS: crs, T: t, Label: label}
	if v, ok := model.ParseFloat64(msg["latitude"]); ok {
		p.Lat = v
	}
	if v, ok := model.ParseFloat64(msg["longitude"]); ok {
		p.Lon = v
	}
	if v, ok := model.ParseFloat64(msg["altitude"]); ok {
		p.Alt = v
	}
	if pos, ok := msg["position_covariance"].([]any); ok && len(pos) > 0 {
		if v, ok := model.ParseFloat64(pos[0]); ok {
			p.Accuracy = v
		}
	}
	if status, ok := msg["status"].(map[string]any); ok {
		if v, ok := model.ParseFloat64(status["accuracy"]); ok {
			p.Accuracy = v
		}
	}
	return p
}

// BuildImage 构造 image payload。
// MVP 支持 CompressedImage（jpeg/png）与由 rosbridge 压缩（compression=jpeg）后的 raw Image。
func BuildImage(msg map[string]any, stampMs int64) model.ImagePayload {
	p := model.ImagePayload{Encoding: model.ImageEncodingBase64, StampMs: stampMs, Mime: "image/jpeg"}
	if format, ok := msg["format"].(string); ok && format != "" {
		p.Mime = mimeFromFormat(format)
	}
	if w, ok := model.ParseFloat64(msg["width"]); ok {
		p.Width = int(w)
	}
	if h, ok := model.ParseFloat64(msg["height"]); ok {
		p.Height = int(h)
	}
	if data, ok := msg["data"].(string); ok {
		p.Data = data
	} else if arr, ok := msg["data"].([]any); ok {
		// rosbridge 有时把 base64 拆成字节数组（小图），按 int 转字节还原
		buf := make([]byte, 0, len(arr))
		for _, item := range arr {
			if n, ok := model.ParseFloat64(item); ok {
				buf = append(buf, byte(int(n)&0xFF))
			}
		}
		p.Data = string(buf)
		p.Encoding = "base64"
	}
	return p
}

// mimeFromFormat 把 CompressedImage.format 映射为 MIME。
func mimeFromFormat(format string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	switch {
	case strings.Contains(f, "png"):
		return "image/png"
	case strings.Contains(f, "bmp"):
		return "image/bmp"
	case strings.Contains(f, "webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// BuildJSON 构造兜底 json payload 并生成 schemaHint（R5）。
func BuildJSON(msg map[string]any) (model.JSONPayload, map[string]model.FieldHint) {
	hints := model.BuildSchemaHint(msg, "")
	return model.JSONPayload{Root: msg}, hints
}

// SortedKeys 返回 msg 的一级键（排序后，保证输出稳定）。
func SortedKeys(msg map[string]any) []string {
	keys := make([]string, 0, len(msg))
	for k := range msg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
