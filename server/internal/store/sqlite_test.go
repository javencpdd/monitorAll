package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/crypto"
	"github.com/monitorall/monitorall/internal/model"
)

// newTestStore 在临时目录打开一个测试用数据库。
func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(config.StoreConfig{DSN: filepath.Join(dir, "test.db"), WAL: true},
		filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestMigrateIdempotent 校验建表与迁移幂等、schemaVersion 正确。
func TestMigrateIdempotent(t *testing.T) {
	st := newTestStore(t)
	if err := st.Migrate(); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	v, err := st.schemaVersion(context.Background())
	if err != nil {
		t.Fatalf("读取 schema_version 失败: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Fatalf("schema_version 错误: got %d want %d", v, CurrentSchemaVersion)
	}
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
}

// TestDataSourceCRUD 校验数据源增删改查。
func TestDataSourceCRUD(t *testing.T) {
	st := newTestStore(t)
	ds := model.NewDataSource("机械狗摄像头", model.KindVideo, model.ProtoRTMP,
		map[string]any{"rtmpUrl": "rtmp://172.31.68.227:1936/live/lite3"})
	if err := st.CreateDataSource(ds); err != nil {
		t.Fatalf("创建数据源失败: %v", err)
	}
	got, err := st.GetDataSource(ds.ID)
	if err != nil {
		t.Fatalf("读取数据源失败: %v", err)
	}
	if got.Name != "机械狗摄像头" || got.Kind != model.KindVideo {
		t.Fatalf("数据源字段不符: %+v", got)
	}
	got.Name = "改名后"
	if err := st.UpdateDataSource(got); err != nil {
		t.Fatalf("更新数据源失败: %v", err)
	}
	if again, _ := st.GetDataSource(ds.ID); again.Name != "改名后" {
		t.Fatalf("更新未生效: %+v", again)
	}
	list, err := st.ListDataSources()
	if err != nil || len(list) != 1 {
		t.Fatalf("列表查询失败: %v len=%d", err, len(list))
	}
	if err := st.DeleteDataSource(ds.ID); err != nil {
		t.Fatalf("删除数据源失败: %v", err)
	}
	if _, err := st.GetDataSource(ds.ID); err == nil {
		t.Fatal("删除后不应能读到数据源")
	}
}

// TestSecretPersistMasked 校验敏感字段落盘加密、出参为 ***、适配器读取时解密还原。
func TestSecretPersistMasked(t *testing.T) {
	st := newTestStore(t)
	ds := model.NewDataSource("遥测服务", model.KindHTTP, model.ProtoHTTPPoll, map[string]any{
		"url":     "http://172.31.68.9:8080/api/stat",
		"method":  "GET",
		"headers": map[string]any{"Authorization": "Bearer real-token"},
	})
	ds.SecretKeys = crypto.DefaultSecretKeys(model.KindHTTP)
	if err := st.CreateDataSource(ds); err != nil {
		t.Fatalf("创建数据源失败: %v", err)
	}

	// 出参（库内）应为 ***
	stored, err := st.GetDataSource(ds.ID)
	if err != nil {
		t.Fatalf("读取数据源失败: %v", err)
	}
	v, _ := crypto.GetByPath(stored.ConnParams, "headers.Authorization")
	if v != crypto.MaskedValue {
		t.Fatalf("落盘的 connParams 应脱敏为 ***，实际: %v", v)
	}
	// 适配器读取应还原明文
	real, err := st.ConnParamsFor(ds.ID)
	if err != nil {
		t.Fatalf("读取真实连接参数失败: %v", err)
	}
	rv, _ := crypto.GetByPath(real, "headers.Authorization")
	if rv != "Bearer real-token" {
		t.Fatalf("真实参数未还原: %v", rv)
	}

	// 更新时传 *** 应保持原值
	stored.ConnParams["headers"].(map[string]any)["Authorization"] = crypto.MaskedValue
	stored.ConnParams["url"] = "http://172.31.68.9:8080/api/v2/stat"
	if err := st.UpdateDataSource(stored); err != nil {
		t.Fatalf("更新数据源失败: %v", err)
	}
	after, _ := st.ConnParamsFor(ds.ID)
	av, _ := crypto.GetByPath(after, "headers.Authorization")
	if av != "Bearer real-token" {
		t.Fatalf("*** 未保持原值: %v", av)
	}
}

// TestChannelCRUD 校验通道增删改查与唯一约束。
func TestChannelCRUD(t *testing.T) {
	st := newTestStore(t)
	ds := model.NewDataSource("ROS1", model.KindROS, model.ProtoROS1, map[string]any{"bridgeUrl": "ws://172.31.68.227:9090"})
	if err := st.CreateDataSource(ds); err != nil {
		t.Fatalf("创建数据源失败: %v", err)
	}
	ch := model.NewChannel(ds.ID, "/odom", model.PayloadTimeSeries, map[string]any{"rosMsgType": "nav_msgs/Odometry"})
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("创建通道失败: %v", err)
	}
	list, err := st.ListChannelsBySource(ds.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("查询通道失败: %v len=%d", err, len(list))
	}
	if list[0].PayloadType != model.PayloadTimeSeries {
		t.Fatalf("payloadType 不符: %+v", list[0])
	}
	dup := model.NewChannel(ds.ID, "/odom", model.PayloadJSON, nil)
	if err := st.CreateChannel(dup); err == nil {
		t.Fatal("同名通道应命中唯一约束")
	}
	if err := st.DeleteChannel(ch.ID); err != nil {
		t.Fatalf("删除通道失败: %v", err)
	}
	if _, err := st.GetChannel(ch.ID); err == nil {
		t.Fatal("删除后不应能读到通道")
	}
}

// TestDashboardAndCardCRUD 校验看板与卡片 CRUD、全量替换与级联删除。
func TestDashboardAndCardCRUD(t *testing.T) {
	st := newTestStore(t)
	db := model.NewDashboard("默认看板")
	if err := st.CreateDashboard(db); err != nil {
		t.Fatalf("创建看板失败: %v", err)
	}
	if db.Revision != 1 || db.GridCols != model.DefaultGridCols || db.Margin != [2]int{12, 12} {
		t.Fatalf("看板默认值不符: %+v", db)
	}
	ds := model.NewDataSource("HTTP", model.KindHTTP, model.ProtoHTTPPoll, map[string]any{"url": "http://x/api"})
	if err := st.CreateDataSource(ds); err != nil {
		t.Fatalf("创建数据源失败: %v", err)
	}
	ch := model.NewChannel(ds.ID, "/api", model.PayloadJSON, nil)
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("创建通道失败: %v", err)
	}
	card := model.NewCard(db.ID, ch.ID, "json-tree", "原始响应")
	if err := st.CreateCard(card); err != nil {
		t.Fatalf("创建卡片失败: %v", err)
	}
	cards, err := st.ListCards(db.ID)
	if err != nil || len(cards) != 1 {
		t.Fatalf("查询卡片失败: %v len=%d", err, len(cards))
	}
	// 全量替换
	card2 := model.NewCard(db.ID, ch.ID, "table", "表格")
	if err := st.ReplaceCards(db.ID, []model.Card{*card2}); err != nil {
		t.Fatalf("全量替换卡片失败: %v", err)
	}
	if got, _ := st.ListCards(db.ID); len(got) != 1 || got[0].RendererType != "table" {
		t.Fatalf("替换结果不符: %+v", got)
	}
	// 级联：删除数据源应清掉通道与卡片
	if err := st.DeleteDataSource(ds.ID); err != nil {
		t.Fatalf("删除数据源失败: %v", err)
	}
	if got, _ := st.ListCards(db.ID); len(got) != 0 {
		t.Fatalf("级联清理卡片失败: %+v", got)
	}
	if err := st.DeleteDashboard(db.ID); err != nil {
		t.Fatalf("删除看板失败: %v", err)
	}
	if _, err := st.GetDashboard(db.ID); err == nil {
		t.Fatal("删除后不应能读到看板")
	}
}

// TestEnsureDefaultDashboard 校验首次启动自动创建默认看板（D8）且不重复创建。
func TestEnsureDefaultDashboard(t *testing.T) {
	st := newTestStore(t)
	if err := EnsureDefaultDashboard(st); err != nil {
		t.Fatalf("创建默认看板失败: %v", err)
	}
	if err := EnsureDefaultDashboard(st); err != nil {
		t.Fatalf("重复调用失败: %v", err)
	}
	list, err := st.ListDashboards()
	if err != nil || len(list) != 1 {
		t.Fatalf("默认看板数量错误: %v len=%d", err, len(list))
	}
	if list[0].Name != config.DefaultDashboardName {
		t.Fatalf("默认看板名称错误: %s", list[0].Name)
	}
}
