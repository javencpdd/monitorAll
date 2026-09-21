package api

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 看板请求体 —————————————————

// CreateDashboardReq 为新建看板的请求体。
type CreateDashboardReq struct {
	Name     string `json:"name" binding:"required"`
	GridCols int    `json:"gridCols,omitempty"`
}

// UpdateDashboardReq 为全量保存看板的请求体（含 revision 乐观锁）。
type UpdateDashboardReq struct {
	Name         string            `json:"name"`
	Revision     int64             `json:"revision"`
	GridCols     int               `json:"gridCols,omitempty"`
	RowHeight    int               `json:"rowHeight,omitempty"`
	Margin       [2]int            `json:"margin,omitempty"`
	GlobalConfig map[string]any    `json:"globalConfig,omitempty"`
	Cards        []UpdateCardItem  `json:"cards"`
}

// UpdateCardItem 为全量保存中的卡片项。
type UpdateCardItem struct {
	ID             string                 `json:"id,omitempty"`
	ChannelID      string                 `json:"channelId"`
	RendererType   string                 `json:"rendererType"`
	Title          string                 `json:"title"`
	Unit           string                 `json:"unit,omitempty"`
	RenderConfig   map[string]any         `json:"renderConfig,omitempty"`
	DisplayOptions *model.DisplayOptions  `json:"displayOptions,omitempty"`
	Layout         *model.Layout          `json:"layout,omitempty"`
}

// UpdateLayoutReq 为轻量保存布局的请求体（拖拽后高频调用）。
type UpdateLayoutReq struct {
	Revision int64          `json:"revision"`
	Layouts  []LayoutItem   `json:"layouts"`
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
}

// ————————————————— 看板 CRUD —————————————————

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

	newCards := make([]model.Card, 0, len(req.Cards))
	oldCards, err := d.Store.ListCards(id)
	if err != nil {
		Fail(c, err)
		return
	}
	oldByID := make(map[string]model.Card, len(oldCards))
	for _, oc := range oldCards {
		oldByID[oc.ID] = oc
	}

	for _, item := range req.Cards {
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

	// 全量保存为事务：先换卡片再看板
	if err := d.Store.ReplaceCards(id, newCards); err != nil {
		Fail(c, err)
		return
	}
	if err := d.Store.UpdateDashboard(db); err != nil {
		Fail(c, err)
		return
	}
	d.syncCardSubscriptions(oldCards, newCards)
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

// deleteDashboard 删除看板并释放其卡片持有的订阅。
func (d *Deps) deleteDashboard(c *gin.Context) {
	id := c.Param("id")
	cards, err := d.Store.ListCards(id)
	if err != nil {
		Fail(c, err)
		return
	}
	for i := range cards {
		d.unsubscribeCard(&cards[i])
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
	if _, err := d.Store.GetDashboard(dashboardID); err != nil {
		Fail(c, err)
		return
	}
	var req CreateCardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, apperr.Wrap(err, apperr.BadRequest, "请求体解析失败"))
		return
	}
	ch, err := d.Store.GetChannel(req.ChannelID)
	if err != nil {
		Fail(c, err)
		return
	}
	card := model.NewCard(dashboardID, req.ChannelID, req.RendererType, req.Title)
	card.Unit = req.Unit
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
	d.subscribeCard(card, ch)
	Created(c, card)
}

// updateCard 更新卡片；改通道时先退订旧通道再订阅新通道。
func (d *Deps) updateCard(c *gin.Context) {
	card, err := d.Store.GetCard(c.Param("id"))
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
	if req.ChannelID != "" && req.ChannelID != oldChannelID {
		newCh, err := d.Store.GetChannel(req.ChannelID)
		if err != nil {
			Fail(c, err)
			return
		}
		card.ChannelID = req.ChannelID
		_ = newCh
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
	card.UpdatedAt = time.Now().UnixMilli()
	if err := d.Store.UpdateCard(card); err != nil {
		Fail(c, err)
		return
	}
	if req.ChannelID != "" && req.ChannelID != oldChannelID {
		// 先退订旧通道，再订阅新通道
		if oldCh, err := d.Store.GetChannel(oldChannelID); err == nil {
			d.unsubscribeCardWith(card, oldCh)
		}
		if newCh, err := d.Store.GetChannel(card.ChannelID); err == nil {
			d.subscribeCard(card, newCh)
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
	d.unsubscribeCard(card)
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

// syncCardSubscriptions 在全量保存后同步订阅：新增的订阅、移除的释放。
func (d *Deps) syncCardSubscriptions(oldCards, newCards []model.Card) {
	oldSet := make(map[string]struct{}, len(oldCards))
	for _, c := range oldCards {
		oldSet[c.ChannelID] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newCards))
	for _, c := range newCards {
		newSet[c.ChannelID] = struct{}{}
	}
	for _, c := range newCards {
		if _, ok := oldSet[c.ChannelID]; ok {
			continue
		}
		if ch, err := d.Store.GetChannel(c.ChannelID); err == nil {
			d.subscribeCard(&c, ch)
		}
	}
	for _, c := range oldCards {
		if _, ok := newSet[c.ChannelID]; ok {
			continue
		}
		if ch, err := d.Store.GetChannel(c.ChannelID); err == nil {
			d.unsubscribeCardWith(&c, ch)
		}
	}
}
