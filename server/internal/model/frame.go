package model

import (
	"encoding/json"
	"sort"
	"strings"
)

// ————————————————— 统一 Frame 契约（架构 §5，全局最关键） —————————————————

// FieldHint 描述 payload 中某个字段的类型与单位，驱动前端字段选择器。
type FieldHint struct {
	Type string `json:"type"`           // number | string | boolean | object | array
	Unit string `json:"unit,omitempty"`
	Path string `json:"path"`           // JSONPath，如 "twist.linear.x"
}

// SchemaHint 为 path → FieldHint 的映射。
type SchemaHint map[string]FieldHint

// Frame 是所有数据源归一化后的统一帧结构。任何适配器不得增删外壳字段。
type Frame struct {
	ChannelID   string               `json:"channelId"`
	Seq         uint64               `json:"seq"`         // 通道内单调递增，用于丢帧检测
	PublishedTs int64                `json:"publishedTs"` // 数据产生时刻（ms）；无法获取时 = ingestedTs
	IngestedTs  int64                `json:"ingestedTs"`  // 后端接收时刻（ms）
	PayloadType PayloadType          `json:"payloadType"`
	Payload     json.RawMessage      `json:"payload"`
	SchemaHint  map[string]FieldHint `json:"schemaHint,omitempty"` // path → hint，驱动字段映射 UI
	SizeBytes   int                  `json:"sizeBytes"`
}

// FrameExt 为内存态扩展（不序列化到 WS）。
type FrameExt struct {
	Frame
	DroppedSinceLast uint64 `json:"-"`
}

// NewFrame 构造一帧：序列化 payload、填 ingestedTs 与 sizeBytes。
// publishedTs <= 0 时自动取 ingestedTs（架构 §5.4-R6）。
func NewFrame(channelID string, pt PayloadType, payload any, publishedTs int64) (Frame, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Frame{}, err
	}
	ingested := NowMs()
	if publishedTs <= 0 {
		publishedTs = ingested
	}
	return Frame{
		ChannelID:   channelID,
		PayloadType: pt,
		Payload:     raw,
		PublishedTs: publishedTs,
		IngestedTs:  ingested,
		SizeBytes:   len(raw),
	}, nil
}

// Decode 把 payload 反序列化到 v。
func (f *Frame) Decode(v any) error {
	if len(f.Payload) == 0 {
		return nil
	}
	return json.Unmarshal(f.Payload, v)
}

// ————————————————— 归一化辅助函数 —————————————————

// FlattenNumericFields 把嵌套 map 扁平化为「点分路径 → 数值」。
// 非数值字段（string/bool/嵌套对象）不进 fields，但会在 hints 中声明类型（R1）。
func FlattenNumericFields(root map[string]any, prefix string) (map[string]float64, map[string]FieldHint) {
	fields := make(map[string]float64)
	hints := make(map[string]FieldHint)
	flattenValue(root, prefix, fields, hints)
	return fields, hints
}

// flattenValue 递归展平一个值。
func flattenValue(v any, path string, fields map[string]float64, hints map[string]FieldHint) {
	switch val := v.(type) {
	case map[string]any:
		if path != "" {
			hints[path] = FieldHint{Type: "object", Path: path}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			flattenValue(val[k], child, fields, hints)
		}
	case []any:
		if path != "" {
			hints[path] = FieldHint{Type: "array", Path: path}
		}
	case bool:
		if path != "" {
			hints[path] = FieldHint{Type: "boolean", Path: path}
		}
	case string:
		if path != "" {
			hints[path] = FieldHint{Type: "string", Path: path}
		}
	case nil:
		if path != "" {
			hints[path] = FieldHint{Type: "string", Path: path}
		}
	default:
		if f, ok := ParseFloat64(v); ok {
			if path != "" {
				fields[path] = f
				hints[path] = FieldHint{Type: "number", Path: path}
			}
		} else if path != "" {
			hints[path] = FieldHint{Type: "string", Path: path}
		}
	}
}

// BuildSchemaHint 对任意 JSON 值生成一级（数组取首元素）字段提示。
func BuildSchemaHint(root any, prefix string) map[string]FieldHint {
	hints := make(map[string]FieldHint)
	buildHint(root, prefix, hints, 0)
	return hints
}

// buildHint 递归生成 hint，depth 控制只展开前两层以避免爆炸。
func buildHint(v any, path string, hints map[string]FieldHint, depth int) {
	const maxDepth = 3
	if depth > maxDepth {
		return
	}
	switch val := v.(type) {
	case map[string]any:
		if path != "" {
			hints[path] = FieldHint{Type: "object", Path: path}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			buildHint(val[k], child, hints, depth+1)
		}
	case []any:
		if path != "" {
			hints[path] = FieldHint{Type: "array", Path: path}
		}
		if len(val) > 0 {
			buildHint(val[0], path+"[]", hints, depth+1)
		}
	case bool:
		if path != "" {
			hints[path] = FieldHint{Type: "boolean", Path: path}
		}
	case string:
		if path != "" {
			hints[path] = FieldHint{Type: "string", Path: path}
		}
	case nil:
		if path != "" {
			hints[path] = FieldHint{Type: "string", Path: path}
		}
	default:
		if path != "" {
			hints[path] = FieldHint{Type: "number", Path: path}
		}
	}
}

// InferPayloadType 依据架构 §5.4-R4 从 HTTP 响应提取结果推断 payloadType。
// 数组 → table；全数值对象 → time_series_sample；单个数值 → scalar；其余 → json。
func InferPayloadType(v any) PayloadType {
	switch val := v.(type) {
	case []any:
		return PayloadTable
	case map[string]any:
		if len(val) == 0 {
			return PayloadJSON
		}
		allNumber := true
		for _, item := range val {
			if _, ok := ParseFloat64(item); !ok {
				allNumber = false
				break
			}
		}
		if allNumber {
			return PayloadTimeSeries
		}
		return PayloadJSON
	case nil:
		return PayloadJSON
	default:
		if _, ok := ParseFloat64(val); ok {
			return PayloadScalar
		}
		return PayloadJSON
	}
}

// StampToMs 把 ROS header.stamp（秒+纳秒）转成毫秒；缺失返回 0。
// header 形如 {"stamp":{"sec":1758000000,"nanosec":123456789}}。
func StampToMs(header any) int64 {
	h, ok := header.(map[string]any)
	if !ok {
		return 0
	}
	stamp, ok := h["stamp"]
	if !ok {
		return 0
	}
	switch s := stamp.(type) {
	case map[string]any:
		sec, _ := ParseInt64(s["sec"])
		nsec, _ := ParseInt64(s["nanosec"])
		if sec == 0 && nsec == 0 {
			return 0
		}
		return sec*1000 + nsec/1000000
	case float64:
		// 部分实现直接给浮点秒
		return int64(s * 1000)
	default:
		if f, ok := ParseFloat64(stamp); ok {
			return int64(f * 1000)
		}
		return 0
	}
}

// JoinPath 拼接点分路径，空段自动跳过。
func JoinPath(parts ...string) string {
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		segs = append(segs, p)
	}
	return strings.Join(segs, ".")
}
