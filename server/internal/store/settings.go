package store

import (
	"context"
	"database/sql"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 全局设置（app_settings，v4） —————————————————
//
// 表内每个 key 存一个 JSON blob（目前只有 branding / weather）。
// 契约上的两个刻意选择：
//   - GetSetting 读不到时返回空串【且不报错】—— 设置项从来没写过是正常状态，
//     不是异常。报错会让上层被迫写"忽略 NotFound"的分支，一旦写错就把
//     "没配过" 和 "库坏了" 混为一谈。
//   - SetSetting 是 upsert（ON CONFLICT DO UPDATE）—— 设置项全局只有一份，
//     让调用方先判存在再决定 INSERT/UPDATE 会引入 TOCTOU 竞态。

// GetSetting 读取一个设置项的 JSON 文本；不存在返回空串且不报错。
func (s *SQLite) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", apperr.Wrap(err, apperr.StoreError, "读取全局设置失败")
	}
	return v, nil
}

// SetSetting 写入（或覆盖）一个设置项的 JSON 文本。
func (s *SQLite) SetSetting(ctx context.Context, key, value string) error {
	ctx2 := ctx
	if ctx2 == nil {
		ctx2 = context.Background()
	}
	return s.tx(ctx2, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx2, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, model.NowMs())
		return err
	})
}
