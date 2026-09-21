package ros

import (
	"context"
	"errors"
	"strings"

	"github.com/monitorall/monitorall/internal/model"
)

// 🚩 本文件是 ROS1 / ROS2 差异的唯一落点。
// 全仓除本文件外，禁止出现 `if version == "ros1"` / `"ros2"` 之类的版本分支。
// 差异点清单（架构 §8.4）：
//  1. msgType 字符串：nav_msgs/msg/X（ROS2） ↔ nav_msgs/X（ROS1）
//  2. subscribe 的 type 字段：ROS1 必填，ROS2 可选（推断不出时省略）
//  3. rosapi 服务参数：ROS1 args 为数组，ROS2 args 为对象
//  4. rosapi 服务名不一致：/rosapi/topics、/rosapi/topic_type、/rosapi/topics_and_types
//  5. ROS2 订阅无数据是静默的（无 status error），需统一用 3s 超时标记 reconnecting

// ————————————————— 版本判定 —————————————————

// isROS2 判断是否为 ROS2（唯一允许做版本比较的地方）。
func isROS2(version string) bool {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case string(model.ProtoROS2), "dashing", "foxy", "galactic", "humble", "iron", "jazzy":
		return true
	default:
		return false
	}
}

// VersionOf 从 protocol / version 字段归一化出 "ros1" / "ros2"。
func VersionOf(version string) string {
	if isROS2(version) {
		return string(model.ProtoROS2)
	}
	return string(model.ProtoROS1)
}

// ————————————————— 差异点 1：msgType 归一化 —————————————————

// NormalizeMsgType 去掉 ROS2 形式的 "/msg/" 段，统一为 ROS1 形式做映射表 key。
// 例：nav_msgs/msg/Odometry → nav_msgs/Odometry。
func NormalizeMsgType(t string) string {
	t = strings.TrimSpace(strings.TrimPrefix(t, "/"))
	if t == "" {
		return ""
	}
	return strings.Replace(t, "/msg/", "/", 1)
}

// DenormalizeMsgType 按版本还原为 rosbridge 期望的 msgType 字符串。
// ROS2 需要 nav_msgs/msg/Odometry；ROS1 需要 nav_msgs/Odometry。
func DenormalizeMsgType(version string, t string) string {
	t = NormalizeMsgType(t)
	if t == "" || !isROS2(version) {
		return t
	}
	parts := strings.Split(t, "/")
	if len(parts) != 2 {
		return t
	}
	return parts[0] + "/msg/" + parts[1]
}

// ————————————————— 差异点 2：subscribe 报文 —————————————————

// BuildSubscribeMessage 构造 rosbridge 订阅报文。
// ROS1：type 必填（缺失会返回 status error），因此推断不出时用默认 std_msgs/String 兜底不填会让订阅失败 → 直接报错由上层处理。
// ROS2：type 可选；提供时若与实际不完全匹配会导致订阅静默失败，因此仅在确定时填写。
func BuildSubscribeMessage(version string, topic, msgType string, opts SubscribeOptions) map[string]any {
	msg := map[string]any{
		"op":   OpSubscribe,
		"topic": topic,
	}
	if msgType != "" {
		msg["type"] = DenormalizeMsgType(version, msgType)
	} else if !isROS2(version) {
		// ROS1 必填：交给上层保证传入，这里保持不填由 rosbridge 报错并触发重连判定
		msg["type"] = ""
	}
	if opts.ThrottleRate > 0 {
		msg["throttle_rate"] = opts.ThrottleRate
	}
	if opts.QueueLength > 0 {
		msg["queue_length"] = opts.QueueLength
	}
	if opts.FragmentSize > 0 {
		msg["fragment_size"] = opts.FragmentSize
	}
	if opts.Compression != "" && opts.Compression != "none" {
		msg["compression"] = opts.Compression
	}
	return msg
}

// SubscribeOptionsFor 按 payloadType 与限流 Hz 生成订阅附加参数（D6）。
// 图像通道强制 compression=jpeg，从源头减少 JSON 序列化量。
func SubscribeOptionsFor(version string, pt model.PayloadType, maxHz float64) SubscribeOptions {
	opts := SubscribeOptions{
		QueueLength: DefaultQueueLength,
		Compression: CompressionNone,
	}
	if maxHz > 0 {
		opts.ThrottleRate = int(1000 / maxHz)
		if opts.ThrottleRate < minThrottleRateMs {
			opts.ThrottleRate = minThrottleRateMs
		}
	}
	if pt == model.PayloadImage {
		opts.Compression = CompressionJPEG
		// 图像上限 10Hz：节流周期不得小于 imageThrottleRateMs（即取更慢的那档）。
		if opts.ThrottleRate <= 0 || opts.ThrottleRate < imageThrottleRateMs {
			opts.ThrottleRate = imageThrottleRateMs
		}
	}
	return opts
}

// 订阅附加参数常量（禁止散落魔法值）。
const (
	// CompressionNone 为不压缩。
	CompressionNone = "none"
	// CompressionJPEG 为 JPEG 压缩（图像通道使用）。
	CompressionJPEG = "jpeg"
	// DefaultQueueLength 为默认队列长度（1 = 只保留最新，丢弃积压）。
	DefaultQueueLength = 1
	// minThrottleRateMs 为 throttle_rate 下限（过快没有意义且浪费带宽）。
	minThrottleRateMs = 20
	// imageThrottleRateMs 为图像通道默认节流周期（10Hz，与 imageMaxHz 对齐）。
	imageThrottleRateMs = 100
)

// ————————————————— 差异点 3/4：rosapi 服务调用 —————————————————

// rosapi 服务名常量。
const (
	// ServiceTopics 为 ROS1/ROS2 通用话题列表服务。
	ServiceTopics = "/rosapi/topics"
	// ServiceTopicType 为 ROS1 的话题类型查询服务。
	ServiceTopicType = "/rosapi/topic_type"
	// ServiceTopicsAndTypes 为 ROS2 发行版常用的话题+类型服务。
	ServiceTopicsAndTypes = "/rosapi/topics_and_types"
)

// BuildServiceCall 按版本构造 rosapi 调用报文：ROS1 args 为数组，ROS2 args 为对象。
func BuildServiceCall(version string, service string, args any) map[string]any {
	msg := map[string]any{"op": OpCallService, "service": service}
	if isROS2(version) {
		msg["args"] = toObject(args)
	} else {
		msg["args"] = toArray(args)
	}
	return msg
}

// toArray 把入参转成 ROS1 需要的数组形式。
func toArray(args any) []any {
	switch v := args.(type) {
	case nil:
		return []any{}
	case []any:
		return v
	case []string:
		out := make([]any, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out
	case map[string]any:
		out := make([]any, 0, len(v))
		for _, val := range v {
			out = append(out, val)
		}
		return out
	default:
		return []any{args}
	}
}

// toObject 把入参转成 ROS2 需要的对象形式。
func toObject(args any) map[string]any {
	switch v := args.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return v
	case []any:
		out := make(map[string]any, len(v))
		for i, item := range v {
			out["arg"+itoa(i)] = item
		}
		return out
	default:
		return map[string]any{"value": args}
	}
}

// itoa 为极简整数转字符串（避免 import strconv 造成语义噪音）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 4)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	return string(buf)
}

// CandidateServiceNames 返回按版本优先的话题发现服务名列表（全部尝试，全部失败则软降级）。
func CandidateServiceNames(version string) []string {
	if isROS2(version) {
		return []string{ServiceTopicsAndTypes, ServiceTopics}
	}
	return []string{ServiceTopics, ServiceTopicsAndTypes}
}

// TopicTypeServiceName 返回按版本优先的话题类型查询服务名。
func TopicTypeServiceName(version string) string {
	if isROS2(version) {
		return ServiceTopicsAndTypes
	}
	return ServiceTopicType
}

// ExtractTopics 从 rosapi 响应中提取话题列表（兼容 topics / topics_and_types 两种返回）。
// 返回 nil 表示无法解析（软失败由调用方处理）。
func ExtractTopics(values map[string]any) []string {
	if values == nil {
		return nil
	}
	if raw, ok := values["topics"]; ok {
		if arr, ok := raw.([]any); ok {
			out := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	if raw, ok := values["topics_and_types"]; ok {
		if arr, ok := raw.([]any); ok {
			out := make([]string, 0, len(arr))
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if s, ok := m["topic"].(string); ok {
						out = append(out, s)
					}
				}
			}
			return out
		}
	}
	return nil
}

// ExtractTopicTypes 从 rosapi 响应中提取 topic → msgType（已归一化为 ROS1 形式）。
func ExtractTopicTypes(values map[string]any) map[string]string {
	out := make(map[string]string)
	if values == nil {
		return out
	}
	if raw, ok := values["topics_and_types"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				topic, _ := m["topic"].(string)
				msgType, _ := m["type"].(string)
				if topic != "" {
					out[topic] = NormalizeMsgType(msgType)
				}
			}
			return out
		}
	}
	if typesRaw, ok := values["types"]; ok {
		topicsRaw, hasTopics := values["topics"]
		if !hasTopics {
			return out
		}
		topics, ok1 := topicsRaw.([]any)
		types, ok2 := typesRaw.([]any)
		if !ok1 || !ok2 {
			return out
		}
		for i, t := range topics {
			if i >= len(types) {
				break
			}
			topic, ok := t.(string)
			msgType, okType := types[i].(string)
			if ok && okType && topic != "" {
				out[topic] = NormalizeMsgType(msgType)
			}
		}
	}
	return out
}

// ————————————————— 差异点 5：订阅无数据的统一处理 —————————————————

// NoDataTimeoutMs 为订阅后判定「无数据」的统一超时（ROS2 静默失败需要它）。
const NoDataTimeoutMs = 3000

// NoDataErrorMessage 为无数据时写入 channel.lastError 的统一文案。
const NoDataErrorMessage = "no data within 3s"

// NoDataRetryDelayMs 为无数据后重试一次 Subscribe 的间隔。
const NoDataRetryDelayMs = 10000

// ShouldSilentFail 返回该版本在订阅失败时是否静默（无 status error）。
// ROS2 静默 → 需要靠 NoDataTimeoutMs 兜底判定。
func ShouldSilentFail(version string) bool {
	return isROS2(version)
}

// DiscoverTopics 用 rosapi 尽力发现话题（软失败：任何错误都返回 nil, err，由上层降级为手工 topic）。
func (c *Client) DiscoverTopics(ctx context.Context) ([]string, error) {
	names := CandidateServiceNames(c.version)
	for _, svc := range names {
		values, err := c.CallService(ctx, svc, map[string]any{})
		if err != nil {
			continue
		}
		if topics := ExtractTopics(values); len(topics) > 0 {
			return topics, nil
		}
	}
	return nil, errRosapiUnavailable
}

// DiscoverTopicTypes 用 rosapi 尽力发现话题类型（软失败）。
func (c *Client) DiscoverTopicTypes(ctx context.Context) (map[string]string, error) {
	names := CandidateServiceNames(c.version)
	for _, svc := range names {
		values, err := c.CallService(ctx, svc, map[string]any{})
		if err != nil {
			continue
		}
		if types := ExtractTopicTypes(values); len(types) > 0 {
			return types, nil
		}
	}
	return nil, errRosapiUnavailable
}

// errRosapiUnavailable 为 rosapi 不可用（软失败，不置数据源错误态）。
var errRosapiUnavailable = errors.New("rosapi 不可用，话题发现降级为手工填写")
