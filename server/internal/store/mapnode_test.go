package store

import (
	"testing"

	"github.com/monitorall/monitorall/internal/model"
)

// newTestMapBoard 建一个地图看板（含一个数据源与一个 geo 通道）。
func newTestMapBoard(t *testing.T, st *SQLite) (board *model.Dashboard, ch *model.Channel) {
	t.Helper()
	ds := model.NewDataSource("测试源", model.KindROS, model.ProtoROS1,
		map[string]any{"url": "ws://127.0.0.1:9090"})
	if err := st.CreateDataSource(ds); err != nil {
		t.Fatalf("建数据源失败: %v", err)
	}
	ch = model.NewChannel(ds.ID, "/gps/fix", model.PayloadGeoPose, nil)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	board = model.NewDashboard("测试地图看板")
	board.Kind = model.BoardKindMap
	if err := st.CreateDashboard(board); err != nil {
		t.Fatalf("建地图看板失败: %v", err)
	}
	return board, ch
}

// newTestNode 构造一个带兜底坐标的节点请求体。
func newTestNode(chID string) *model.MapNode {
	lat, lon := 39.90923, 116.397428
	return &model.MapNode{
		ChannelID:   chID,
		Name:        "1 号车",
		FallbackLat: &lat,
		FallbackLon: &lon,
		FallbackCRS: model.CRSWGS84,
		PayloadType: string(model.PayloadGeoPose),
	}
}

// TestMapNodeCRUD 校验节点增删改查与 revision 严格校验。
func TestMapNodeCRUD(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)

	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	if res.Revision != board.Revision+1 {
		t.Fatalf("创建节点后 revision 应为 %d，实际 %d", board.Revision+1, res.Revision)
	}
	if res.Node.ID == "" || res.Node.BoardID != board.ID {
		t.Fatalf("节点回参不完整: %+v", res.Node)
	}

	nodes, err := st.ListMapNodes(board.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("列节点失败或数量不符: %v %d", err, len(nodes))
	}
	if nodes[0].FallbackLat == nil || *nodes[0].FallbackLat != 39.90923 {
		t.Fatalf("兜底纬度未正确持久化: %+v", nodes[0].FallbackLat)
	}

	// 用旧 revision 再保存 → 必须冲突（乐观锁）
	updated := newTestNode(ch.ID)
	updated.ID = res.Node.ID
	updated.Name = "改名后"
	if _, err := st.UpdateMapNode(board.ID, updated, board.Revision); err == nil {
		t.Fatal("用旧 revision 更新应返回冲突，实际通过了")
	}
	if _, err := st.UpdateMapNode(board.ID, updated, res.Revision); err != nil {
		t.Fatalf("用新 revision 更新失败: %v", err)
	}
	got, err := st.GetMapNode(res.Node.ID)
	if err != nil {
		t.Fatalf("读取节点失败: %v", err)
	}
	if got.Name != "改名后" {
		t.Fatalf("更新未生效: %s", got.Name)
	}
}

// TestMapNodeMaxPerBoard 校验节点数上限（D0-4：≤20）。
func TestMapNodeMaxPerBoard(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	rev := board.Revision
	for i := 0; i < model.MaxNodesPerBoard; i++ {
		n := newTestNode(ch.ID)
		r, err := st.CreateMapNode(board.ID, n, rev)
		if err != nil {
			t.Fatalf("创建第 %d 个节点失败: %v", i+1, err)
		}
		rev = r.Revision
	}
	if _, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), rev); err == nil {
		t.Fatalf("超过 %d 个节点仍创建成功", model.MaxNodesPerBoard)
	}
}

// TestDeleteNodeUnbindKeepsCards 校验 C10：删节点默认解绑保留卡片，且 map_geom 不丢（便于 rebind）。
func TestDeleteNodeUnbindKeepsCards(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	nodeID := res.Node.ID

	ids := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		c := model.NewCard(board.ID, ch.ID, "scalar", "卡")
		anchor := string(model.AnchorNode)
		c.NodeID = &nodeID
		c.AnchorMode = &anchor
		c.MapGeom = &model.MapGeometry{V: 1, X: 24, Y: -228, W: 354, H: 256}
		if err := st.CreateCard(c); err != nil {
			t.Fatalf("建卡失败: %v", err)
		}
		ids = append(ids, c.ID)
	}

	del, err := st.DeleteMapNode(board.ID, nodeID, false, res.Revision)
	if err != nil {
		t.Fatalf("删节点失败: %v", err)
	}
	if del.Unbound != 2 || del.Deleted != 0 {
		t.Fatalf("解绑数应为 2/0，实际 %d/%d", del.Unbound, del.Deleted)
	}
	if _, err := st.GetMapNode(nodeID); err == nil {
		t.Fatal("节点应已被删除")
	}
	for _, id := range ids {
		card, err := st.GetCard(id)
		if err != nil {
			t.Fatalf("卡片应保留，实际被删: %v", err)
		}
		if card.NodeID != nil {
			t.Fatalf("卡片应已解绑（nodeId 为空），实际 %q", *card.NodeID)
		}
		// anchor_mode 保持默认值 node：不变式是 node_id = '' ⟺ 未锚定。
		// 不写 'free' —— 否则重新挂载时前端通常只发 nodeId，anchor_mode 会永久继承 'free'，
		// 形成 node_id='n_x' + anchor_mode='free' 的自相矛盾状态。
		// 注意：读取时默认值 'node' 会被折叠成 nil（网格卡片 JSON 必须与 v3 之前字节级一致），
		// 所以这里是"nil 或 node"都算通过，唯一要拦住的是 'free'。
		if card.AnchorMode != nil && *card.AnchorMode != string(model.AnchorNode) {
			t.Fatalf("解绑后 anchorMode 不得为 %q（未锚定由 node_id='' 表达）", *card.AnchorMode)
		}
		var rawAnchor string
		if err := st.db.QueryRow(`SELECT anchor_mode FROM cards WHERE id = ?`, id).Scan(&rawAnchor); err != nil {
			t.Fatalf("读取 anchor_mode 失败: %v", err)
		}
		if rawAnchor != string(model.AnchorNode) {
			t.Fatalf("库内 anchor_mode 应保持 %q，实际 %q", model.AnchorNode, rawAnchor)
		}
		// map_geom 保留在库里（重新绑定后位置能回来），但未锚定卡片不会在接口上暴露它。
		var rawGeom string
		if err := st.db.QueryRow(`SELECT map_geom FROM cards WHERE id = ?`, id).Scan(&rawGeom); err != nil {
			t.Fatalf("读取 map_geom 失败: %v", err)
		}
		if rawGeom == "" || rawGeom == "{}" {
			t.Fatalf("解绑应保留 map_geom（便于重新绑定），实际 %q", rawGeom)
		}
	}
}

// TestDeleteNodeCascade 校验 cascade=delete 时卡片一并删除。
func TestDeleteNodeCascade(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	nodeID := res.Node.ID
	c := model.NewCard(board.ID, ch.ID, "scalar", "卡")
	c.NodeID = &nodeID
	if err := st.CreateCard(c); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}
	del, err := st.DeleteMapNode(board.ID, nodeID, true, res.Revision)
	if err != nil {
		t.Fatalf("删节点失败: %v", err)
	}
	if del.Deleted != 1 {
		t.Fatalf("级联删除应删掉 1 张卡，实际 %d", del.Deleted)
	}
	if _, err := st.GetCard(c.ID); err == nil {
		t.Fatal("级联删除后卡片仍存在")
	}
}

// TestDeleteChannelDetachesNodes 校验删通道时节点的两种处置（有兜底 → 转静态；无兜底 → 随通道删）。
func TestDeleteChannelDetachesNodes(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	rev := board.Revision

	withFallback, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), rev)
	if err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	// 绑定同一个通道，但没有兜底坐标 —— 通道一删它就失去唯一位置来源，应随通道删除。
	noFallback := &model.MapNode{ChannelID: ch.ID, Name: "无兜底节点", FallbackCRS: model.CRSWGS84}
	noFallbackRes, err := st.CreateMapNode(board.ID, noFallback, withFallback.Revision)
	if err != nil {
		t.Fatalf("建无兜底节点失败: %v", err)
	}

	if err := st.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("删通道失败: %v", err)
	}
	got, err := st.GetMapNode(withFallback.Node.ID)
	if err != nil {
		t.Fatalf("有兜底的节点应被保留，实际被删: %v", err)
	}
	if got.ChannelID != "" {
		t.Fatalf("有兜底节点应降级为静态（channelId 置空），实际 %q", got.ChannelID)
	}
	if !got.Static {
		t.Fatal("有兜底节点降级后 static 应为 true")
	}
	if _, err := st.GetMapNode(noFallbackRes.Node.ID); err == nil {
		t.Fatal("无兜底的节点应随通道删除")
	}
}

// TestDeleteDashboardCascadesNodes 校验删看板时节点被清理（不依赖外键开关）。
func TestDeleteDashboardCascadesNodes(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	if err := st.DeleteDashboard(board.ID); err != nil {
		t.Fatalf("删看板失败: %v", err)
	}
	if _, err := st.GetMapNode(res.Node.ID); err == nil {
		t.Fatal("删看板后仍残留孤儿节点")
	}
}

// TestSaveCardGeometry 校验几何保存：写入生效、不递增 revision（C7）、跳过他板卡片。
func TestSaveCardGeometry(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	nodeID := res.Node.ID
	c := model.NewCard(board.ID, ch.ID, "scalar", "卡")
	c.NodeID = &nodeID
	if err := st.CreateCard(c); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}
	before, err := st.GetDashboard(board.ID)
	if err != nil {
		t.Fatalf("读取看板失败: %v", err)
	}

	geom := &model.MapGeometry{V: 1, X: 10, Y: -20, W: 354, H: 256}
	out, err := st.SaveCardGeometry(board.ID, map[string]*model.MapGeometry{
		c.ID:  geom,
		"不存在": geom,
	})
	if err != nil {
		t.Fatalf("保存几何失败: %v", err)
	}
	if out.Saved != 1 || out.Skipped != 1 {
		t.Fatalf("saved/skipped 应为 1/1，实际 %d/%d", out.Saved, out.Skipped)
	}
	after, err := st.GetDashboard(board.ID)
	if err != nil {
		t.Fatalf("读取看板失败: %v", err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("几何保存不得递增 revision（C7）：%d → %d", before.Revision, after.Revision)
	}
	card, err := st.GetCard(c.ID)
	if err != nil {
		t.Fatalf("读取卡片失败: %v", err)
	}
	if card.MapGeom == nil || card.MapGeom.W != 354 || card.MapGeom.Y != -20 {
		t.Fatalf("几何未正确持久化: %+v", card.MapGeom)
	}
}

// TestFindDirtyNodeRefs 校验脏引用自检：指向不存在节点 / 跨板引用都能被报告。
func TestFindDirtyNodeRefs(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	st.db.Exec(`INSERT INTO cards
		(id, dashboard_id, channel_id, renderer_type, node_id, anchor_mode, map_geom, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		"cd_dangling", board.ID, ch.ID, "scalar", "mn_not_exist", "node", "{}", 1, 1)
	st.db.Exec(`INSERT INTO cards
		(id, dashboard_id, channel_id, renderer_type, node_id, anchor_mode, map_geom, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		"cd_other", "db_another", ch.ID, "scalar", res.Node.ID, "node", "{}", 1, 1)

	refs, err := st.FindDirtyNodeRefs()
	if err != nil {
		t.Fatalf("自检失败: %v", err)
	}
	problems := map[string]string{}
	for _, r := range refs {
		problems[r.CardID] = r.Problem
	}
	if problems["cd_dangling"] != "node_missing" {
		t.Fatalf("悬空引用未被报告或类型不对: %+v", problems)
	}
	if problems["cd_other"] != "node_other_board" {
		t.Fatalf("跨板引用未被报告或类型不对: %+v", problems)
	}
}

// TestReplaceCardsAndUpdateDashboardAtomic 校验 H16：换卡片与 revision 自增在同一事务。
// 第二步失败（revision 不匹配）时卡片不能被换掉。
func TestReplaceCardsAndUpdateDashboardAtomic(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	old := model.NewCard(board.ID, ch.ID, "scalar", "旧卡")
	if err := st.CreateCard(old); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}
	db, err := st.GetDashboard(board.ID)
	if err != nil {
		t.Fatalf("读取看板失败: %v", err)
	}
	db.Revision++ // 假装自增，但用错误的期望值构造 CAS 失败
	db.Revision = 999

	newCard := model.NewCard(board.ID, ch.ID, "scalar", "新卡")
	if err := st.ReplaceCardsAndUpdateDashboard(db, []model.Card{*newCard}); err == nil {
		t.Fatal("revision 不匹配时应返回冲突，实际通过了")
	}
	cards, err := st.ListCards(board.ID)
	if err != nil {
		t.Fatalf("列卡片失败: %v", err)
	}
	if len(cards) != 1 || cards[0].ID != old.ID {
		t.Fatalf("事务回滚后旧卡片应原样保留，实际 %+v", cards)
	}
}

// TestGridCardJSONUnchanged 校验网格卡片的三个 MapBoard 字段为 nil（序列化后与 v3 之前字节级一致）。
func TestGridCardJSONUnchanged(t *testing.T) {
	st := newTestStore(t)
	_, ch := newTestMapBoard(t, st)
	board := model.NewDashboard("网格看板") // Kind 默认 grid
	if err := st.CreateDashboard(board); err != nil {
		t.Fatalf("建网格看板失败: %v", err)
	}
	c := model.NewCard(board.ID, ch.ID, "scalar", "网格卡")
	if err := st.CreateCard(c); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}
	got, err := st.GetCard(c.ID)
	if err != nil {
		t.Fatalf("读取卡片失败: %v", err)
	}
	if got.NodeID != nil || got.AnchorMode != nil || got.MapGeom != nil {
		t.Fatalf("网格卡片的 MapBoard 字段必须保持 nil（否则 JSON 会多出字段）: %+v", got)
	}
}

// newTestSecondChannel 建第二条通道：测试卡片的 channel_id 必须与被测节点所属通道不同，
// 否则会先被 DeleteChannel 的 `DELETE FROM cards WHERE channel_id=?` 删掉，
// 就观察不到"解绑"这条路径了。
func newTestSecondChannel(t *testing.T, st *SQLite, dsID string) *model.Channel {
	t.Helper()
	ch := model.NewChannel(dsID, "/cam/front", model.PayloadVideoStream, nil)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("建第二条通道失败: %v", err)
	}
	return ch
}

// TestDeleteChannelKeepsFallbackNodeAsStatic 校验删通道时"有兜底坐标"的节点降级为静态节点保留。
//
// 回归点：降级曾写成 `SET channel_id = NULL`，在 NOT NULL DEFAULT ” 的 DDL 下会直接违反约束，
// 让整个 DeleteChannel 事务回滚 —— 删通道是常规操作，不是边缘路径。
func TestDeleteChannelKeepsFallbackNodeAsStatic(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	res, err := st.CreateMapNode(board.ID, newTestNode(ch.ID), board.Revision)
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	if err := st.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("删通道失败（有兜底节点应降级保留，不应回滚）: %v", err)
	}
	got, err := st.GetMapNode(res.Node.ID)
	if err != nil {
		t.Fatalf("有兜底节点应保留，实际被删: %v", err)
	}
	if got.ChannelID != "" {
		t.Fatalf("降级后 channelId 应为无通道（空），实际 %q", got.ChannelID)
	}
	if !got.Static {
		t.Fatal("降级后 static 应为 true")
	}
	if got.FallbackLat == nil || got.FallbackLon == nil {
		t.Fatal("降级后兜底坐标必须保留（这是节点还能显示的原因）")
	}
}

// TestDeleteChannelUnbindsCardsOnRemovedNode 校验删通道时"无兜底"节点随通道删除，
// 且挂在它上面的卡片被解绑（不能留下指向已不存在节点的孤儿 node_id）。
//
// 回归点：DetachMapNodesByChannel 曾只处理节点、完全没解绑卡片 ——
// 一旦上面那条降级语句修好不再挡住事务，这条常规路径就会开始生产脏数据。
func TestDeleteChannelUnbindsCardsOnRemovedNode(t *testing.T) {
	st := newTestStore(t)
	board, ch := newTestMapBoard(t, st)
	camCh := newTestSecondChannel(t, st, ch.DataSourceID)

	node := newTestNode(ch.ID)
	node.FallbackLat = nil
	node.FallbackLon = nil
	node.PayloadType = string(model.PayloadGeoPose)
	res, err := st.CreateMapNode(board.ID, node, board.Revision)
	if err != nil {
		t.Fatalf("创建无兜底节点失败: %v", err)
	}
	nodeID := res.Node.ID

	c := model.NewCard(board.ID, camCh.ID, "video_stream", "随车画面")
	c.NodeID = &nodeID
	if err := st.CreateCard(c); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}

	if err := st.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("删通道失败: %v", err)
	}
	if _, err := st.GetMapNode(nodeID); err == nil {
		t.Fatal("无兜底节点应随通道删除")
	}
	card, err := st.GetCard(c.ID)
	if err != nil {
		t.Fatalf("卡片应保留（解绑而非删除）: %v", err)
	}
	if card.NodeID != nil && *card.NodeID != "" {
		t.Fatalf("节点已删，卡片必须解绑（nodeId 为空），实际 %q", *card.NodeID)
	}
}
