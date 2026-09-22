package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monitorall/monitorall/internal/apperr"
)

// ————————————————— 建表 DDL 与迁移链（架构 §4.4 / §12.5） —————————————————

// CurrentSchemaVersion 为当前库结构版本，与 config.CurrentSchemaVersion 对齐。
const CurrentSchemaVersion = 2

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
}

// Migrate 按序执行未应用的迁移，每条迁移一个事务；结束后写入最新 schema_version。
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
