// Package store 提供 SQLite（modernc.org/sqlite，纯 Go 免 CGO）持久化实现：
// 6 张表 + schemaVersion 迁移链 + 各类实体 CRUD + 敏感字段加密落盘。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 注册 "sqlite" 数据库驱动（纯 Go，免 CGO）

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/config"
	"github.com/monitorall/monitorall/internal/crypto"
	"github.com/monitorall/monitorall/internal/model"
)

// Store 定义 6 类实体的 CRUD 契约（便于未来替换实现与单测 mock）。
type Store interface {
	// 生命周期
	Close() error
	Ping(ctx context.Context) error

	// 数据源
	ListDataSources() ([]model.DataSource, error)
	GetDataSource(id string) (*model.DataSource, error)
	CreateDataSource(ds *model.DataSource) error
	UpdateDataSource(ds *model.DataSource) error
	DeleteDataSource(id string) error
	// ConnParamsFor 返回解密后的真实连接参数（仅供适配器建连使用，切勿直接返回给 API）
	ConnParamsFor(id string) (map[string]any, error)

	// 通道
	ListChannelsBySource(dataSourceID string) ([]model.Channel, error)
	ListAllChannels() ([]model.Channel, error)
	GetChannel(id string) (*model.Channel, error)
	CreateChannel(ch *model.Channel) error
	UpdateChannel(ch *model.Channel) error
	DeleteChannel(id string) error
	DeleteChannelsBySource(dataSourceID string) error

	// 看板
	ListDashboards() ([]model.Dashboard, error)
	GetDashboard(id string) (*model.Dashboard, error)
	CreateDashboard(d *model.Dashboard) error
	UpdateDashboard(d *model.Dashboard) error
	DeleteDashboard(id string) error

	// 离线导入文件（离线 JSON 回放）
	ListImports() ([]model.Import, error)
	GetImport(id string) (*model.Import, error)
	CreateImport(im *model.Import) error
	DeleteImport(id string) error

	// 卡片
	ListCards(dashboardID string) ([]model.Card, error)
	ListAllCards() ([]model.Card, error)
	GetCard(id string) (*model.Card, error)
	CreateCard(c *model.Card) error
	UpdateCard(c *model.Card) error
	DeleteCard(id string) error
	ReplaceCards(dashboardID string, cards []model.Card) error
	DeleteCardsByChannel(channelID string) error
}

// secretRow 为 secret_meta 表的一行（敏感字段密文）。
type secretRow struct {
	id    string
	nonce []byte
	ct    []byte
}

// Open 打开（必要时创建）SQLite 数据库并执行迁移。
func Open(sc config.StoreConfig, secretKeyFile string) (*SQLite, error) {
	if sc.DSN == "" {
		sc.DSN = "./data/monitorall.db"
	}
	dsn := sc.DSN
	if sc.WAL {
		dsn += "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "打开数据库失败")
	}
	// 单写者场景，限制连接池避免锁竞争
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	st := &SQLite{db: db, dsn: sc.DSN}
	if err := st.Ping(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if secretKeyFile != "" {
		cipher, err := crypto.NewCipher(secretKeyFile)
		if err != nil {
			_ = db.Close()
			return nil, apperr.Wrap(err, apperr.SecretDecryptFailed, "初始化敏感字段加密失败")
		}
		st.cipher = cipher
	}

	if err := st.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

// SQLite 是 Store 的 database/sql 实现。
type SQLite struct {
	db     *sql.DB
	dsn    string
	cipher *crypto.Cipher
}

// DSN 返回数据库路径（启动横幅与日志使用）。
func (s *SQLite) DSN() string { return s.dsn }

// Close 关闭数据库连接。
func (s *SQLite) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping 探活数据库。
func (s *SQLite) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return apperr.Wrap(err, apperr.StoreError, "数据库不可用")
	}
	return nil
}

// ————————————————— 事务封装 —————————————————

// tx 在事务中执行 fn，失败回滚；fn 返回 *apperr.AppError 时原样上抛，其它错误包装为 StoreError。
func (s *SQLite) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperr.Wrap(err, apperr.StoreError, "开启事务失败")
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		var ae *apperr.AppError
		if errors.As(err, &ae) {
			return err
		}
		return apperr.Wrap(err, apperr.StoreError, err.Error())
	}
	if err := t.Commit(); err != nil {
		return apperr.Wrap(err, apperr.StoreError, "提交事务失败")
	}
	return nil
}

// ————————————————— JSON 列辅助 —————————————————

// marshalJSON 序列化任意值为 JSON 文本。
func marshalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", apperr.Wrap(err, apperr.StoreError, "序列化 JSON 列失败")
	}
	return string(b), nil
}

// unmarshalJSON 把 JSON 文本反序列化到 v；空文本视为空值。
func unmarshalJSON(s string, v any) error {
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return apperr.Wrap(err, apperr.StoreError, "反序列化 JSON 列失败")
	}
	return nil
}

// nullableString 把可空列转成 string。
func nullableString(v sql.NullString) string { return v.String }

// ————————————————— 敏感字段：落盘加密 / 读取解密 —————————————————

// saveSecrets 把敏感字段明文逐条加密写入 secret_meta（主键 dsID:path）。
func (s *SQLite) saveSecrets(ctx context.Context, tx *sql.Tx, dsID string, secrets map[string]string) error {
	if s.cipher == nil || len(secrets) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO secret_meta (id, nonce, ciphertext) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET nonce = excluded.nonce, ciphertext = excluded.ciphertext`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for path, plain := range secrets {
		nonce, ct, err := s.cipher.Encrypt(plain)
		if err != nil {
			return err
		}
		id := dsID + ":" + path
		if _, err := stmt.ExecContext(ctx, id, nonce, ct); err != nil {
			return err
		}
	}
	return nil
}

// loadSecrets 读取并解密某数据源的全部敏感字段。
func (s *SQLite) loadSecrets(dsID string) (map[string]string, error) {
	out := make(map[string]string)
	if s.cipher == nil {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT id, nonce, ciphertext FROM secret_meta WHERE id LIKE ?`, dsID+":%")
	if err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "读取敏感字段失败")
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var nonce, ct []byte
		if err := rows.Scan(&id, &nonce, &ct); err != nil {
			return nil, apperr.Wrap(err, apperr.StoreError, "扫描敏感字段失败")
		}
		path := id[len(dsID)+1:]
		plain, err := s.cipher.Decrypt(nonce, ct)
		if err != nil {
			// 解密失败不上抛为 panic，转为 50011 由调用方决定表现
			return nil, apperr.Wrap(err, apperr.SecretDecryptFailed, fmt.Sprintf("字段 %s 解密失败", path))
		}
		out[path] = plain
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, apperr.StoreError, "遍历敏感字段失败")
	}
	return out, nil
}

// deleteSecrets 删除某数据源的全部敏感字段密文。
func (s *SQLite) deleteSecrets(ctx context.Context, tx *sql.Tx, dsID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM secret_meta WHERE id LIKE ?`, dsID+":%")
	return err
}

// ————————————————— 默认看板（D8） —————————————————

// EnsureDefaultDashboard 在没有任何看板时自动创建一个「默认看板」。
func EnsureDefaultDashboard(s Store) error {
	list, err := s.ListDashboards()
	if err != nil {
		return err
	}
	if len(list) > 0 {
		return nil
	}
	return s.CreateDashboard(model.NewDashboard(config.DefaultDashboardName))
}
