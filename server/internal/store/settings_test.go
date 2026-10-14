package store

import (
	"context"
	"testing"
)

// TestAppSettingsTableCreated 校验 v4 迁移建出 app_settings 表（迁移链已含该条）。
func TestAppSettingsTableCreated(t *testing.T) {
	st := newTestStore(t)
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='app_settings'`).Scan(&n); err != nil {
		t.Fatalf("查询 app_settings 失败: %v", err)
	}
	if n != 1 {
		t.Fatal("v4 迁移未创建 app_settings 表")
	}
}

// TestGetSettingMissingReturnsEmpty 校验"从没写过"是正常状态：返回空串且不报错。
//
// 如果这里返回 NotFound，上层每个读取点都要写"忽略 NotFound"分支，
// 一旦写错就把"没配过"和"库坏了"混为一谈。
func TestGetSettingMissingReturnsEmpty(t *testing.T) {
	st := newTestStore(t)
	got, err := st.GetSetting(context.Background(), "branding")
	if err != nil {
		t.Fatalf("读取不存在的设置项不应报错，实际: %v", err)
	}
	if got != "" {
		t.Fatalf("不存在的设置项应返回空串，实际 %q", got)
	}
}

// TestSetSettingUpsert 校验写入与覆盖（第二次写必须更新 updated_at）。
func TestSetSettingUpsert(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.SetSetting(ctx, "branding", `{"title":"A"}`); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	got, err := st.GetSetting(ctx, "branding")
	if err != nil || got != `{"title":"A"}` {
		t.Fatalf("首次写入后读取失败: got %q err %v", got, err)
	}
	var ts1 int64
	if err := st.db.QueryRow(`SELECT updated_at FROM app_settings WHERE key='branding'`).Scan(&ts1); err != nil {
		t.Fatalf("读取 updated_at 失败: %v", err)
	}
	if ts1 == 0 {
		t.Fatal("updated_at 未写入（排障时会误以为这项设置从没被改过）")
	}

	if err := st.SetSetting(ctx, "branding", `{"title":"B"}`); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}
	got, err = st.GetSetting(ctx, "branding")
	if err != nil || got != `{"title":"B"}` {
		t.Fatalf("覆盖后读取失败: got %q err %v", got, err)
	}
	var ts2 int64
	if err := st.db.QueryRow(`SELECT updated_at FROM app_settings WHERE key='branding'`).Scan(&ts2); err != nil {
		t.Fatalf("读取 updated_at 失败: %v", err)
	}
	if ts2 < ts1 {
		t.Fatalf("覆盖后 updated_at 未推进: %d -> %d", ts1, ts2)
	}

	// 两个 key 互不干扰
	if err := st.SetSetting(ctx, "weather", `{"enabled":true}`); err != nil {
		t.Fatalf("写入 weather 失败: %v", err)
	}
	if v, _ := st.GetSetting(ctx, "branding"); v != `{"title":"B"}` {
		t.Fatalf("写 weather 影响了 branding: %q", v)
	}
	var cnt int
	if err := st.db.QueryRow(`SELECT count(*) FROM app_settings`).Scan(&cnt); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if cnt != 2 {
		t.Fatalf("应只有 2 个设置项，实际 %d", cnt)
	}
}
