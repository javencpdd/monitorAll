package model

import "time"

// NowMs 返回当前毫秒时间戳（全项目时间字段统一 int64 ms）。
func timeNowMs() int64 { return time.Now().UnixMilli() }

// PayloadType 为归一化帧的载荷类型。
type PayloadType string

// 全部 payloadType（P0 七种 + P1/P2 扩展点）。
const (
	PayloadVideoStream PayloadType = "video_stream"
	PayloadImage       PayloadType = "image"
	PayloadScalar      PayloadType = "scalar"
	PayloadTimeSeries  PayloadType = "time_series_sample"
	PayloadGeoPose     PayloadType = "geo_pose"
	PayloadTable       PayloadType = "table"
	PayloadJSON        PayloadType = "json"
	PayloadLogLine     PayloadType = "log_line"      // [EXT] P1
	PayloadStateEnum   PayloadType = "state_enum"    // [EXT] P1
	PayloadHistogram   PayloadType = "histogram_bins" // [EXT] P1
	PayloadPointCloud  PayloadType = "point_cloud"    // [EXT] P2
)

// Status 为数据源 / 通道的运行状态。
type Status string

// 七种状态。
const (
	StatusIdle         Status = "idle"         // 新建未连接 / 无订阅
	StatusConnecting   Status = "connecting"
	StatusOnline       Status = "online"
	StatusReconnecting Status = "reconnecting"
	StatusError        Status = "error"
	StatusDegraded     Status = "degraded"     // 仅视频：WebRTC 不可用已降级
	StatusOffline      Status = "offline"
)

// DataSourceKind 为数据源大类。
type DataSourceKind string

// 三类数据源。
const (
	KindVideo DataSourceKind = "video"
	KindROS   DataSourceKind = "ros"
	KindHTTP  DataSourceKind = "http"
)

// Protocol 为具体接入协议。
type Protocol string

// 全部协议（rtsp 为扩展点）。
const (
	ProtoRTMP     Protocol = "rtmp"
	ProtoRTSP     Protocol = "rtsp"      // [EXT]
	ProtoROS1     Protocol = "ros1"
	ProtoROS2     Protocol = "ros2"
	ProtoHTTPPoll Protocol = "http-poll"
)

// CRS 为坐标系标识（后端只盖章，不做转换）。
type CRS string

// 三种坐标系。
const (
	CRSWGS84 CRS = "WGS-84"
	CRSGCJ02 CRS = "GCJ-02"
	CRSBD09  CRS = "BD-09"
)

// DefaultCRS 为未显式配置时的默认坐标系（ROS NavSatFix / GPS 原始值）。
const DefaultCRS = CRSWGS84

// ParseCRS 把字符串解析为 CRS，非法值回落默认坐标系。
func ParseCRS(v string) CRS {
	switch CRS(v) {
	case CRSWGS84, CRSGCJ02, CRSBD09:
		return CRS(v)
	default:
		return DefaultCRS
	}
}

// IsValidPayloadType 判断 payloadType 是否为受支持的类型。
func IsValidPayloadType(pt PayloadType) bool {
	switch pt {
	case PayloadVideoStream, PayloadImage, PayloadScalar, PayloadTimeSeries,
		PayloadGeoPose, PayloadTable, PayloadJSON,
		PayloadLogLine, PayloadStateEnum, PayloadHistogram, PayloadPointCloud:
		return true
	default:
		return false
	}
}
