package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/model"
	"github.com/monitorall/monitorall/internal/store"
)

// newTestAPI 起一个带临时数据库的 Gin 引擎（适配器/总线/WS 传 nil：本用例只覆盖持久化与 HTTP 契约）。
func newTestAPI(t *testing.T) (*gin.Engine, *store.SQLite) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	st, err := store.Open(config.StoreConfig{DSN: filepath.Join(dir, "t.db"), WAL: true},
		filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, _, err := NewRouter(config.DefaultConfig(), st, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("构建路由失败: %v", err)
	}
	return r, st
}

// doJSON 发起一次 JSON 请求并返回响应体。
func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是合法 JSON（%s %s）: %s", method, path, w.Body.String())
		}
	}
	return w.Code, out
}

// newTestChannel 建一个数据源与通道，返回通道 ID。
func newTestChannel(t *testing.T, st *store.SQLite, pt model.PayloadType) string {
	t.Helper()
	ds := model.NewDataSource("测试源", model.KindROS, model.ProtoROS1,
		map[string]any{"url": "ws://127.0.0.1:9090"})
	if err := st.CreateDataSource(ds); err != nil {
		t.Fatalf("建数据源失败: %v", err)
	}
	ch := model.NewChannel(ds.ID, "/gps/fix", pt, nil)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("建通道失败: %v", err)
	}
	return ch.ID
}

// TestUpdateDashboardPreservesLayout 校验 H11：全量保存时请求体没带回的 layout 必须被保留。
//
// 这是发布 gate 级用例。修复前的行为：NewCard 生成的默认 layout（0,0,3,4）会覆盖用户拖好的位置，
// 接口还返回 200 —— 用户看到"保存成功了但布局回到原点"，且找不到任何报错。
func TestUpdateDashboardPreservesLayout(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadScalar)
	board := model.NewDashboard("网格看板")
	if err := st.CreateDashboard(board); err != nil {
		t.Fatalf("建看板失败: %v", err)
	}
	card := model.NewCard(board.ID, chID, "scalar", "卡")
	card.Layout = model.Layout{X: 6, Y: 2, W: 4, H: 4, MinW: 2, MinH: 2}
	if err := st.CreateCard(card); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}

	// 全量保存：cards[] 里【故意不带 layout】
	status, resp := doJSON(t, r, http.MethodPut, config.APIPrefix+"/dashboards/"+board.ID, map[string]any{
		"name":     "改名后",
		"revision": board.Revision,
		"cards": []map[string]any{{
			"id":           card.ID,
			"channelId":    chID,
			"rendererType": "scalar",
			"title":        "卡",
		}},
	})
	if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
		t.Fatalf("全量保存应成功，实际 status=%d body=%v", status, resp)
	}
	got, err := st.GetCard(card.ID)
	if err != nil {
		t.Fatalf("读取卡片失败: %v", err)
	}
	if got.Layout.X != 6 || got.Layout.Y != 2 || got.Layout.W != 4 {
		t.Fatalf("layout 被重置了（H11 未修复）: %+v", got.Layout)
	}
}

// TestUpdateMapBoardPreservesNodeBinding 校验 H11 在 v3 的延伸：
// 全量保存不得清掉 nodeId / anchorMode / mapGeom，否则用户拖好的位置与节点绑定会凭空消失。
func TestUpdateMapBoardPreservesNodeBinding(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadGeoPose)

	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	data, _ := resp["data"].(map[string]any)
	boardID, _ := data["id"].(string)
	if boardID == "" {
		t.Fatalf("建地图看板失败: %v", resp)
	}
	board, err := st.GetDashboard(boardID)
	if err != nil {
		t.Fatalf("读取看板失败: %v", err)
	}

	_, resp = doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", map[string]any{
		"revision":    board.Revision,
		"channelId":   chID,
		"name":        "1 号车",
		"fallbackLat": 39.9,
		"fallbackLon": 116.4,
	})
	nodeData, _ := resp["data"].(map[string]any)
	nested, _ := nodeData["node"].(map[string]any)
	nodeID, _ := nested["id"].(string)
	if nodeID == "" {
		t.Fatalf("建节点失败: %v", resp)
	}
	board.Revision++

	card := model.NewCard(boardID, chID, "scalar", "卡")
	anchor := string(model.AnchorNode)
	card.NodeID = &nodeID
	card.AnchorMode = &anchor
	card.MapGeom = &model.MapGeometry{V: 1, X: 24, Y: -228, W: 354, H: 256}
	if err := st.CreateCard(card); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}

	// 全量保存：cards[] 里【故意不带 nodeId / anchorMode】
	status, resp := doJSON(t, r, http.MethodPut, config.APIPrefix+"/mapboards/"+boardID, map[string]any{
		"name":     "地图看板",
		"revision": board.Revision,
		"cards": []map[string]any{{
			"id":           card.ID,
			"channelId":    chID,
			"rendererType": "scalar",
			"title":        "卡",
		}},
	})
	if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
		t.Fatalf("全量保存应成功，实际 status=%d body=%v", status, resp)
	}
	got, err := st.GetCard(card.ID)
	if err != nil {
		t.Fatalf("读取卡片失败: %v", err)
	}
	if got.NodeID == nil || *got.NodeID != nodeID {
		t.Fatalf("nodeId 被重置了（卡片变成未锚定孤儿）: %+v", got.NodeID)
	}
	if got.MapGeom == nil || got.MapGeom.W != 354 {
		t.Fatalf("mapGeom 被重置了（拖拽结果丢失）: %+v", got.MapGeom)
	}
}

// TestMapBoardKindGuard 校验 kind 守卫：对网格看板调用地图看板端点必须被拒（40003）。
func TestMapBoardKindGuard(t *testing.T) {
	r, st := newTestAPI(t)
	board := model.NewDashboard("网格看板")
	if err := st.CreateDashboard(board); err != nil {
		t.Fatalf("建看板失败: %v", err)
	}
	status, resp := doJSON(t, r, http.MethodGet, config.APIPrefix+"/mapboards/"+board.ID, nil)
	code, _ := resp["code"].(float64)
	if status == 200 || int(code) != 40003 {
		t.Fatalf("网格看板访问地图端点应返回 40003，实际 status=%d code=%v", status, code)
	}
}

// TestCreateCardRejectsMapRendererInMapBoard 校验 C11：地图看板内禁止绑定 map 渲染器。
func TestCreateCardRejectsMapRendererInMapBoard(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadScalar)

	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	data, _ := resp["data"].(map[string]any)
	boardID, _ := data["id"].(string)
	board, err := st.GetDashboard(boardID)
	if err != nil {
		t.Fatalf("读取看板失败: %v", err)
	}
	_, resp = doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", map[string]any{
		"revision":    board.Revision,
		"name":        "静态节点",
		"fallbackLat": 39.9,
		"fallbackLon": 116.4,
	})
	nodeData, _ := resp["data"].(map[string]any)
	nested, _ := nodeData["node"].(map[string]any)
	nodeID, _ := nested["id"].(string)
	if nodeID == "" {
		t.Fatalf("建节点失败: %v", resp)
	}

	_, resp = doJSON(t, r, http.MethodPost, config.APIPrefix+"/dashboards/"+boardID+"/cards", map[string]any{
		"channelId":    chID,
		"rendererType": "map",
		"nodeId":       nodeID,
	})
	if code, _ := resp["code"].(float64); int(code) != 40003 {
		t.Fatalf("地图看板内绑 map 渲染器应返回 40003，实际 %v", resp)
	}
}

// TestNodeChannelPayloadWhitelist 校验节点位置通道的白名单（C：仅 geo_pose / json / table）。
func TestNodeChannelPayloadWhitelist(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadScalar) // scalar 不在白名单

	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	data, _ := resp["data"].(map[string]any)
	boardID, _ := data["id"].(string)
	board, _ := st.GetDashboard(boardID)

	_, resp = doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", map[string]any{
		"revision":    board.Revision,
		"channelId":   chID,
		"name":        "节点",
		"fallbackLat": 39.9,
		"fallbackLon": 116.4,
	})
	if code, _ := resp["code"].(float64); int(code) != 40003 {
		t.Fatalf("scalar 通道作为位置源应返回 40003，实际 %v", resp)
	}
}

// TestStaticNodeRequiresFallback 校验纯静态节点必须提供兜底坐标。
func TestStaticNodeRequiresFallback(t *testing.T) {
	r, _ := newTestAPI(t)
	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	data, _ := resp["data"].(map[string]any)
	boardID, _ := data["id"].(string)

	status, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", map[string]any{
		"revision": 1,
		"name":     "无兜底节点",
	})
	if code, _ := resp["code"].(float64); status == 201 || int(code) != 40001 {
		t.Fatalf("无兜底坐标的静态节点应返回 40001，实际 status=%d body=%v", status, resp)
	}
}

// TestGeometryEndpointDoesNotBumpRevision 校验 C7：几何端点不递增 revision、永不返回冲突。
func TestGeometryEndpointDoesNotBumpRevision(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadScalar)
	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	data, _ := resp["data"].(map[string]any)
	boardID, _ := data["id"].(string)

	card := model.NewCard(boardID, chID, "scalar", "卡")
	if err := st.CreateCard(card); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}
	before, _ := st.GetDashboard(boardID)

	for i := 0; i < 3; i++ {
		status, resp := doJSON(t, r, http.MethodPut, config.APIPrefix+"/mapboards/"+boardID+"/geometry", map[string]any{
			"items": []map[string]any{{
				"id": card.ID, "x": 10 + i, "y": -20, "w": 354, "h": 256,
			}},
		})
		if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
			t.Fatalf("几何保存应成功，实际 status=%d body=%v", status, resp)
		}
	}
	after, _ := st.GetDashboard(boardID)
	if after.Revision != before.Revision {
		t.Fatalf("几何保存不得递增 revision（否则会制造 40009 冲突风暴）: %d → %d",
			before.Revision, after.Revision)
	}
}

// TestNodeRevisionConflict 校验节点的 revision 严格校验。
func TestNodeRevisionConflict(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadGeoPose)
	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	data, _ := resp["data"].(map[string]any)
	boardID, _ := data["id"].(string)
	board, _ := st.GetDashboard(boardID)

	nodeBody := map[string]any{
		"revision":    board.Revision,
		"channelId":   chID,
		"name":        "1 号车",
		"fallbackLat": 39.9,
		"fallbackLon": 116.4,
	}
	if _, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", nodeBody); resp["code"].(float64) != 0 {
		t.Fatalf("首次建节点应成功: %v", resp)
	}
	// 复用同一个（已过期的）revision → 必须冲突
	_, resp = doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", nodeBody)
	if code, _ := resp["code"].(float64); int(code) != 40009 {
		t.Fatalf("过期 revision 应返回 40009，实际 %v", resp)
	}
}

// TestUpdateDashboardWithoutCardsKeepsCards 是**数据丢失**的回归防线。
//
// 缺陷形态：`PUT /dashboards/:id` 的 `cards` 是值类型数组，无法区分「请求没提卡片」
// 与「请求传了空数组」。于是只想改 globalConfig 的请求（地图样式/视角保存的真实形态）
// 会被当成"全量替换为空"→ 看板上的卡片**全部被删掉**，而接口返回 200。
// 一个只读意图的调用触发写破坏，是本项目最典型的静默数据丢失。
func TestUpdateDashboardWithoutCardsKeepsCards(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadScalar)
	board := model.NewDashboard("网格看板")
	if err := st.CreateDashboard(board); err != nil {
		t.Fatalf("建看板失败: %v", err)
	}
	card := model.NewCard(board.ID, chID, "scalar", "卡")
	if err := st.CreateCard(card); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}

	// 真实形态：只改 globalConfig，**不带 cards 字段**
	status, resp := doJSON(t, r, http.MethodPut, config.APIPrefix+"/dashboards/"+board.ID, map[string]any{
		"revision":     board.Revision,
		"globalConfig": map[string]any{"mapStyle": "amap://styles/satellite"},
	})
	if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
		t.Fatalf("保存失败: status=%d body=%v", status, resp)
	}

	cards, err := st.ListCards(board.ID)
	if err != nil {
		t.Fatalf("列卡片失败: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("不带 cards 的保存不得删除卡片，实际剩 %d 张（数据丢失）", len(cards))
	}

	// 回参也必须带卡片：前端拿到后会 `dashboard.current = updated`，
	// 若回参 cards 为空，本地列表同样会被清空（用户看到卡片消失）。
	data, _ := resp["data"].(map[string]any)
	rc, _ := data["cards"].([]any)
	if len(rc) != 1 {
		t.Fatalf("响应体应回带现有卡片，实际 %d 张", len(rc))
	}

	db, err := st.GetDashboard(board.ID)
	if err != nil {
		t.Fatalf("读取看板失败: %v", err)
	}
	if db.GlobalConfig["mapStyle"] != "amap://styles/satellite" {
		t.Fatalf("globalConfig 未保存: %+v", db.GlobalConfig)
	}
	if db.Revision != board.Revision+1 {
		t.Fatalf("revision 应自增 %d → %d，实际 %d", board.Revision, board.Revision+1, db.Revision)
	}
	if db.Name != board.Name {
		t.Fatalf("未传 name 时应保留原名，实际 %q", db.Name)
	}
}

// TestUpdateDashboardWithEmptyCardsDeletesThem 保证"显式传空数组"语义未被上面那条改坏：
// 用户确实删光了所有卡片时，必须真的删掉。
func TestUpdateDashboardWithEmptyCardsDeletesThem(t *testing.T) {
	r, st := newTestAPI(t)
	chID := newTestChannel(t, st, model.PayloadScalar)
	board := model.NewDashboard("网格看板")
	if err := st.CreateDashboard(board); err != nil {
		t.Fatalf("建看板失败: %v", err)
	}
	if err := st.CreateCard(model.NewCard(board.ID, chID, "scalar", "卡")); err != nil {
		t.Fatalf("建卡失败: %v", err)
	}

	status, resp := doJSON(t, r, http.MethodPut, config.APIPrefix+"/dashboards/"+board.ID, map[string]any{
		"revision": board.Revision,
		"cards":    []map[string]any{},
	})
	if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
		t.Fatalf("保存失败: status=%d body=%v", status, resp)
	}
	cards, err := st.ListCards(board.ID)
	if err != nil {
		t.Fatalf("列卡片失败: %v", err)
	}
	if len(cards) != 0 {
		t.Fatalf("显式传空数组应删光卡片，实际剩 %d 张", len(cards))
	}
}

// TestUpdateMapNodeIsPatchNotFullReplace 是**"改一个字段把别的清空"**的回归防线。
//
// 缺陷形态：`PUT /mapboards/:id/nodes/:nodeId` 曾与创建路径共用全量组装函数，
// 于是只改 `fallbackCrs` 的请求会把 channelId / name / 兜底坐标 / 字段路径全部清零。
// 后果两种都糟：校验拦住 → 用户对着莫名的 40001 猜；校验通过 → **字段被静默清空**且返回 200。
// 更新接口的请求体是补丁，不是全量。
func TestUpdateMapNodeIsPatchNotFullReplace(t *testing.T) {
	r, st := newTestAPI(t)
	_, resp := doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards", map[string]any{"name": "地图看板"})
	boardID := resp["data"].(map[string]any)["id"].(string)

	// 建一个静态节点（有名字 + 兜底坐标 + WGS-84）
	_, resp = doJSON(t, r, http.MethodPost, config.APIPrefix+"/mapboards/"+boardID+"/nodes", map[string]any{
		"revision":    1,
		"name":        "1号车",
		"fallbackLat": 22.909874,
		"fallbackLon": 113.878496,
		"fallbackCrs": "WGS-84",
	})
	data := resp["data"].(map[string]any)
	nodeID := data["node"].(map[string]any)["id"].(string)

	// 只改坐标解析标准 —— 这就是前端"编辑节点"实际发出的请求体形状
	status, resp := doJSON(t, r, http.MethodPut, config.APIPrefix+"/mapboards/"+boardID+"/nodes/"+nodeID, map[string]any{
		"revision":    2,
		"fallbackCrs": "GCJ-02",
	})
	if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
		t.Fatalf("局部更新失败: status=%d body=%v", status, resp)
	}

	node := resp["data"].(map[string]any)["node"].(map[string]any)
	if got := node["fallbackCrs"]; got != "GCJ-02" {
		t.Fatalf("fallbackCrs 应被更新为 GCJ-02，实际 %v", got)
	}
	// —— 以下三项就是"没提 ≠ 要清空"的核心断言 ——
	if got := node["name"]; got != "1号车" {
		t.Fatalf("未提 name 时必须保留旧值，实际 %v（字段被清空）", got)
	}
	if got, _ := node["fallbackLat"].(float64); got != 22.909874 {
		t.Fatalf("未提 fallbackLat 时必须保留旧值，实际 %v（字段被清空）", node["fallbackLat"])
	}
	if got, _ := node["fallbackLon"].(float64); got != 113.878496 {
		t.Fatalf("未提 fallbackLon 时必须保留旧值，实际 %v（字段被清空）", node["fallbackLon"])
	}

	// 落库确认（不能只在响应里对）
	stored, err := st.GetMapNode(nodeID)
	if err != nil {
		t.Fatalf("读取节点失败: %v", err)
	}
	if stored.Name != "1号车" || stored.FallbackCRS != "GCJ-02" {
		t.Fatalf("落库值不对: name=%q crs=%q", stored.Name, stored.FallbackCRS)
	}
	if stored.FallbackLat == nil || *stored.FallbackLat != 22.909874 {
		t.Fatalf("落库兜底纬度被清空: %v", stored.FallbackLat)
	}

	// 补丁语义下，改名 + 改标准必须能一起生效
	status, resp = doJSON(t, r, http.MethodPut, config.APIPrefix+"/mapboards/"+boardID+"/nodes/"+nodeID, map[string]any{
		"revision":    3,
		"name":        "1号车(改)",
		"fallbackCrs": "WGS-84",
	})
	if code, _ := resp["code"].(float64); status != 200 || int(code) != 0 {
		t.Fatalf("第二次更新失败: status=%d body=%v", status, resp)
	}
	node = resp["data"].(map[string]any)["node"].(map[string]any)
	if node["name"] != "1号车(改)" || node["fallbackCrs"] != "WGS-84" {
		t.Fatalf("改名+改标准未同时生效: %v", node)
	}
}
