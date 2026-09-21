package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFrameSerialization 校验 Frame 的 JSON 字段与架构文档 §5.1 逐字一致。
func TestFrameSerialization(t *testing.T) {
	payload := ScalarPayload{Value: 24.63, Unit: "V", Min: 0, Max: 30, Label: "voltage"}
	f, err := NewFrame("ch_9a1b2c3d4e5f6g7h", PayloadScalar, payload, 1758000000090)
	if err != nil {
		t.Fatalf("构造帧失败: %v", err)
	}
	f.Seq = 10241
	f.IngestedTs = 1758000000096
	f.SchemaHint = map[string]FieldHint{
		"voltage": {Type: "number", Unit: "V", Path: "voltage"},
	}

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	wantKeys := []string{"channelId", "seq", "publishedTs", "ingestedTs", "payloadType", "payload", "schemaHint", "sizeBytes"}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Fatalf("缺少字段 %s，实际输出: %s", k, string(data))
		}
	}
	if got["channelId"] != "ch_9a1b2c3d4e5f6g7h" {
		t.Fatalf("channelId 不符: %v", got["channelId"])
	}
	if got["payloadType"] != "scalar" {
		t.Fatalf("payloadType 不符: %v", got["payloadType"])
	}
	if got["publishedTs"].(float64) != 1758000000090 {
		t.Fatalf("publishedTs 不符: %v", got["publishedTs"])
	}
	// 反序列化后字段应保持一致
	var back Frame
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("反序列化 Frame 失败: %v", err)
	}
	if back.Seq != 10241 || back.ChannelID != f.ChannelID || back.SizeBytes != len(f.Payload) {
		t.Fatalf("往返后字段不一致: %+v", back)
	}
	var p ScalarPayload
	if err := back.Decode(&p); err != nil {
		t.Fatalf("解码 payload 失败: %v", err)
	}
	if p.Value != 24.63 || p.Unit != "V" || p.Label != "voltage" {
		t.Fatalf("payload 解码不一致: %+v", p)
	}
}

// TestNewFrameDefaults 校验 publishedTs 缺省取 ingestedTs、sizeBytes = len(payload)（R6/R7）。
func TestNewFrameDefaults(t *testing.T) {
	f, err := NewFrame("ch_test", PayloadJSON, JSONPayload{Root: map[string]any{"a": 1}}, 0)
	if err != nil {
		t.Fatalf("构造帧失败: %v", err)
	}
	if f.PublishedTs != f.IngestedTs {
		t.Fatalf("publishedTs 应回落为 ingestedTs: %d != %d", f.PublishedTs, f.IngestedTs)
	}
	if f.SizeBytes != len(f.Payload) || f.SizeBytes == 0 {
		t.Fatalf("sizeBytes 不等于 payload 长度: %d", f.SizeBytes)
	}
}

// TestIDPrefix 校验 ID 前缀与长度规则（TASKS §1.2）。
func TestIDPrefix(t *testing.T) {
	cases := map[string]string{
		PrefixDataSource: NewDataSourceID(),
		PrefixChannel:    NewChannelID(),
		PrefixCard:       NewCardID(),
		PrefixDashboard:  NewDashboardID(),
	}
	for prefix, id := range cases {
		if !strings.HasPrefix(id, prefix) {
			t.Fatalf("%s 前缀错误: %s", prefix, id)
		}
		if len(id) != len(prefix)+IDRandomLen {
			t.Fatalf("%s 长度错误: %s (%d)", prefix, id, len(id))
		}
	}
	if a, b := NewChannelID(), NewChannelID(); a == b {
		t.Fatal("两次生成的 ID 不应相同")
	}
}

// TestFlattenNumericFields 校验数值展平与非数值字段进入 hint（R1）。
func TestFlattenNumericFields(t *testing.T) {
	root := map[string]any{
		"pose":  map[string]any{"x": 1.5, "y": 2.5},
		"twist": map[string]any{"linear": map[string]any{"x": 3.5}},
		"frame": "base_link",
		"ok":    true,
		"list":  []any{1, 2},
	}
	fields, hints := FlattenNumericFields(root, "")
	for _, path := range []string{"pose.x", "pose.y", "twist.linear.x"} {
		if _, ok := fields[path]; !ok {
			t.Fatalf("缺少数值字段 %s: %+v", path, fields)
		}
	}
	if _, ok := fields["frame"]; ok {
		t.Fatal("字符串字段不应进入 fields")
	}
	if _, ok := fields["ok"]; ok {
		t.Fatal("布尔字段不应进入 fields")
	}
	if hints["frame"].Type != "string" {
		t.Fatalf("frame 的 hint 类型错误: %+v", hints["frame"])
	}
	if hints["ok"].Type != "boolean" {
		t.Fatalf("ok 的 hint 类型错误: %+v", hints["ok"])
	}
	if hints["list"].Type != "array" {
		t.Fatalf("list 的 hint 类型错误: %+v", hints["list"])
	}
}

// TestInferPayloadType 校验 HTTP 响应推断规则（R4）。
func TestInferPayloadType(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want PayloadType
	}{
		{"数组→table", []any{map[string]any{"a": 1}}, PayloadTable},
		{"全数值对象→time_series", map[string]any{"a": 1.0, "b": 2.0}, PayloadTimeSeries},
		{"含字符串对象→json", map[string]any{"a": 1.0, "b": "x"}, PayloadJSON},
		{"单数值→scalar", 12.5, PayloadScalar},
		{"空对象→json", map[string]any{}, PayloadJSON},
	}
	for _, c := range cases {
		if got := InferPayloadType(c.in); got != c.want {
			t.Fatalf("%s 推断错误: got %s want %s", c.name, got, c.want)
		}
	}
}

// TestStampToMs 校验 ROS header.stamp（秒+纳秒）转毫秒。
func TestStampToMs(t *testing.T) {
	header := map[string]any{"stamp": map[string]any{"sec": 1758000000, "nanosec": 123000000}}
	if got := StampToMs(header); got != 1758000000123 {
		t.Fatalf("stamp 转换错误: %d", got)
	}
	if got := StampToMs(map[string]any{}); got != 0 {
		t.Fatalf("无 header 应为 0: %d", got)
	}
}

// TestParseCRS 校验坐标系解析与默认回落。
func TestParseCRS(t *testing.T) {
	if ParseCRS("GCJ-02") != CRSGCJ02 {
		t.Fatal("GCJ-02 解析错误")
	}
	if ParseCRS("unknown") != DefaultCRS {
		t.Fatal("未知坐标系应回落默认")
	}
	if DefaultCRS != CRSWGS84 {
		t.Fatal("默认坐标系应为 WGS-84（后端只盖章不转换）")
	}
}

// TestGeoPosePayloadCRS 校验 geo_pose payload 的 crs 字段必然出现在 JSON 中（合规）。
func TestGeoPosePayloadCRS(t *testing.T) {
	p := GeoPosePayload{Lat: 39.9, Lon: 116.4, CRS: CRSWGS84, T: 1}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(data), `"crs":"WGS-84"`) {
		t.Fatalf("geo_pose 缺少 crs 标注: %s", string(data))
	}
}
