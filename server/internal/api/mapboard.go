package api

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— v3 MapBoard REST 层 —————————————————
//
// 边界（保住"内核不复制"这条架构约束）：
//   - 卡片 CRUD 【不新增端点】，复用 /dashboards/:id/cards 与 /cards/:id。
//     新增渲染器时后端零改动，前端只改 manifests（F-18 的落点）。
//   - 本文件只负责 MapBoard 独有的三件事：聚合读、节点 CRUD、几何轻量写。
//   - 不触碰 ws / bus / adapter：节点订阅由前端按连接发起，后端不做持久化订阅。

// MapBoardWithNodes 为地图看板的聚合返回体（看板 + 卡片 + 节点）。
type MapBoardWithNodes struct {
	model.Dashboard
	Cards []model.Card        `json:"cards"`
	Nodes []model.MapNodeView `json:"nodes"`
}

// MapBoardSummary 为地图看板列表项（带节点数，列表页不做 N+1 查询）。
type MapBoardSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	NodeCount int    `json:"nodeCount"`
	Revision  int64  `json:"revision"`
	UpdatedAt int64  `json:"updatedAt"`
}

// MapNodeReq 为节点创建/更新的请求体。
type MapNodeReq struct {
	Revision int64 `json:"revision"`
	// ID 可选：客户端提供即幂等（重试返回"已存在"时前端可视为成功）。
	ID string `json:"id,omitempty"`
	// ChannelID 为空表示纯静态节点（此时必须有兜底坐标）。
	ChannelID string `json:"channelId,omitempty"`
	Name      string `json:"name"`
	// FallbackLat / FallbackLon 为兜底坐标；nil 表示【无兜底】（不能用 0 做哨兵）。
	FallbackLat *float64 `json:"fallbackLat,omitempty"`
	FallbackLon *float64 `json:"fallbackLon,omitempty"`
	FallbackCRS string   `json:"fallbackCrs,omitempty"`
	LatPath     string   `json:"latPath,omitempty"`
	LonPath     string   `json:"lonPath,omitempty"`
	YawPath     string   `json:"yawPath,omitempty"`
	YawUnit     string   `json:"yawUnit,omitempty"`
	Static      bool     `json:"static,omitempty"`
	// PayloadType【不接受客户端输入】：它由服务端从绑定的通道读取，
	// 否则客户端可以伪造一个不在白名单里的类型绕过校验。
}

// GeometryItem 为单张卡片的像素几何。
type GeometryItem struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

// UpdateMapGeometryReq 为轻量几何保存的请求体（不携带 revision）。
type UpdateMapGeometryReq struct {
	Items []GeometryItem `json:"items"`
}

// mapBoardOrFail 读取看板并断言它是地图看板；不满足时已渲染错误并返回 nil。
func (d *Deps) mapBoardOrFail(c *gin.Context) *model.Dashboard {
	board, err := d.Store.GetDashboard(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return nil
	}
	if board.Kind != model.BoardKindMap {
		Fail(c, apperr.New(apperr.UnsupportedType, "该接口仅适用于地图看板（kind=map）"))
		return nil
	}
	return board
}

// validateMapCardBinding 校验地图看板下的卡片绑定关系。
//
// 禁止 map 渲染器：MapBoard 本身已经是一张全局底图，卡片里再放一个地图渲染器
// 会创建第二个 AMap 实例（WebGL 上下文与 Key 配额双杀），且"图里套图"在交互上无法解释。
func (d *Deps) validateMapCardBinding(board *model.Dashboard, rendererType string, nodeID *string) error {
	if board.Kind != model.BoardKindMap {
		return nil
	}
	if rendererType == "map" {
		return apperr.New(apperr.UnsupportedType, "地图看板内不支持绑定地图渲染器卡片").
			WithDetail(map[string]any{"field": "rendererType", "reason": "unsupported_in_mapboard"})
	}
	if nodeID == nil || *nodeID == "" {
		return apperr.New(apperr.InvalidParam, "地图看板的卡片必须指定 nodeId（未锚定卡片不会被渲染）").
			WithDetail(map[string]any{"field": "nodeId", "reason": "required_for_mapboard"})
	}
	node, err := d.Store.GetMapNode(*nodeID)
	if err != nil {
		return apperr.Wrap(err, apperr.InvalidParam, "卡片引用了不存在的节点")
	}
	if node.BoardID != board.ID {
		// 跨板引用必须拒绝：软引用没有外键，不校验就会产出"卡片在别的看板里借尸还魂"的脏数据。
		return apperr.New(apperr.InvalidParam, "节点不属于该看板").
			WithDetail(map[string]any{"field": "nodeId", "reason": "node_other_board"})
	}
	return nil
}

// ————————————————— 看板级 —————————————————

// listMapBoards 列出全部地图看板（含节点数）。
func (d *Deps) listMapBoards(c *gin.Context) {
	all, err := d.Store.ListDashboards()
	if err != nil {
		Fail(c, err)
		return
	}
	out := make([]MapBoardSummary, 0, len(all))
	for _, b := range all {
		if b.Kind != model.BoardKindMap {
			continue
		}
		n, err := d.Store.CountMapNodes(b.ID)
		if err != nil {
			Fail(c, err)
			return
		}
		out = append(out, MapBoardSummary{
			ID: b.ID, Name: b.Name, Kind: string(b.Kind),
			NodeCount: n, Revision: b.Revision, UpdatedAt: b.UpdatedAt,
		})
	}
	OK(c, out)
}

// createMapBoard 新建空的地图看板。
func (d *Deps) createMapBoard(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	if req.Name == "" {
		req.Name = "地图看板"
	}
	board := model.NewDashboard(req.Name)
	board.Kind = model.BoardKindMap
	if err := d.Store.CreateDashboard(board); err != nil {
		Fail(c, err)
		return
	}
	Created(c, MapBoardWithNodes{Dashboard: *board, Cards: []model.Card{}, Nodes: []model.MapNodeView{}})
}

// getMapBoard 返回看板 + 卡片 + 节点视图。
//
// 容错原则：通道缺失【不能】让整个看板打不开。节点照常返回，channelMissing=true 交给前端置灰，
// 用户能立刻看到"哪个节点需要重新绑定"。因为一个通道没了就返回 500 属于故障放大。
func (d *Deps) getMapBoard(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	cards, err := d.Store.ListCards(board.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	if cards == nil {
		cards = []model.Card{}
	}
	nodes, err := d.Store.ListMapNodes(board.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	// 一次拉全量通道做内存关联，避免 N+1（20 节点 × 每节点一次查询）。
	channels, err := d.Store.ListAllChannels()
	if err != nil {
		Fail(c, err)
		return
	}
	chByID := make(map[string]*model.Channel, len(channels))
	for i := range channels {
		chByID[channels[i].ID] = &channels[i]
	}
	views := make([]model.MapNodeView, 0, len(nodes))
	for _, n := range nodes {
		v := model.MapNodeView{
			MapNode:                n,
			RecommendedRateLimitHz: model.RecommendedNodeRateLimitHz,
			HasFallback:            n.FallbackLat != nil && n.FallbackLon != nil,
		}
		if n.ChannelID != "" {
			if ch, ok := chByID[n.ChannelID]; ok {
				v.ChannelName = ch.Name
				v.RateLimitHz = ch.RateLimitHz
			} else {
				// 脏引用：只报告，不修数据。自动修复属于破坏性操作，必须人工确认。
				v.ChannelMissing = true
				d.Log.Warn("地图节点引用了不存在的通道（脏引用，已降级为置灰）",
					"boardId", board.ID, "nodeId", n.ID, "channelId", n.ChannelID)
			}
		}
		views = append(views, v)
	}
	OK(c, MapBoardWithNodes{Dashboard: *board, Cards: cards, Nodes: views})
}

// updateMapBoard 全量保存地图看板。
//
// 刻意复用 updateDashboard 的全部逻辑（revision 校验、卡片继承、单事务、订阅同步），
// 只多一道 kind 守卫 —— 复制一份实现就会引入第二套保存语义，那正是本项目最容易漂移的地方。
func (d *Deps) updateMapBoard(c *gin.Context) {
	if d.mapBoardOrFail(c) == nil {
		return
	}
	d.updateDashboard(c)
}

// deleteMapBoard 删除地图看板（卡片与节点由 store 在同一事务内级联清理）。
func (d *Deps) deleteMapBoard(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	cards, err := d.Store.ListCards(board.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	for i := range cards {
		if cardSubscribes(board.Kind, &cards[i]) {
			d.unsubscribeCard(&cards[i])
		}
	}
	if err := d.Store.DeleteDashboard(board.ID); err != nil {
		Fail(c, err)
		return
	}
	OK(c, map[string]any{"deleted": true})
}

// updateMapGeometry 批量保存卡片像素几何。
//
// 与看板结构保存的三个关键差异（C7）：
//  1. 不校验、不递增 revision —— 几何是"视图偏好"，最坏结果是重拖一次；
//     若纳入 revision 域，一次拖拽会产生数十次自增，制造 40009 冲突风暴。
//  2. 不更新 dashboards.updated_at —— 否则列表页会被拖拽刷屏。
//  3. 永不返回 40009 —— 前端不需要为"拖一下就弹冲突"写分支。
func (d *Deps) updateMapGeometry(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	var req UpdateMapGeometryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	if len(req.Items) == 0 {
		Fail(c, apperr.New(apperr.InvalidParam, "items 不能为空"))
		return
	}
	if len(req.Items) > model.MaxGeometryItems {
		Fail(c, apperr.Newf(apperr.InvalidParam, "单次最多保存 %d 条几何", model.MaxGeometryItems))
		return
	}
	items := make(map[string]*model.MapGeometry, len(req.Items))
	for _, it := range req.Items {
		if it.ID == "" {
			Fail(c, apperr.New(apperr.InvalidParam, "几何项缺少 id"))
			return
		}
		g := &model.MapGeometry{V: model.MapGeometryVersion, X: it.X, Y: it.Y, W: it.W, H: it.H}
		if err := model.ValidateGeometry(g); err != nil {
			Fail(c, err)
			return
		}
		items[it.ID] = g
	}
	res, err := d.Store.SaveCardGeometry(board.ID, items)
	if err != nil {
		Fail(c, err)
		return
	}
	if res.Skipped > 0 {
		// 不属于本看板的卡片 id：不报错（可能是旧数据残留），但要留痕。
		d.Log.Warn("几何保存跳过了不属于本看板的卡片", "boardId", board.ID, "skipped", res.Skipped)
	}
	OK(c, res)
}

// ————————————————— 节点 CRUD —————————————————

// listMapNodes 列出看板下的全部节点。
func (d *Deps) listMapNodes(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	nodes, err := d.Store.ListMapNodes(board.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, nodes)
}

// buildMapNode 创建路径：从请求组装一个**全新**节点。
// 未提供的字段按零值处理 —— 这是"创建"该有的语义。
func (d *Deps) buildMapNode(c *gin.Context, boardID string, req *MapNodeReq) (*model.MapNode, bool) {
	n := &model.MapNode{
		ID:          req.ID,
		BoardID:     boardID,
		ChannelID:   req.ChannelID,
		Name:        req.Name,
		FallbackLat: req.FallbackLat,
		FallbackLon: req.FallbackLon,
		FallbackCRS: model.CRS(req.FallbackCRS),
		LatPath:     req.LatPath,
		LonPath:     req.LonPath,
		YawPath:     req.YawPath,
		YawUnit:     req.YawUnit,
		Static:      req.Static,
	}
	return d.validateAndEnrichNode(c, boardID, n)
}

// mergeMapNodePatch 把请求体当作**补丁**叠加到既有节点上。
//
// ⚠️ 这是"没提 ≠ 要清空"的落地。更新接口的请求体只带要改的几个字段，
// 若照创建路径全量组装，没提到的字段会被清零（channelId / name / 兜底坐标 / 字段路径）。
// 后果分两种，都很糟：
//
//	① 校验恰好拦住（如"未绑通道又无兜底坐标"）→ 用户对着莫名的 40001 猜；
//	② 校验恰好通过 → **字段被静默清空**，接口还返回 200。
//
// 本项目已经反复出现这一类缺陷（`PUT /dashboards` 的 cards 也是），所以这里显式按补丁处理：
// 空串 / nil 一律视为"未提供"，沿用旧值。
func mergeMapNodePatch(old *model.MapNode, req *MapNodeReq) *model.MapNode {
	n := *old
	if req.Name != "" {
		n.Name = req.Name
	}
	if req.ChannelID != "" {
		n.ChannelID = req.ChannelID
	}
	if req.FallbackCRS != "" {
		n.FallbackCRS = model.CRS(req.FallbackCRS)
	}
	if req.FallbackLat != nil {
		n.FallbackLat = req.FallbackLat
	}
	if req.FallbackLon != nil {
		n.FallbackLon = req.FallbackLon
	}
	if req.LatPath != "" {
		n.LatPath = req.LatPath
	}
	if req.LonPath != "" {
		n.LonPath = req.LonPath
	}
	if req.YawPath != "" {
		n.YawPath = req.YawPath
	}
	if req.YawUnit != "" {
		n.YawUnit = req.YawUnit
	}
	return &n
}

// validateAndEnrichNode 校验并补全节点（创建与更新共用同一条校验链）。
func (d *Deps) validateAndEnrichNode(c *gin.Context, boardID string, n *model.MapNode) (*model.MapNode, bool) {
	n.BoardID = boardID
	if n.FallbackCRS == "" {
		n.FallbackCRS = model.DefaultCRS
	}
	if err := model.ValidateMapNode(n); err != nil {
		Fail(c, err)
		return nil, false
	}
	if n.ChannelID == "" {
		// 纯静态节点：必须有兜底坐标，否则节点在地图上无处可放。
		if n.FallbackLat == nil {
			FailDetail(c, apperr.InvalidParam, "未绑定位置通道的节点必须提供兜底坐标",
				map[string]any{"field": "fallbackLat", "reason": "required_without_channel"})
			return nil, false
		}
		n.Static = true
		return n, true
	}
	ch, err := d.Store.GetChannel(n.ChannelID)
	if err != nil {
		Fail(c, err)
		return nil, false
	}
	if err := model.ValidateNodeChannel(ch.PayloadType, n.LatPath, n.LonPath); err != nil {
		Fail(c, err)
		return nil, false
	}
	n.PayloadType = string(ch.PayloadType)
	// 限频兜底（H8 档 A）：仅当未配置（<=0）时自动写回建议值，【不覆盖用户显式设置】。
	// 自动改用户配置是隐式行为，只敢在"它本来就是未设置"时做，并在日志里留痕。
	if ch.RateLimitHz <= 0 {
		ch.RateLimitHz = model.RecommendedNodeRateLimitHz
		if err := d.Store.UpdateChannel(ch); err != nil {
			d.Log.Warn("节点通道限频自动兜底失败", "channelId", ch.ID, "err", err)
		} else {
			d.Log.Info("节点通道未配置限频，已自动设为建议值",
				"channelId", ch.ID, "rateLimitHz", model.RecommendedNodeRateLimitHz)
		}
	} else if ch.RateLimitHz > model.RecommendedNodeRateLimitHz {
		d.Log.Warn("节点位置通道限频高于建议值，可能打满 WebSocket",
			"channelId", ch.ID, "rateLimitHz", ch.RateLimitHz,
			"recommended", model.RecommendedNodeRateLimitHz)
	}
	return n, true
}

// createMapNode 新建节点（revision 严格校验，同一事务自增）。
func (d *Deps) createMapNode(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	var req MapNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	n, ok := d.buildMapNode(c, board.ID, &req)
	if !ok {
		return
	}
	res, err := d.Store.CreateMapNode(board.ID, n, req.Revision)
	if err != nil {
		Fail(c, err)
		return
	}
	d.Log.Info("创建地图节点", "boardId", board.ID, "nodeId", res.Node.ID,
		"channelId", res.Node.ChannelID, "payloadType", res.Node.PayloadType,
		"static", res.Node.Static, "hasFallback", res.Node.FallbackLat != nil,
		"revision", res.Revision)
	Created(c, res)
}

// updateMapNode 全量更新节点（revision 严格校验）。
func (d *Deps) updateMapNode(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	nodeID := c.Param("nodeId")

	var req MapNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}

	// 先读旧值：更新是**补丁**语义，不是全量覆盖（见 mergeMapNodePatch）。
	old, err := d.Store.GetMapNode(nodeID)
	if err != nil {
		Fail(c, err)
		return
	}
	if old.BoardID != board.ID {
		Fail(c, apperr.New(apperr.NotFound, "节点不属于该看板"))
		return
	}

	merged := mergeMapNodePatch(old, &req)
	merged.ID = nodeID
	n, ok := d.validateAndEnrichNode(c, board.ID, merged)
	if !ok {
		return
	}
	res, err := d.Store.UpdateMapNode(board.ID, n, req.Revision)
	if err != nil {
		Fail(c, err)
		return
	}
	d.Log.Info("更新地图节点", "boardId", board.ID, "nodeId", nodeID,
		"channelId", res.Node.ChannelID, "revision", res.Revision)
	OK(c, res)
}

// deleteMapNode 删除节点。默认解绑保留卡片（C10），cascade=delete 时一并删除。
//
// 解绑后必须释放这些卡片的订阅（C15）—— 否则它们变成"看不见却仍在拉数据"的白订阅，
// 在 20 节点规模下会累积成一堆白白占用适配器连接的僵尸引用。
func (d *Deps) deleteMapNode(c *gin.Context) {
	board := d.mapBoardOrFail(c)
	if board == nil {
		return
	}
	nodeID := c.Param("nodeId")
	var query struct {
		Revision int64  `form:"revision"`
		Cascade  string `form:"cascade"`
	}
	if err := c.ShouldBindQuery(&query); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "查询参数解析失败"))
		return
	}
	switch query.Cascade {
	case "", "unbind", "delete":
	default:
		Fail(c, apperr.Newf(apperr.InvalidParam, "非法的 cascade 取值 %q（应为 unbind 或 delete）", query.Cascade))
		return
	}
	cascade := query.Cascade == "delete"

	// 删除前先取卡片列表：解绑后 cards.node_id 已被清空，事后就查不出该释放哪些订阅了。
	bound := make([]model.Card, 0)
	if !cascade {
		all, err := d.Store.ListCards(board.ID)
		if err != nil {
			Fail(c, err)
			return
		}
		for _, card := range all {
			if card.NodeID != nil && *card.NodeID == nodeID {
				bound = append(bound, card)
			}
		}
	}

	res, err := d.Store.DeleteMapNode(board.ID, nodeID, cascade, query.Revision)
	if err != nil {
		Fail(c, err)
		return
	}
	for i := range bound {
		d.unsubscribeCard(&bound[i])
	}
	d.Log.Info("删除地图节点", "boardId", board.ID, "nodeId", nodeID,
		"mode", fmt.Sprintf("%t", cascade), "unbound", res.Unbound, "deleted", res.Deleted,
		"revision", res.Revision)
	OK(c, res)
}

// selfCheckMapNodes 报告 cards.node_id 的脏引用（只报告，不自动修）。
func (d *Deps) selfCheckMapNodes() {
	refs, err := d.Store.FindDirtyNodeRefs()
	if err != nil {
		d.Log.Warn("节点引用自检失败", "err", err)
		return
	}
	if len(refs) == 0 {
		return
	}
	for _, r := range refs {
		d.Log.Warn("发现卡片-节点脏引用（卡片将按未锚定处理，不渲染也不订阅）",
			"cardId", r.CardID, "boardId", r.BoardID, "nodeId", r.NodeID, "problem", r.Problem)
	}
}
