package model

import (
	"math"
	"strings"

	"github.com/monitorall/monitorall/internal/apperr"
)

// ————————————————— v3 MapBoard（全局地图看板）领域模型 —————————————————

// BoardKind 区分看板种类。v3 之前只存在网格看板，迁移后全部旧看板自动为 grid。
type BoardKind string

const (
	// BoardKindGrid 为网格看板（12 列栅格，既有形态）。
	BoardKindGrid BoardKind = "grid"
	// BoardKindMap 为地图看板（v3 新增，地图作底图 + 节点锚定卡片）。
	BoardKindMap BoardKind = "map"
)

// ParseBoardKind 解析看板种类，非法值返回 false（调用方应显式报错，不要静默回落）。
func ParseBoardKind(v string) (BoardKind, bool) {
	switch BoardKind(v) {
	case BoardKindGrid, BoardKindMap:
		return BoardKind(v), true
	default:
		return "", false
	}
}

// ————————————————— 卡片在地图看板中的像素几何 —————————————————

// MapGeometry 为卡片在 MapBoard 下的像素几何（v3 契约 v1）。
//
// 语义要点（错一个都会产生"不报错但位置不对"的静默偏差）：
//   - x/y 是【相对节点投影锚点】的 CSS px 偏移，不是容器绝对坐标、不是经纬度、不是栅格单位；可为负。
//   - w/h 是【整卡外框】的 CSS px，不是内容区。视频卡 16:9 施加于内容区，由渲染器 manifest 派生，
//     不写进这里（避免第二个真源）。
//   - V 为契约版本，读 {} 视为未初始化。未来 v2 采用"读 v1/v2、始终写 v2"的双读单写。
type MapGeometry struct {
	V int `json:"v"`
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// MapGeometryVersion 为当前 map_geom 契约版本。
const MapGeometryVersion = 1

// AnchorMode 为卡片锚定方式。
type AnchorMode string

// 只有一种锚定方式：跟随节点（x/y 解释为相对节点投影点的偏移）。
//
// 曾经存在 AnchorFree="free"（节点被删后由服务端写入）表示"已脱离节点"，已移除：
//  1. 双真源 —— "未锚定"已由 node_id = ” 完整表达，再引入 anchor_mode='free'
//     就是同一事实两种表示，此后每个读方都要回答"两者冲突时以谁为准"；
//  2. 粘滞 bug —— 卡片解绑被写成 'free' 后，重新挂载节点时前端通常只发 nodeId，
//     锚定模式会永久继承 'free'，形成 node_id='n_x' + anchor_mode='free' 的自相矛盾状态；
//  3. 零消费者 —— 订阅判定只看 node_id，没有任何代码读 anchor_mode。
//
// 不变式：node_id = ” ⟺ 未锚定。anchor_mode 恒为 'node'。
const AnchorNode AnchorMode = "node"

// ParseAnchorMode 解析锚定模式；客户端只允许显式指定 node。
func ParseAnchorMode(v string) (AnchorMode, bool) {
	switch AnchorMode(v) {
	case AnchorNode:
		return AnchorNode, true
	default:
		return "", false
	}
}

// ————————————————— 地图节点 —————————————————

// MapNode 为地图看板上的一个节点：位置锚点 + 非 GPS 卡片的宿主。
//
// 实时位置【不落库】：位置来自所绑定通道的帧（内存 + WS），本表只持久化兜底坐标与绑定关系。
type MapNode struct {
	ID          string   `json:"id"`
	BoardID     string   `json:"boardId"`
	ChannelID   string   `json:"channelId"`   // 空 = 纯静态节点（或通道已被删除）
	Name        string   `json:"name"`        // 展示名，必填
	FallbackLat *float64 `json:"fallbackLat"` // nil = 无兜底（不能用 0 做哨兵，(0,0) 是合法坐标）
	FallbackLon *float64 `json:"fallbackLon"` // nil = 无兜底
	FallbackCRS CRS      `json:"fallbackCrs"` // 默认 WGS-84；前端是唯一转换点
	PayloadType string   `json:"payloadType"` // 绑定通道的 payloadType 快照（冗余，避免每次联表）
	LatPath     string   `json:"latPath"`     // json/table 通道取纬度的字段路径
	LonPath     string   `json:"lonPath"`     // json/table 通道取经度的字段路径
	YawPath     string   `json:"yawPath"`     // 可选：朝向
	YawUnit     string   `json:"yawUnit"`     // deg | rad，默认 deg
	Static      bool     `json:"static"`      // true = 纯静态节点（无位置通道或通道已删）
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
}

// MapNodeView 为 map_nodes 加上通道运行期信息的视图（GET /mapboards/:id 返回体）。
//
// ChannelMissing 是故障降级的关键：通道被删除时节点不能让整个看板打不开，
// 而是置为 true + 前端置灰提示重新绑定（不能因为一个通道没了就打不开看板 —— 那是故障放大）。
type MapNodeView struct {
	MapNode
	ChannelMissing         bool    `json:"channelMissing"`
	ChannelName            string  `json:"channelName,omitempty"`
	RateLimitHz            float64 `json:"rateLimitHz"`
	RecommendedRateLimitHz float64 `json:"recommendedRateLimitHz"`
	HasFallback            bool    `json:"hasFallback"`
}

// ————————————————— 规模与限频默认值 —————————————————

const (
	// MaxNodesPerBoard 为单个地图看板的节点数上限（D0-4：≤20 节点，不做聚类/LOD）。
	MaxNodesPerBoard = 20
	// RecommendedNodeRateLimitHz 为节点位置通道的建议限频（H8：不限频会打满 WS）。
	RecommendedNodeRateLimitHz = 10.0
	// MaxNodeNameLen 为节点名最大长度。
	MaxNodeNameLen = 64
	// MaxGeometryItems 为单次几何保存的最大条目数（防批量滥用）。
	MaxGeometryItems = 200
	// MinGeometryPx / MaxGeometryPx 为卡片尺寸的合法区间（px，外框）。
	MinGeometryPx = 80
	MaxGeometryPx = 2000
	// MaxGeometryOffsetPx 为偏移量的绝对值上限（防止 NaN/越界把布局写坏）。
	MaxGeometryOffsetPx = 100000
)

// PositionPayloadTypes 为允许作为节点位置源的通道类型。
//
// 【当前只放行 geo_pose】json / table 暂不放行，原因是**坐标系无源**：
//   - geo_pose 的帧由适配器逐帧盖章 payload.crs（ros 适配器已实现），坐标语义有唯一权威来源；
//   - json / table 通道既不产 payload.crs，也没有 channel.meta.crs 的写入方，
//     放行后坐标只能默认按 WGS-84 解释 —— 若设备实际是 GCJ-02，点位整体偏几百米**且不报错**。
//     这正是本项目反复出现的「猜不到就取默认值继续走」缺陷形态（第 2 号）；
//     对一个回答"机器人现在在哪"的系统来说，**显示错位置比不显示更糟**。
//
// 重新放行的前置条件：json / table 适配器补写 channel.meta.crs（或帧级 crs）。
// 届时把它们加回本表即可，前端那组 latPath / lonPath UI 无需重写（已保留）。
var PositionPayloadTypes = map[PayloadType]bool{
	PayloadGeoPose: true,
}

// NeedsPathFor 返回该 payloadType 是否必须提供 latPath/lonPath。
// 仅在 PositionPayloadTypes 放行非 geo_pose 类型时才可能为 true。
func NeedsPathFor(pt PayloadType) bool { return pt != "" && pt != PayloadGeoPose }

// ————————————————— 校验 —————————————————

// ValidateMapNode 校验节点字段，返回带 detail 的 InvalidParam / UnsupportedType 错误。
// 坐标系非法必须显式报错：ParseCRS 那种静默回落会让"配错了 CRS"表现为"位置偏了几百米且不报错"。
func ValidateMapNode(n *MapNode) error {
	if strings.TrimSpace(n.Name) == "" {
		return apperr.New(apperr.InvalidParam, "节点名不能为空").
			WithDetail(map[string]any{"field": "name", "reason": "required"})
	}
	if len([]rune(n.Name)) > MaxNodeNameLen {
		return apperr.Newf(apperr.InvalidParam, "节点名不能超过 %d 个字符", MaxNodeNameLen).
			WithDetail(map[string]any{"field": "name", "reason": "too_long", "max": MaxNodeNameLen})
	}
	if n.FallbackCRS != "" && n.FallbackCRS != CRSWGS84 && n.FallbackCRS != CRSGCJ02 && n.FallbackCRS != CRSBD09 {
		return apperr.Newf(apperr.InvalidParam, "非法坐标系 %q", n.FallbackCRS).
			WithDetail(map[string]any{
				"field": "fallbackCrs", "reason": "unsupported",
				"allowed": []string{string(CRSWGS84), string(CRSGCJ02), string(CRSBD09)},
			})
	}
	// 纬度与经度必须成对出现（DB 层还有一条 CHECK 兜底，这里是给前端可读的错误）。
	if (n.FallbackLat == nil) != (n.FallbackLon == nil) {
		return apperr.New(apperr.InvalidParam, "兜底纬度与经度必须同时提供或同时留空").
			WithDetail(map[string]any{"field": "fallbackLat", "reason": "must_pair_with_lon"})
	}
	if n.FallbackLat != nil {
		if !isFinite(*n.FallbackLat) || *n.FallbackLat < -90 || *n.FallbackLat > 90 {
			return apperr.New(apperr.InvalidParam, "兜底纬度超出 [-90, 90]").
				WithDetail(map[string]any{"field": "fallbackLat", "reason": "out_of_range", "min": -90, "max": 90})
		}
		if !isFinite(*n.FallbackLon) || *n.FallbackLon < -180 || *n.FallbackLon > 180 {
			return apperr.New(apperr.InvalidParam, "兜底经度超出 [-180, 180]").
				WithDetail(map[string]any{"field": "fallbackLon", "reason": "out_of_range", "min": -180, "max": 180})
		}
	}
	if n.YawUnit != "" && n.YawUnit != "deg" && n.YawUnit != "rad" {
		return apperr.Newf(apperr.InvalidParam, "非法朝向单位 %q（应为 deg 或 rad）", n.YawUnit).
			WithDetail(map[string]any{"field": "yawUnit", "reason": "unsupported", "allowed": []string{"deg", "rad"}})
	}
	return nil
}

// ValidateNodeChannel 校验节点绑定的位置通道是否可用作位置源。
// payloadType 不在白名单 → UnsupportedType（40003）；需要字段路径却没给 → InvalidParam。
func ValidateNodeChannel(pt PayloadType, latPath, lonPath string) error {
	if !PositionPayloadTypes[pt] {
		return apperr.Newf(apperr.UnsupportedType,
			"节点位置源当前仅支持 %s 类型通道；%s / %s 通道的位置可视化暂未启用（当前通道为 %q）",
			PayloadGeoPose, PayloadJSON, PayloadTable, pt)
	}
	if NeedsPathFor(pt) && (strings.TrimSpace(latPath) == "" || strings.TrimSpace(lonPath) == "") {
		return apperr.Newf(apperr.InvalidParam,
			"%s 类型的位置通道必须提供 latPath 与 lonPath", pt).
			WithDetail(map[string]any{"field": "latPath", "reason": "required_for_payload_type", "payloadType": pt})
	}
	return nil
}

// ValidateGeometry 校验一条卡片像素几何。w/h 必须落在合法区间，偏移不能是 NaN/Inf。
func ValidateGeometry(g *MapGeometry) error {
	if g == nil {
		return apperr.New(apperr.InvalidParam, "几何不能为空")
	}
	if !isFinite(float64(g.X)) || !isFinite(float64(g.Y)) {
		return apperr.New(apperr.InvalidParam, "偏移量必须是有限数（不接受 NaN/Infinity）")
	}
	if g.X > MaxGeometryOffsetPx || g.X < -MaxGeometryOffsetPx ||
		g.Y > MaxGeometryOffsetPx || g.Y < -MaxGeometryOffsetPx {
		return apperr.Newf(apperr.InvalidParam, "偏移量绝对值不能超过 %d px", MaxGeometryOffsetPx)
	}
	if g.W < MinGeometryPx || g.W > MaxGeometryPx || g.H < MinGeometryPx || g.H > MaxGeometryPx {
		return apperr.Newf(apperr.InvalidParam, "卡片尺寸应在 [%d, %d] px 之间", MinGeometryPx, MaxGeometryPx).
			WithDetail(map[string]any{"field": "w/h", "reason": "out_of_range",
				"min": MinGeometryPx, "max": MaxGeometryPx})
	}
	return nil
}

// isFinite 判断浮点数是否有限（NaN / ±Inf 会让后续布局计算静默崩坏）。
func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
