package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/logx"
	"github.com/monitorall/monitorall/internal/version"
)

// ————————————————— 建表 DDL 与迁移链（架构 §4.4 / §12.5） —————————————————

// CurrentSchemaVersion 为当前库结构版本。
//
// 唯一真源在 version.SchemaVersion；此处为别名，避免多处各写一份导致漂移（H9）。
const CurrentSchemaVersion = version.SchemaVersion

// ddlV1 为初始版本的全部建表语句（6 张表）。迁移链只增不改，勿编辑本常量。
const ddlV1 = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS data_sources (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	kind           TEXT NOT NULL,
	protocol       TEXT NOT NULL,
	conn_params    TEXT NOT NULL DEFAULT '{}',
	secret_keys    TEXT NOT NULL DEFAULT '[]',
	retry_policy   TEXT,
	status         TEXT NOT NULL DEFAULT 'idle',
	last_error     TEXT NOT NULL DEFAULT '',
	last_ok_at     INTEGER NOT NULL DEFAULT 0,
	enabled        INTEGER NOT NULL DEFAULT 1,
	created_at     INTEGER NOT NULL DEFAULT 0,
	updated_at     INTEGER NOT NULL DEFAULT 0,
	schema_version INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS channels (
	id             TEXT PRIMARY KEY,
	data_source_id TEXT NOT NULL,
	name           TEXT NOT NULL,
	payload_type   TEXT NOT NULL,
	meta           TEXT,
	rate_limit_hz  REAL NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL DEFAULT 0,
	UNIQUE(data_source_id, name)
);

CREATE INDEX IF NOT EXISTS idx_channels_source ON channels(data_source_id);

CREATE TABLE IF NOT EXISTS dashboards (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	revision       INTEGER NOT NULL DEFAULT 1,
	schema_version INTEGER NOT NULL DEFAULT 1,
	grid_cols      INTEGER NOT NULL DEFAULT 12,
	row_height     INTEGER NOT NULL DEFAULT 30,
	margin         TEXT NOT NULL DEFAULT '[12,12]',
	global_config  TEXT NOT NULL DEFAULT '{}',
	created_at     INTEGER NOT NULL DEFAULT 0,
	updated_at     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS cards (
	id             TEXT PRIMARY KEY,
	dashboard_id   TEXT NOT NULL,
	channel_id     TEXT NOT NULL,
	renderer_type  TEXT NOT NULL,
	title          TEXT NOT NULL DEFAULT '',
	unit           TEXT NOT NULL DEFAULT '',
	render_config  TEXT NOT NULL DEFAULT '{}',
	display_options TEXT NOT NULL DEFAULT '{}',
	layout         TEXT NOT NULL DEFAULT '{}',
	created_at     INTEGER NOT NULL DEFAULT 0,
	updated_at     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_cards_dashboard ON cards(dashboard_id);
CREATE INDEX IF NOT EXISTS idx_cards_channel ON cards(channel_id);

CREATE TABLE IF NOT EXISTS secret_meta (
	id         TEXT PRIMARY KEY,
	nonce      BLOB NOT NULL,
	ciphertext BLOB NOT NULL
);
`

// ddlV2Imports 为离线导入文件表（离线 JSON 回放功能）。
// 文件本体存于 <dataDir>/imports/<rel_path>，表内只记相对路径与解析统计。
const ddlV2Imports = `
CREATE TABLE IF NOT EXISTS imports (
	id          TEXT PRIMARY KEY,
	file_name   TEXT NOT NULL,
	rel_path    TEXT NOT NULL,
	size_bytes  INTEGER NOT NULL DEFAULT 0,
	frame_count INTEGER NOT NULL DEFAULT 0,
	first_ts    INTEGER NOT NULL DEFAULT 0,
	last_ts     INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_imports_created ON imports(created_at);
`

// ddlV3MapBoard 为 v3 MapBoard（全局地图看板）所需的结构变更。迁移链只增不改，勿编辑本常量。
//
// 设计要点：
//   - dashboards.kind 默认 'grid' —— 所有 v2 看板自动归为网格看板，行为与迁移前完全一致。
//   - cards 三列全部带默认值，且 Go 侧用指针 + omitempty —— 网格卡片的 JSON 与迁移前字节级一致。
//   - map_nodes.fallback_lat/lon 用 NULL 表达"无兜底"，**不能用 0 做哨兵**：(0,0) 是合法坐标，
//     否则"删通道时无兜底节点随通道删除"的逻辑会误删合法的 (0,0) 节点。
//   - map_geom 只做 JSON object 语法校验，具体 v1 schema 由服务层把关。
const ddlV3MapBoard = `
ALTER TABLE dashboards ADD COLUMN kind TEXT NOT NULL DEFAULT 'grid'
	CHECK (kind IN ('grid','map'));

ALTER TABLE cards ADD COLUMN node_id TEXT NOT NULL DEFAULT '';

ALTER TABLE cards ADD COLUMN anchor_mode TEXT NOT NULL DEFAULT 'node';

ALTER TABLE cards ADD COLUMN map_geom TEXT NOT NULL DEFAULT '{}'
	CHECK (json_valid(map_geom) AND json_type(map_geom) = 'object');

CREATE TABLE IF NOT EXISTS map_nodes (
	id           TEXT PRIMARY KEY,
	board_id     TEXT NOT NULL,
	channel_id   TEXT,
	name         TEXT NOT NULL CHECK (length(trim(name)) > 0),
	fallback_lat REAL,
	fallback_lon REAL,
	fallback_crs TEXT NOT NULL DEFAULT 'WGS-84'
	             CHECK (fallback_crs IN ('WGS-84','GCJ-02','BD-09')),
	payload_type TEXT NOT NULL DEFAULT '',
	lat_path     TEXT NOT NULL DEFAULT '',
	lon_path     TEXT NOT NULL DEFAULT '',
	yaw_path     TEXT NOT NULL DEFAULT '',
	yaw_unit     TEXT NOT NULL DEFAULT 'deg',
	static       INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL DEFAULT 0,
	updated_at   INTEGER NOT NULL DEFAULT 0,
	CHECK ((fallback_lat IS NULL) = (fallback_lon IS NULL)),
	CHECK (fallback_lat IS NULL OR (fallback_lat BETWEEN -90 AND 90)),
	CHECK (fallback_lon IS NULL OR (fallback_lon BETWEEN -180 AND 180)),
	FOREIGN KEY (board_id)   REFERENCES dashboards(id) ON DELETE CASCADE,
	FOREIGN KEY (channel_id) REFERENCES channels(id)   ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_map_nodes_board   ON map_nodes(board_id);
CREATE INDEX IF NOT EXISTS idx_map_nodes_channel ON map_nodes(channel_id) WHERE channel_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cards_node        ON cards(node_id) WHERE node_id <> '';
`

// ddlV4AppSettings 为全局设置表（品牌 / 天气等运行期可改项）。
//
// 为什么用「key + JSON blob」而不是每项一列：
//   - 设置项会持续增加，每加一项就 ALTER TABLE 一次意味着每加一项都要发一条迁移；
//   - 每列都需要默认值与「用户没填过」的表达方式，列越多越容易漂移。
//
// 表内只放两个 key：branding / weather，各自是一个 JSON blob。
// 优先级链为 DB > config.yaml > 代码默认值，缺失字段由上层回落补齐（api/settings.go）。
const ddlV4AppSettings = `
CREATE TABLE IF NOT EXISTS app_settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at INTEGER NOT NULL DEFAULT 0
);
`

// Migration 为一条迁移：版本号、名称与 SQL。
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// migrations 为迁移链，按顺序执行且幂等。新增迁移只增不改。
var migrations = []Migration{
	{Version: 1, Name: "init", SQL: ddlV1},
	{Version: 2, Name: "add_imports", SQL: ddlV2Imports},
	{Version: 3, Name: "add_mapboard", SQL: ddlV3MapBoard},
	{Version: 4, Name: "add_app_settings", SQL: ddlV4AppSettings},
}

// Migrate 按序执行未应用的迁移，每条迁移一个事务；结束后写入最新 schema_version。
//
// 升级前先做一份一致性快照（VACUUM INTO），失败即中止 —— 迁移不可逆，无备份不升级。
func (s *SQLite) Migrate() error {
	ctx := context.Background()
	// 先自举 meta 表：全新库尚无 meta，直接读 schema_version 会报 "no such table"。
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return apperr.Wrap(err, apperr.StoreError, "初始化 meta 表失败")
	}
	current, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}
	if current > CurrentSchemaVersion {
		// 数据库来自更高版本的二进制 —— 绝不降级运行（会静默丢数据），直接启动失败。
		return apperr.Newf(apperr.StoreError,
			"数据库结构版本 v%d 高于本程序支持的 v%d，请先升级 MonitorAll 或恢复对应版本的备份",
			current, CurrentSchemaVersion)
	}
	if path, err := s.backupBeforeMigrate(ctx, current, CurrentSchemaVersion); err != nil {
		return err
	} else if path != "" {
		logx.With("module", "store").Info("迁移前已生成数据库一致性备份",
			"from", current, "to", CurrentSchemaVersion, "backup", path)
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
		current = m.Version
	}
	return nil
}

// applyMigration 在单个事务内执行一条迁移并更新 meta.schema_version。
func (s *SQLite) applyMigration(ctx context.Context, m Migration) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("执行迁移 v%d(%s) 失败: %w", m.Version, m.Name, err)
		}
		if _, err := t.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			fmt.Sprintf("%d", m.Version)); err != nil {
			return fmt.Errorf("写入 schema_version 失败: %w", err)
		}
		return nil
	})
}

// schemaVersion 读取 meta.schema_version，缺失返回 0。
func (s *SQLite) schemaVersion(ctx context.Context) (int, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, apperr.Wrap(err, apperr.StoreError, "读取 schema_version 失败")
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, nil
	}
	return n, nil
}
