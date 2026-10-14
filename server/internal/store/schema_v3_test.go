package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/version"
)

// newV2Database 手工造一个 v2 结构的数据库文件（含 v1/v2 全部表与一条旧数据），
// 用于验证真实的 v2 → v3 升级路径，而不是"全新库直接建到 v3"。
func newV2Database(t *testing.T, dir string) (dsn string, boardID string, cardID string) {
	t.Helper()
	dsn = filepath.Join(dir, "v2.db")
	db, err := sql.Open("sqlite", dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("打开 v2 测试库失败: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(ddlV1); err != nil {
		t.Fatalf("建 v1 表失败: %v", err)
	}
	if _, err := db.Exec(ddlV2Imports); err != nil {
		t.Fatalf("建 v2 imports 表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('schema_version', '2')`); err != nil {
		t.Fatalf("写入 schema_version=2 失败: %v", err)
	}
	// 用 v2 的列清单插入一条看板与一张卡片（不写 kind / node_id / anchor_mode / map_geom）。
	boardID = "db_legacy"
	cardID = "card_legacy"
	if _, err := db.Exec(`INSERT INTO dashboards
		(id, name, revision, schema_version, grid_cols, row_height, margin, global_config, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		boardID, "旧看板", 1, 1, 12, 30, "[12,12]", "{}", 1700000000000, 1700000000000); err != nil {
		t.Fatalf("插入旧看板失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cards
		(id, dashboard_id, channel_id, renderer_type, title, unit, render_config, display_options, layout, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		cardID, boardID, "ch_legacy", "scalar", "旧卡片", "", "{}", "{}", `{"x":0,"y":0,"w":3,"h":4,"minW":2,"minH":2}`,
		1700000000000, 1700000000000); err != nil {
		t.Fatalf("插入旧卡片失败: %v", err)
	}
	return dsn, boardID, cardID
}

// TestMigrationChainComplete 防止"版本号 +1 了却忘了追加迁移"（升级会静默跳过）。
func TestMigrationChainComplete(t *testing.T) {
	if len(migrations) != version.SchemaVersion {
		t.Fatalf("迁移链有 %d 条，但当前版本为 v%d —— 有版本号没有对应迁移", len(migrations), version.SchemaVersion)
	}
	for i, m := range migrations {
		want := i + 1
		if m.Version != want {
			t.Fatalf("迁移链第 %d 条版本号为 %d，应为 %d（必须连续且从 1 开始）", i+1, m.Version, want)
		}
		if name := version.History[m.Version]; name != m.Name {
			t.Fatalf("迁移 v%d 的名称 %q 与 version.History 登记的 %q 不一致", m.Version, m.Name, name)
		}
	}
}

// TestMigrateV2ToV3 验证真实升级路径：结构到位、旧数据默认值正确、升级前生成备份。
func TestMigrateV2ToV3(t *testing.T) {
	dir := t.TempDir()
	dsn, boardID, cardID := newV2Database(t, dir)
	keyFile := filepath.Join(dir, "secret.key")

	st, err := Open(config.StoreConfig{DSN: dsn, WAL: true}, keyFile)
	if err != nil {
		t.Fatalf("升级打开失败: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	got, err := st.schemaVersion(ctx)
	if err != nil {
		t.Fatalf("读取 schema_version 失败: %v", err)
	}
	if got != version.SchemaVersion {
		t.Fatalf("升级后版本错误: got %d want %d", got, version.SchemaVersion)
	}

	// —— 旧数据默认值：网格看板行为必须与升级前完全一致 ——
	var kind string
	if err := st.db.QueryRowContext(ctx, `SELECT kind FROM dashboards WHERE id = ?`, boardID).Scan(&kind); err != nil {
		t.Fatalf("读取 kind 失败: %v", err)
	}
	if kind != "grid" {
		t.Fatalf("旧看板 kind 应为 grid（自动回填），got %q", kind)
	}
	var nodeID, anchorMode, mapGeom string
	if err := st.db.QueryRowContext(ctx,
		`SELECT node_id, anchor_mode, map_geom FROM cards WHERE id = ?`, cardID).
		Scan(&nodeID, &anchorMode, &mapGeom); err != nil {
		t.Fatalf("读取卡片新列失败: %v", err)
	}
	if nodeID != "" || anchorMode != "node" || mapGeom != "{}" {
		t.Fatalf("旧卡片新列默认值错误: nodeID=%q anchorMode=%q mapGeom=%q", nodeID, anchorMode, mapGeom)
	}

	// —— map_nodes 表与索引到位 ——
	for _, name := range []string{"map_nodes", "idx_map_nodes_board", "idx_map_nodes_channel", "idx_cards_node"} {
		var count int
		if err := st.db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("查询 %s 失败: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("%s 未创建（count=%d）", name, count)
		}
	}

	// —— 升级前必须生成一致性备份 ——
	bakDir := filepath.Join(dir, "backup")
	entries, err := os.ReadDir(bakDir)
	if err != nil {
		t.Fatalf("备份目录不存在（升级前未备份）: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("升级前未生成任何备份文件")
	}

	// 备份必须是可打开、且已迁移到 v3 之前状态的一致性副本（不要求业务，只要求可打开）。
	bakDB, err := sql.Open("sqlite", filepath.Join(bakDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("备份文件无法打开: %v", err)
	}
	var n int
	if err := bakDB.QueryRow(`SELECT count(*) FROM dashboards WHERE id = ?`, boardID).Scan(&n); err != nil {
		t.Fatalf("备份内容不可用（不是一致性副本）: %v", err)
	}
	if n != 1 {
		t.Fatalf("备份缺少旧看板数据: count=%d", n)
	}
	_ = bakDB.Close()

	// —— 旧数据仍可读（scanDashboard/scanCard 不应因加列而崩） ——
	if _, err := st.GetDashboard(boardID); err != nil {
		t.Fatalf("读取旧看板失败: %v", err)
	}
	if _, err := st.GetCard(cardID); err != nil {
		t.Fatalf("读取旧卡片失败: %v", err)
	}
}

// TestMigrateRejectsDowngrade 数据库版本高于二进制时，必须启动失败而不是降级运行。
func TestMigrateRejectsDowngrade(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "99"); err != nil {
		t.Fatalf("伪造高版本失败: %v", err)
	}
	err := st.Migrate()
	if err == nil {
		t.Fatal("数据库版本高于二进制时应报错，实际通过了（会静默丢数据）")
	}
}

// TestMapGeomJSONCheck map_geom 只接受 JSON object，非法值必须被拒而不是静默存坏数据。
func TestMapGeomJSONCheck(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO cards (id, dashboard_id, channel_id, renderer_type, map_geom, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)`, "card_bad", "db_x", "ch_x", "scalar", `[1,2]`, 1, 1); err == nil {
		t.Fatal("map_geom 为数组时应被 CHECK 拒绝，实际写入成功")
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO cards (id, dashboard_id, channel_id, renderer_type, map_geom, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)`, "card_ok", "db_x", "ch_x", "scalar", `{"v":1,"x":1,"y":2,"w":3,"h":4}`, 1, 1); err != nil {
		t.Fatalf("合法 map_geom 对象应写入成功，实际: %v", err)
	}
}

// TestDashboardKindCheck kind 取值受 CHECK 约束，避免脏枚举。
func TestDashboardKindCheck(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO dashboards (id, name, kind, created_at, updated_at) VALUES (?,?,?,?,?)`,
		"db_bad", "坏看板", "satellite", 1, 1); err == nil {
		t.Fatal("非法 kind 应被 CHECK 拒绝，实际写入成功")
	}
	for _, k := range []string{"grid", "map"} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO dashboards (id, name, kind, created_at, updated_at) VALUES (?,?,?,?,?)`,
			"db_"+k, k+"看板", k, 1, 1); err != nil {
			t.Fatalf("合法 kind=%s 应写入成功，实际: %v", k, err)
		}
	}
}

// TestForeignKeysEnabledWithoutWAL 校验 H10 修复：store.wal=false 时外键仍必须生效。
// v3 的 map_nodes 两个外键（级联删看板 / 删通道置 NULL）完全依赖这条。
func TestForeignKeysEnabledWithoutWAL(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(config.StoreConfig{DSN: filepath.Join(dir, "nowal.db"), WAL: false},
		filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("以 WAL=false 打开失败: %v", err)
	}
	defer st.Close()

	var fk int
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("读取 PRAGMA foreign_keys 失败: %v", err)
	}
	if fk != 1 {
		t.Fatal("WAL=false 时外键约束未开启（H10 未修复）—— map_nodes 的级联与置 NULL 会静默失效")
	}

	// 连接回收之后仍必须生效。
	//
	// `ensurePragmas()` 只修过"打开时那一条连接"，而 Open 里设了
	// `SetConnMaxLifetime(time.Hour)` —— 到期后会新建连接，`wal:false` 时
	// 若 DSN 里没带 foreign_keys(ON)，新连接的外键就是关的且不报错。
	// 禁掉空闲连接，迫使下一次查询真的去开一条新连接来模拟这个场景。
	st.db.SetMaxIdleConns(0)
	var fkRecycled int
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fkRecycled); err != nil {
		t.Fatalf("回收连接后读取 PRAGMA foreign_keys 失败: %v", err)
	}
	if fkRecycled != 1 {
		t.Fatal("连接回收后外键约束失效 —— DSN 里缺少 foreign_keys(ON)，" +
			"ensurePragmas 只修过打开时那一条连接（wal:false 时必现，且无声）")
	}
}

// TestPruneBackups 校验备份只保留最近 N 份。
func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()
	names := []string{"monitorall-a.db", "monitorall-b.db", "monitorall-c.db", "monitorall-d.db", "monitorall-e.db"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatalf("造备份文件失败: %v", err)
		}
	}
	// 不属于备份的文件不应被清理
	if err := os.WriteFile(filepath.Join(dir, "keepme.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("造无关文件失败: %v", err)
	}
	if err := pruneBackups(dir, backupKeepCount); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != backupKeepCount+1 {
		t.Fatalf("应保留 %d 份备份 + 1 个无关文件，实际 %d 个", backupKeepCount, len(entries)-1)
	}
	if _, err := os.Stat(filepath.Join(dir, "keepme.txt")); err != nil {
		t.Fatal("无关文件被误删")
	}
	for _, n := range names[:2] { // a、b 最旧，应被删
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Fatalf("旧备份 %s 未被清理", n)
		}
	}
}
