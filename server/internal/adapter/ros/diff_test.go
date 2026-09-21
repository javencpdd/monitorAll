package ros

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monitorall/monitorall/internal/adapter"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 差异点 1：msgType 归一化 —————————————————

// TestNormalizeMsgType 校验 ROS2 形式 ↔ ROS1 形式的互转。
func TestNormalizeMsgType(t *testing.T) {
	cases := map[string]string{
		"nav_msgs/msg/Odometry": "nav_msgs/Odometry",
		"nav_msgs/Odometry":     "nav_msgs/Odometry",
		"/sensor_msgs/msg/Imu":  "sensor_msgs/Imu",
		"":                      "",
	}
	for in, want := range cases {
		if got := NormalizeMsgType(in); got != want {
			t.Fatalf("NormalizeMsgType(%q) = %q, want %q", in, got, want)
		}
	}
	// 版本还原
	if got := DenormalizeMsgType(string(model.ProtoROS2), "nav_msgs/Odometry"); got != "nav_msgs/msg/Odometry" {
		t.Fatalf("ROS2 还原错误: %s", got)
	}
	if got := DenormalizeMsgType(string(model.ProtoROS1), "nav_msgs/Odometry"); got != "nav_msgs/Odometry" {
		t.Fatalf("ROS1 应保持原样: %s", got)
	}
}

// TestVersionOf 校验版本归一化（含发行版代号）。
func TestVersionOf(t *testing.T) {
	for _, v := range []string{"ros2", "humble", "foxy", "jazzy"} {
		if VersionOf(v) != string(model.ProtoROS2) {
			t.Fatalf("%s 应判定为 ROS2", v)
		}
	}
	for _, v := range []string{"ros1", "noetic", "", "melodic"} {
		if VersionOf(v) != string(model.ProtoROS1) {
			t.Fatalf("%s 应判定为 ROS1", v)
		}
	}
}

// ————————————————— 差异点 2：subscribe 报文 —————————————————

// TestBuildSubscribeMessage 校验 ROS1 必填 type、ROS2 可选 type 与压缩参数。
func TestBuildSubscribeMessage(t *testing.T) {
	// ROS1：type 必填
	msg := BuildSubscribeMessage(string(model.ProtoROS1), "/odom", "nav_msgs/Odometry",
		SubscribeOptions{ThrottleRate: 50, QueueLength: 1})
	if msg["op"] != "subscribe" || msg["topic"] != "/odom" {
		t.Fatalf("报文基础字段错误: %+v", msg)
	}
	if msg["type"] != "nav_msgs/Odometry" {
		t.Fatalf("ROS1 type 错误: %v", msg["type"])
	}
	if msg["throttle_rate"] != 50 || msg["queue_length"] != 1 {
		t.Fatalf("节流参数错误: %+v", msg)
	}
	// ROS2：type 用 /msg/ 形式
	msg2 := BuildSubscribeMessage(string(model.ProtoROS2), "/odom", "nav_msgs/Odometry",
		SubscribeOptions{ThrottleRate: 100, QueueLength: 1})
	if msg2["type"] != "nav_msgs/msg/Odometry" {
		t.Fatalf("ROS2 type 错误: %v", msg2["type"])
	}
	// ROS2 未推断出类型时不填 type
	msg3 := BuildSubscribeMessage(string(model.ProtoROS2), "/odom", "", SubscribeOptions{})
	if _, ok := msg3["type"]; ok {
		t.Fatalf("ROS2 未知类型时不应填 type: %+v", msg3)
	}
}

// TestSubscribeOptionsFor 校验图像通道强制 jpeg 压缩与 10Hz 节流（D6）。
func TestSubscribeOptionsFor(t *testing.T) {
	opts := SubscribeOptionsFor(string(model.ProtoROS1), model.PayloadImage, 30)
	if opts.Compression != CompressionJPEG {
		t.Fatalf("图像通道应使用 jpeg 压缩: %s", opts.Compression)
	}
	if opts.ThrottleRate != imageThrottleRateMs {
		t.Fatalf("图像通道节流应为 100ms(10Hz): %d", opts.ThrottleRate)
	}
	if opts.QueueLength != DefaultQueueLength {
		t.Fatalf("队列长度应为 1: %d", opts.QueueLength)
	}
	scalar := SubscribeOptionsFor(string(model.ProtoROS2), model.PayloadScalar, 20)
	if scalar.Compression != CompressionNone {
		t.Fatalf("标量通道不应压缩: %s", scalar.Compression)
	}
	if scalar.ThrottleRate != 50 {
		t.Fatalf("20Hz 应对应 50ms 节流: %d", scalar.ThrottleRate)
	}
}

// ————————————————— 差异点 3/4：rosapi —————————————————

// TestBuildServiceCall 校验 ROS1 args 为数组、ROS2 args 为对象。
func TestBuildServiceCall(t *testing.T) {
	m1 := BuildServiceCall(string(model.ProtoROS1), ServiceTopics, []any{"/a"})
	if _, ok := m1["args"].([]any); !ok {
		t.Fatalf("ROS1 args 应为数组: %T", m1["args"])
	}
	m2 := BuildServiceCall(string(model.ProtoROS2), ServiceTopics, map[string]any{"k": "v"})
	if _, ok := m2["args"].(map[string]any); !ok {
		t.Fatalf("ROS2 args 应为对象: %T", m2["args"])
	}
}

// TestCandidateServiceNames 校验服务名候选顺序（ROS2 优先 topics_and_types）。
func TestCandidateServiceNames(t *testing.T) {
	if got := CandidateServiceNames(string(model.ProtoROS2)); got[0] != ServiceTopicsAndTypes {
		t.Fatalf("ROS2 应优先 topics_and_types: %+v", got)
	}
	if got := CandidateServiceNames(string(model.ProtoROS1)); got[0] != ServiceTopics {
		t.Fatalf("ROS1 应优先 topics: %+v", got)
	}
}

// TestExtractTopicsAndTypes 校验 rosapi 响应的两种结构都能解析。
func TestExtractTopicsAndTypes(t *testing.T) {
	v1 := map[string]any{"topics": []any{"/odom", "/imu"}, "types": []any{"nav_msgs/Odometry", "sensor_msgs/Imu"}}
	topics := ExtractTopics(v1)
	if len(topics) != 2 || topics[0] != "/odom" {
		t.Fatalf("topics 解析失败: %+v", topics)
	}
	types := ExtractTopicTypes(v1)
	if types["/odom"] != "nav_msgs/Odometry" {
		t.Fatalf("types 解析失败: %+v", types)
	}
	v2 := map[string]any{"topics_and_types": []any{
		map[string]any{"topic": "/odom", "type": "nav_msgs/msg/Odometry"},
	}}
	types2 := ExtractTopicTypes(v2)
	if types2["/odom"] != "nav_msgs/Odometry" {
		t.Fatalf("ROS2 结构解析失败（应归一化）: %+v", types2)
	}
}

// TestNoDataPolicy 校验 ROS2 静默失败的统一兜底常量。
func TestNoDataPolicy(t *testing.T) {
	if !ShouldSilentFail(string(model.ProtoROS2)) {
		t.Fatal("ROS2 应标记为静默失败")
	}
	if ShouldSilentFail(string(model.ProtoROS1)) {
		t.Fatal("ROS1 不应标记为静默失败")
	}
	if NoDataTimeoutMs != 3000 {
		t.Fatal("无数据超时应为 3000ms")
	}
}

// ————————————————— 映射表 —————————————————

// TestMappingTable 校验 msgType → payloadType 映射覆盖关键类型。
func TestMappingTable(t *testing.T) {
	cases := map[string]model.PayloadType{
		"std_msgs/Float32":          model.PayloadScalar,
		"std_msgs/Bool":             model.PayloadScalar,
		"sensor_msgs/BatteryState":  model.PayloadScalar,
		"sensor_msgs/Imu":           model.PayloadTimeSeries,
		"nav_msgs/Odometry":         model.PayloadTimeSeries,
		"sensor_msgs/NavSatFix":     model.PayloadGeoPose,
		"sensor_msgs/Image":         model.PayloadImage,
		"sensor_msgs/CompressedImage": model.PayloadImage,
		"unknown_msgs/Whatever":     model.PayloadJSON,
	}
	for in, want := range cases {
		if got := PayloadTypeFor(in); got != want {
			t.Fatalf("PayloadTypeFor(%s) = %s, want %s", in, got, want)
		}
	}
	// ROS2 写法应同样命中
	if PayloadTypeFor("nav_msgs/msg/Odometry") != model.PayloadTimeSeries {
		t.Fatal("ROS2 写法应命中同一映射")
	}
	if RecommendedRenderer("sensor_msgs/NavSatFix") != "map" {
		t.Fatal("NavSatFix 推荐渲染器应为 map")
	}
}

// TestBuildGeoPoseKeepsCRS 校验 geo_pose 保留原始坐标并带 crs（合规红线 D7）。
func TestBuildGeoPoseKeepsCRS(t *testing.T) {
	msg := map[string]any{"latitude": 39.9087, "longitude": 116.3975, "altitude": 50.0}
	p := BuildGeoPose(msg, model.CRSWGS84, 1758000000000, "/fix")
	if p.Lat != 39.9087 || p.Lon != 116.3975 {
		t.Fatalf("坐标被篡改: %+v", p)
	}
	if p.CRS != model.CRSWGS84 {
		t.Fatalf("crs 标注错误: %s", p.CRS)
	}
	// GCJ-02 声明时同样不转换（转换在前端 gcoord 做）
	p2 := BuildGeoPose(msg, model.CRSGCJ02, 1, "/fix")
	if p2.Lat != 39.9087 || p2.CRS != model.CRSGCJ02 {
		t.Fatalf("GCJ-02 场景坐标被篡改: %+v", p2)
	}
}

// TestBuildScalarAndImage 校验标量/图像 payload 构造。
func TestBuildScalarAndImage(t *testing.T) {
	m, _ := Lookup("sensor_msgs/BatteryState")
	p := BuildScalar(map[string]any{"voltage": 24.6}, m)
	if p.Value != 24.6 || p.Unit != "V" || p.Label != "voltage" {
		t.Fatalf("BatteryState 归一化错误: %+v", p)
	}
	img := BuildImage(map[string]any{"format": "jpeg", "data": "BASE64DATA", "width": 640, "height": 480}, 12345)
	if img.Mime != "image/jpeg" || img.Data != "BASE64DATA" || img.Width != 640 || img.Encoding != "base64" {
		t.Fatalf("图像归一化错误: %+v", img)
	}
	if img.StampMs != 12345 {
		t.Fatalf("图像时间戳错误: %d", img.StampMs)
	}
}

// TestBuildTimeSeries 校验数值展平。
func TestBuildTimeSeries(t *testing.T) {
	msg := map[string]any{
		"twist":   map[string]any{"twist": map[string]any{"linear": map[string]any{"x": 0.5}}},
		"child_id": "base",
	}
	ts := BuildTimeSeries(msg, 1758000000000)
	if ts.Fields["twist.twist.linear.x"] != 0.5 {
		t.Fatalf("展平结果错误: %+v", ts.Fields)
	}
	if _, ok := ts.Fields["child_id"]; ok {
		t.Fatal("字符串字段不应进入 fields")
	}
}

// ————————————————— 客户端（mock WebSocket） —————————————————

// fakeConn 实现 WSConn，用于不依赖真实 rosbridge 的客户端测试。
type fakeConn struct {
	mu       sync.Mutex
	write    []map[string]any
	inbox    chan []byte
	closed   bool
	closeOne sync.Once
}

func (f *fakeConn) ReadMessage() (int, []byte, error) {
	msg, ok := <-f.inbox
	if !ok {
		return 0, nil, errClosed()
	}
	return 1, msg, nil
}

func (f *fakeConn) WriteJSON(v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	f.write = append(f.write, m)
	return nil
}

func (f *fakeConn) Close() error {
	f.closeOne.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.inbox) // 关闭读通道，模拟真实连接断开（避免读循环永久阻塞）
	})
	return nil
}

func (f *fakeConn) written() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.write))
	copy(out, f.write)
	return out
}

// errClosed 表示 mock 连接已关闭（等价于 io.EOF）。
func errClosed() error { return errors.New("mock 连接已关闭") }

// TestClientSubscribeAndDispatch 校验订阅报文下发与 publish 消息分发（mock 连接）。
func TestClientSubscribeAndDispatch(t *testing.T) {
	conn := &fakeConn{inbox: make(chan []byte, 4)}
	SetDialer(func(ctx context.Context, url string) (WSConn, error) { return conn, nil })
	defer SetDialer(defaultDial)

	c := NewClient("ws://172.31.68.227:9090", string(model.ProtoROS2), nil)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	if !c.IsConnected() {
		t.Fatal("应处于已连接状态")
	}
	if err := c.Subscribe("/odom", SubscribeOptions{Type: "nav_msgs/Odometry", ThrottleRate: 50, QueueLength: 1}); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	written := conn.written()
	if len(written) != 1 || written[0]["op"] != "subscribe" {
		t.Fatalf("订阅报文错误: %+v", written)
	}
	if written[0]["type"] != "nav_msgs/msg/Odometry" {
		t.Fatalf("ROS2 订阅 type 错误: %v", written[0]["type"])
	}

	// 下发一条 publish，应分发给注册的处理器
	got := make(chan map[string]any, 1)
	c.OnMessage("/odom", func(msg map[string]any) {
		select {
		case got <- msg:
		default:
		}
	})
	payload, _ := json.Marshal(map[string]any{
		"op":    "publish",
		"topic": "/odom",
		"msg":   map[string]any{"pose": map[string]any{"pose": map[string]any{"position": map[string]any{"x": 1.0}}}},
	})
	conn.inbox <- payload
	select {
	case m := <-got:
		if m["pose"] == nil {
			t.Fatalf("消息分发内容错误: %+v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("消息未分发给处理器")
	}

	if err := c.Unsubscribe("/odom"); err != nil {
		t.Fatalf("退订失败: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if c.IsConnected() {
		t.Fatal("关闭后不应处于已连接状态")
	}
}

// TestClientCallService 校验 rosapi 调用与响应配对（软失败路径可测）。
func TestClientCallService(t *testing.T) {
	conn := &fakeConn{inbox: make(chan []byte, 4)}
	SetDialer(func(ctx context.Context, url string) (WSConn, error) { return conn, nil })
	defer SetDialer(defaultDial)

	c := NewClient("ws://host:9090", string(model.ProtoROS1), nil)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer func() { _ = c.Close() }()

	go func() {
		// 模拟 rosbridge 回一条 service_response
		time.Sleep(50 * time.Millisecond)
		resp, _ := json.Marshal(map[string]any{
			"op":      "service_response",
			"service": ServiceTopics,
			"values":  map[string]any{"topics": []any{"/odom"}},
		})
		conn.inbox <- resp
	}()

	values, err := c.CallService(context.Background(), ServiceTopics, []any{})
	if err != nil {
		t.Fatalf("服务调用失败: %v", err)
	}
	if topics := ExtractTopics(values); len(topics) != 1 || topics[0] != "/odom" {
		t.Fatalf("响应解析失败: %+v", values)
	}
	// 超时路径
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.CallService(ctx, "/rosapi/unknown", []any{}); err == nil {
		t.Fatal("无响应时应超时返回错误")
	}
}

// TestAdapterListChannelsSoftFail 校验未建连时 ListChannels 仍能给出手工 topic（不置错误态）。
func TestAdapterListChannelsSoftFail(t *testing.T) {
	a, err := New(&model.DataSource{
		ID:       "ds_test",
		Kind:     model.KindROS,
		Protocol: model.ProtoROS1,
		ConnParams: map[string]any{
			"version":   string(model.ProtoROS1),
			"bridgeUrl": "ws://172.31.68.227:9090",
			"topics":    []any{"/odom", "/battery"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("构造适配器失败: %v", err)
	}
	specs, err := a.ListChannels(context.Background())
	if err != nil {
		t.Fatalf("ListChannels 不应返回错误: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("应返回 2 个手工 topic: %+v", specs)
	}
	// 未建连时 Health 应为 false（但不 panic）
	if h := a.Health(context.Background()); h.OK {
		t.Fatal("未建连时 Health 应为 false")
	}
	// StartChannel 在未建连时应报错（由 Manager 进入退避重连）
	if err := a.StartChannel(context.Background(), "ch_1", adapter.ChannelSpec{Name: "/odom"}, func(string, model.Frame) {}); err == nil {
		t.Fatal("未建连时 StartChannel 应返回错误")
	}
}
