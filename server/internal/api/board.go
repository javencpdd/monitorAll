package api

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 看板请求体 —————————————————

// CreateDashboardReq 为新建看板的请求体。
type CreateDashboardReq struct {
	Name     string          `json:"name" binding:"required"`
	GridCols int             `json:"gridCols,omitempty"`
	Kind     model.BoardKind `json:"kind,omitempty"` // 缺省 grid；map = 地图看板（v3）
}

// UpdateDashboardReq 为全量保存看板的请求体（含 revision 乐观锁）。
type UpdateDashboardReq struct {
	Name         string         `json:"name"`
	Revision     int64          `json:"revision"`
	GridCols     int            `json:"gridCols,omitempty"`
	RowHeight    int            `json:"rowHeight,omitempty"`
	Margin       [2]int         `json:"margin,omitempty"`
	GlobalConfig map[string]any `json:"globalConfig,omitempty"`
	// Cards 为**可选**的全量卡片列表。
	//
	// ⚠️ 必须是指针，不能用值类型：值类型无法区分「请求没提卡片」与「请求传了空数组」。
	// 前者（例如只改 globalConfig 的视角/地图样式保存）若按"空数组 = 全量替换"处理，
	// 会把该看板的卡片**全部删掉** —— 一个只读意图的调用触发写破坏，是典型的静默数据丢失。
	// nil = 不触碰卡片；非 nil = 全量替换（即使是空数组，代表用户确实删光了）。
	Cards *[]UpdateCardItem `json:"cards"`
}

// UpdateCardItem 为全量保存中的卡片项。
type UpdateCardItem struct {
	ID             string                `json:"id,omitempty"`
	ChannelID      string                `json:"channelId"`
	RendererType   string                `json:"rendererType"`
	Title          string                `json:"title"`
	Unit           string                `json:"unit,omitempty"`
	RenderConfig   map[string]any        `json:"renderConfig,omitempty"`
	DisplayOptions *model.DisplayOptions `json:"displayOptions,omitempty"`
	Layout         *model.Layout         `json:"layout,omitempty"`
	// v3：地图看板卡片的节点归属。缺省表示"不改动"，由服务端从旧卡片继承（H11）。
	// 注意：这里【不接受 mapGeom】—— 几何的唯一写路径是 PUT /mapboards/:id/geometry。
	NodeID     *string `json:"nodeId,omitempty"`
	AnchorMode *string `json:"anchorMode,omitempty"`
}

// UpdateLayoutReq 为轻量保存布局的请求体（拖拽后高频调用）。
type UpdateLayoutReq struct {
	Revision int64        `json:"revision"`
	Layouts  []LayoutItem `json:"layouts"`
}

// LayoutItem 为单卡片的布局项。
type LayoutItem struct {
	ID string `json:"id"`
	X  int    `json:"x"`
	Y  int    `json:"y"`
	W  int    `json:"w"`
	H  int    `json:"h"`
}

// CreateCardReq 为建卡请求体。
type CreateCardReq struct {
	ChannelID    string         `json:"channelId" binding:"required"`
	RendererType string         `json:"rendererType" binding:"required"`
	Title        string         `json:"title"`
	Unit         string         `json:"unit,omitempty"`
	RenderConfig map[string]any `json:"renderConfig"`
	Layout       model.Layout   `json:"layout"`
	// v3：地图看板下必须指定所属节点（未指定即为"未锚定"，不渲染也不订阅）。
	NodeID *string `json:"nodeId,omitempty"`
}

// UpdateCardReq 为更新卡片请求体。
type UpdateCardReq struct {
	ChannelID      string                `json:"channelId,omitempty"`
	RendererType   string                `json:"rendererType,omitempty"`
	Title          *string               `json:"title,omitempty"`
	Unit           *string               `json:"unit,omitempty"`
	RenderConfig   map[string]any        `json:"renderConfig,omitempty"`
	DisplayOptions *model.DisplayOptions `json:"displayOptions,omitempty"`
	Layout         *model.Layout         `json:"layout,omitempty"`
	// v3：节点归属（rebind 走这里）。不接受 mapGeom —— 几何的唯一写路径是 PUT /mapboards/:id/geometry。
	NodeID     *string `json:"nodeId,omitempty"`
	AnchorMode *string `json:"anchorMode,omitempty"`
}

// ————————————————— 看板 CRUD —————————————————

// cardSubscribes 判断一张卡片当前是否应当持有通道订阅（C15）。
//
// 网格看板：卡片一律持有。
// 地图看板：只有【已锚定】的卡片持有 —— 未锚定卡片不渲染，若还保留订阅就是"看不见的白订阅"，
// 在 20 节点规模下会累积成一堆白白消耗适配器连接的僵尸引用。
func cardSubscribes(kind model.BoardKind, card *model.Card) bool {
	if card == nil || card.ChannelID == "" {
		return false
	}
	if kind == model.BoardKindMap {
		return card.NodeID != nil && *card.NodeID != ""
	}
	return true
}

// listDashboards 列出全部看板（不含 cards）。
func (d *Deps) listDashboards(c *gin.Context) {
	list, err := d.Store.ListDashboards()
	if err != nil {
		Fail(c, err)
		return
	}
	OK(c, list)
}

// createDashboard 新建空看板。
func (d *Deps) createDashboard(c *gin.Context) {
	var req CreateDashboardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	db := model.NewDashboard(req.Name)
	if req.Kind != "" {
		k, ok := model.ParseBoardKind(string(req.Kind))
		if !ok {
			FailDetail(c, apperr.UnsupportedType, fmt.Sprintf("不支持的看板类型 %q", req.Kind),
				map[string]any{"field": "kind", "reason": "unsupported",
					"allowed": []string{string(model.BoardKindGrid), string(model.BoardKindMap)}})
			return
		}
		db.Kind = k
	}
	if req.GridCols > 0 {
		db.GridCols = req.GridCols
	}
	if err := d.Store.CreateDashboard(db); err != nil {
		Fail(c, err)
		return
	}
	Created(c, model.DashboardWithCards{Dashboard: *db, Cards: []model.Card{}})
}

// getDashboard 读取看板及其卡片。
func (d *Deps) getDashboard(c *gin.Context) {
	db, err := d.Store.GetDashboard(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	cards, err := d.Store.ListCards(db.ID)
	if err != nil {
		Fail(c, err)
		return
	}
	if cards == nil {
		cards = []model.Card{}
	}
	OK(c, model.DashboardWithCards{Dashboard: *db, Cards: cards})
}

// updateDashboard 全量保存看板（cards 一并替换）；revision 不匹配 → 40009（D3）。
func (d *Deps) updateDashboard(c *gin.Context) {
	id := c.Param("id")
	var req UpdateDashboardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	db, err := d.Store.GetDashboard(id)
	if err != nil {
		Fail(c, err)
		return
	}
	if req.Revision != db.Revision {
		Fail(c, apperr.New(apperr.RevisionConflict, "看板已被修改，请刷新后重试"))
		return
	}

	// cards 可选：nil = 请求未提卡片 → 只更新元数据，绝不触碰卡片（见 UpdateDashboardReq.Cards）。
	cardsProvided := req.Cards != nil
	var items []UpdateCardItem
	if cardsProvided {
		items = *req.Cards
	}
	newCards := make([]model.Card, 0, len(items))
	// 无论是否替换卡片都读一次：不替换时要拿它当回参，
	// 否则前端 `dashboard.current = updated` 会把本地卡片列表清空（保存视角后卡片消失）。
	oldCards, err := d.Store.ListCards(id)
	if err != nil {
		Fail(c, err)
		return
	}
	oldByID := make(map[string]model.Card, len(oldCards))
	for _, oc := range oldCards {
		oldByID[oc.ID] = oc
	}

	for _, item := range items {
		if item.ChannelID == "" {
			Fail(c, apperr.New(apperr.InvalidParam, "卡片缺少 channelId"))
			return
		}
		if _, err := d.Store.GetChannel(item.ChannelID); err != nil {
			Fail(c, apperr.Wrap(err, apperr.InvalidParam, "卡片引用了不存在的通道"))
			return
		}
		card := model.NewCard(id, item.ChannelID, item.RendererType, item.Title)
		if item.ID != "" {
			card.ID = item.ID
			if old, ok := oldByID[item.ID]; ok {
				card.CreatedAt = old.CreatedAt
				// H11：请求体没带回的字段一律从旧卡片继承。
				//
				// 不继承的下场：全量保存会把 layout / nodeId / anchorMode / mapGeom 重置成默认值，
				// 用户拖好的位置和节点绑定凭空消失 —— 而且接口返回 200，没有任何报错。
				// 这是本项目最难排查的一类缺陷：看起来像"前端没保存"，实际是后端清掉的。
				if item.Layout == nil {
					card.Layout = old.Layout
				}
				if item.NodeID == nil {
					card.NodeID = old.NodeID
				}
				if item.AnchorMode == nil {
					card.AnchorMode = old.AnchorMode
				}
				// 几何永远继承旧值：它的唯一写路径是 PUT /mapboards/:id/geometry，
				// 全量保存不参与（否则"拖拽结果被一次重命名覆盖"会成为常态）。
				card.MapGeom = old.MapGeom
			}
		}
		if item.Unit != "" {
			card.Unit = item.Unit
		}
		if item.RenderConfig != nil {
			card.RenderConfig = item.RenderConfig
		}
		if item.DisplayOptions != nil {
			card.DisplayOptions = *item.DisplayOptions
		}
		if item.Layout != nil {
			card.Layout = *item.Layout
		}
		// v3：节点归属。客户端只允许显式指定 node；free 是"节点被删除后解绑"的服务端内部状态。
		if item.NodeID != nil {
			card.NodeID = item.NodeID
		}
		if item.AnchorMode != nil {
			am, ok := model.ParseAnchorMode(*item.AnchorMode)
			if !ok {
				FailDetail(c, apperr.UnsupportedType,
					fmt.Sprintf("不支持的锚定方式 %q", *item.AnchorMode),
					map[string]any{"field": "anchorMode", "reason": "unsupported",
						"allowed": []string{string(model.AnchorNode)}})
				return
			}
			v := string(am)
			card.AnchorMode = &v
		}
		newCards = append(newCards, *card)
	}

	if req.Name != "" {
		db.Name = req.Name
	}
	if req.GridCols > 0 {
		db.GridCols = req.GridCols
	}
	if req.RowHeight > 0 {
		db.RowHeight = req.RowHeight
	}
	if req.Margin != [2]int{0, 0} {
		db.Margin = req.Margin
	}
	if req.GlobalConfig != nil {
		db.GlobalConfig = req.GlobalConfig
	}
	db.Revision++
	db.UpdatedAt = time.Now().UnixMilli()

	// H16：换卡片与 revision 自增必须在【同一个事务】内。
	//
	// 此前是 ReplaceCards + UpdateDashboard 两个独立事务：第二步失败时 revision 已经 +1
	// 而卡片没换，接口还返回 200。用户看到的是"保存成功了，但布局是旧的"。
	if cardsProvided {
		if err := d.Store.ReplaceCardsAndUpdateDashboard(db, newCards); err != nil {
			Fail(c, err)
			return
		}
		d.syncCardSubscriptions(db.Kind, oldCards, newCards)
	} else {
		// 未提卡片：只更新看板元数据。**不能**走全量替换 —— 那会把卡片删光。
		if err := d.Store.UpdateDashboard(db); err != nil {
			Fail(c, err)
			return
		}
		// 回参必须带上实际存在的卡片，保持"响应体总是完整的看板"这一契约。
		newCards = oldCards
	}
	OK(c, model.DashboardWithCards{Dashboard: *db, Cards: newCards})
}

// updateLayout 轻量保存布局（仅改 x/y/w/h），同样校验 revision。
func (d *Deps) updateLayout(c *gin.Context) {
	id := c.Param("id")
	var req UpdateLayoutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	db, err := d.Store.GetDashboard(id)
	if err != nil {
		Fail(c, err)
		return
	}
	if req.Revision != db.Revision {
		Fail(c, apperr.New(apperr.RevisionConflict, "看板已被修改，请刷新后重试"))
		return
	}
	cards, err := d.Store.ListCards(id)
	if err != nil {
		Fail(c, err)
		return
	}
	layouts := make(map[string]LayoutItem, len(req.Layouts))
	for _, l := range req.Layouts {
		layouts[l.ID] = l
	}
	for i := range cards {
		if l, ok := layouts[cards[i].ID]; ok {
			cards[i].Layout.X = l.X
			cards[i].Layout.Y = l.Y
			cards[i].Layout.W = l.W
			cards[i].Layout.H = l.H
			cards[i].UpdatedAt = time.Now().UnixMilli()
			if err := d.Store.UpdateCard(&cards[i]); err != nil {
				Fail(c, err)
				return
			}
		}
	}
	db.Revision++
	db.UpdatedAt = time.Now().UnixMilli()
	if err := d.Store.UpdateDashboard(db); err != nil {
		Fail(c, err)
		return
	}
	OK(c, *db)
}

// deleteDashboard 删除看板、其卡片与地图节点，并释放卡片持有的订阅。
func (d *Deps) deleteDashboard(c *gin.Context) {
	id := c.Param("id")
	board, err := d.Store.GetDashboard(id)
	if err != nil {
		Fail(c, err)
		return
	}
	cards, err := d.Store.ListCards(id)
	if err != nil {
		Fail(c, err)
		return
	}
	for i := range cards {
		// 未锚定的卡片不持有订阅，跳过（否则引用计数被减成负数）。
		if cardSubscribes(board.Kind, &cards[i]) {
			d.unsubscribeCard(&cards[i])
		}
	}
	if err := d.Store.DeleteDashboard(id); err != nil {
		Fail(c, err)
		return
	}
	OK(c, map[string]any{"deleted": true})
}

// ————————————————— 卡片 CRUD —————————————————

// listCards 列出看板下的卡片。
func (d *Deps) listCards(c *gin.Context) {
	cards, err := d.Store.ListCards(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	if cards == nil {
		cards = []model.Card{}
	}
	OK(c, cards)
}

// createCard 建卡并触发订阅引用计数 +1。
func (d *Deps) createCard(c *gin.Context) {
	dashboardID := c.Param("id")
	board, err := d.Store.GetDashboard(dashboardID)
	if err != nil {
		Fail(c, err)
		return
	}
	var req CreateCardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	if err := d.validateMapCardBinding(board, req.RendererType, req.NodeID); err != nil {
		Fail(c, err)
		return
	}
	ch, err := d.Store.GetChannel(req.ChannelID)
	if err != nil {
		Fail(c, err)
		return
	}
	card := model.NewCard(dashboardID, req.ChannelID, req.RendererType, req.Title)
	card.Unit = req.Unit
	if req.NodeID != nil && *req.NodeID != "" {
		card.NodeID = req.NodeID
		anchor := string(model.AnchorNode)
		card.AnchorMode = &anchor
	}
	if req.RenderConfig != nil {
		card.RenderConfig = req.RenderConfig
	}
	if req.Layout.W > 0 && req.Layout.H > 0 {
		card.Layout = req.Layout
		if card.Layout.MinW == 0 {
			card.Layout.MinW = model.DefaultMinCardW
		}
		if card.Layout.MinH == 0 {
			card.Layout.MinH = model.DefaultMinCardH
		}
	}
	if err := d.Store.CreateCard(card); err != nil {
		Fail(c, err)
		return
	}
	// 未锚定的地图看板卡片不订阅（C15）：它不会被渲染，订阅它就是白占适配器连接。
	if cardSubscribes(board.Kind, card) {
		d.subscribeCard(card, ch)
	}
	Created(c, card)
}

// updateCard 更新卡片；改通道或变更锚定状态时同步调整订阅引用计数。
func (d *Deps) updateCard(c *gin.Context) {
	card, err := d.Store.GetCard(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	board, err := d.Store.GetDashboard(card.DashboardID)
	if err != nil {
		Fail(c, err)
		return
	}
	var req UpdateCardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	oldChannelID := card.ChannelID
	wasEffective := cardSubscribes(board.Kind, card)

	var newCh *model.Channel
	if req.ChannelID != "" && req.ChannelID != oldChannelID {
		// H17：此前这里取到 newCh 后立刻 `_ = newCh` 丢弃，末尾又重新查一次库。
		// 留着它既能少一次查询，也避免"查到的通道"与"实际写入的通道"不是同一个。
		newCh, err = d.Store.GetChannel(req.ChannelID)
		if err != nil {
			Fail(c, err)
			return
		}
		card.ChannelID = req.ChannelID
	}
	if req.RendererType != "" {
		card.RendererType = req.RendererType
	}
	if req.Title != nil {
		card.Title = *req.Title
	}
	if req.Unit != nil {
		card.Unit = *req.Unit
	}
	if req.RenderConfig != nil {
		card.RenderConfig = req.RenderConfig
	}
	if req.DisplayOptions != nil {
		card.DisplayOptions = *req.DisplayOptions
	}
	if req.Layout != nil {
		card.Layout = *req.Layout
	}
	// v3：节点归属（rebind / 解绑走这里）。
	if req.NodeID != nil {
		card.NodeID = req.NodeID
	}
	if req.AnchorMode != nil {
		am, ok := model.ParseAnchorMode(*req.AnchorMode)
		if !ok {
			FailDetail(c, apperr.UnsupportedType, fmt.Sprintf("不支持的锚定方式 %q", *req.AnchorMode),
				map[string]any{"field": "anchorMode", "reason": "unsupported",
					"allowed": []string{string(model.AnchorNode)}})
			return
		}
		v := string(am)
		card.AnchorMode = &v
	}
	card.UpdatedAt = time.Now().UnixMilli()
	if err := d.Store.UpdateCard(card); err != nil {
		Fail(c, err)
		return
	}

	nowEffective := cardSubscribes(board.Kind, card)
	switch {
	case oldChannelID != card.ChannelID && wasEffective && nowEffective:
		// 换通道：先退订旧通道，再订阅新通道
		if oldCh, err := d.Store.GetChannel(oldChannelID); err == nil {
			d.unsubscribeCardWith(card, oldCh)
		}
		d.subscribeCard(card, newCh)
	case wasEffective && !nowEffective:
		// 变为未锚定（C15）：必须释放订阅，否则留下看不见的白订阅
		if ch, err := d.Store.GetChannel(card.ChannelID); err == nil {
			d.unsubscribeCardWith(card, ch)
		}
	case !wasEffective && nowEffective:
		// 重新锚定（rebind）：恢复订阅
		if ch, err := d.Store.GetChannel(card.ChannelID); err == nil {
			d.subscribeCard(card, ch)
		}
	}
	OK(c, card)
}

// deleteCard 删除卡片（引用计数 -1）。
func (d *Deps) deleteCard(c *gin.Context) {
	card, err := d.Store.GetCard(c.Param("id"))
	if err != nil {
		Fail(c, err)
		return
	}
	// 未锚定的地图看板卡片从未持有订阅，无条件退订会让引用计数变成负数（C15）。
	if board, bErr := d.Store.GetDashboard(card.DashboardID); bErr == nil && cardSubscribes(board.Kind, card) {
		d.unsubscribeCard(card)
	} else if bErr != nil {
		d.Log.Warn("删除卡片前读取看板失败，已跳过退订", "cardId", card.ID, "err", bErr)
	}
	if err := d.Store.DeleteCard(card.ID); err != nil {
		Fail(c, err)
		return
	}
	OK(c, map[string]any{"deleted": true})
}

// ————————————————— 订阅联动 —————————————————

// subscribeCard 让适配器订阅卡片引用的通道（引用计数 +1）。
func (d *Deps) subscribeCard(card *model.Card, ch *model.Channel) {
	if d.Adm == nil || card == nil || ch == nil {
		return
	}
	ds, err := d.Store.GetDataSource(ch.DataSourceID)
	if err != nil {
		d.Log.Warn("卡片订阅失败：数据源不存在", "channelId", ch.ID, "err", err)
		return
	}
	if d.Bus != nil {
		d.Bus.EnsureChannel(ch.ID, ch.PayloadType, ch.RateLimitHz)
	}
	if err := d.Adm.Subscribe(ds, ch); err != nil {
		d.Log.Warn("卡片订阅失败", "channelId", ch.ID, "err", err)
	}
}

// unsubscribeCard 释放卡片对通道的订阅（引用计数 -1）。
func (d *Deps) unsubscribeCard(card *model.Card) {
	if card == nil {
		return
	}
	ch, err := d.Store.GetChannel(card.ChannelID)
	if err != nil {
		return
	}
	d.unsubscribeCardWith(card, ch)
}

// unsubscribeCardWith 使用已加载的通道释放订阅。
func (d *Deps) unsubscribeCardWith(card *model.Card, ch *model.Channel) {
	if d.Adm == nil || card == nil || ch == nil {
		return
	}
	ds, err := d.Store.GetDataSource(ch.DataSourceID)
	if err != nil {
		return
	}
	if err := d.Adm.Unsubscribe(ds, ch); err != nil {
		d.Log.Warn("取消卡片订阅失败", "channelId", ch.ID, "err", err)
	}
}

// effectiveChannelCounts 统计"真正需要订阅"的卡片数（按通道多重集计数）。
//
// 历史缺陷（H12）：此前按 channelID 集合（set）去重，同通道多张卡只算 1 次。
// 于是"一节点绑 3 张卡、删掉 2 张"时引用计数不下降，通道被误判为仍有人用；
// 反过来，建卡 3 张只 subscribe 1 次、删 1 张就 unsubscribe，通道会【提前断流】。
// v3 的"一节点绑多卡"正是主场景，这个缺陷会被显著放大。
//
// 有效性规则：网格看板的卡片全部有效；地图看板只有【已锚定】（nodeId 非空）的卡片有效 ——
// 未锚定卡片不渲染、不订阅（C15）。
func effectiveChannelCounts(kind model.BoardKind, cards []model.Card) map[string]int {
	out := make(map[string]int, len(cards))
	for _, c := range cards {
		if c.ChannelID == "" {
			continue
		}
		if kind == model.BoardKindMap {
			anchored := c.NodeID != nil && *c.NodeID != ""
			if !anchored {
				continue
			}
		}
		out[c.ChannelID]++
	}
	return out
}

// syncCardSubscriptions 在全量保存后同步订阅：按【引用次数的差值】补订阅或释放。
func (d *Deps) syncCardSubscriptions(kind model.BoardKind, oldCards, newCards []model.Card) {
	oldN := effectiveChannelCounts(kind, oldCards)
	newN := effectiveChannelCounts(kind, newCards)
	for chID, n := range newN {
		for i := oldN[chID]; i < n; i++ {
			if ch, err := d.Store.GetChannel(chID); err == nil {
				d.subscribeCard(&model.Card{ChannelID: chID}, ch)
			}
		}
	}
	for chID, n := range oldN {
		for i := newN[chID]; i < n; i++ {
			if ch, err := d.Store.GetChannel(chID); err == nil {
				d.unsubscribeCardWith(&model.Card{ChannelID: chID}, ch)
			}
		}
	}
}
