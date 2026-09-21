package crypto

import (
	"path/filepath"
	"testing"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// TestCipherRoundTrip 校验加解密往返一致与密钥文件权限。
func TestCipherRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "secret.key")
	c, err := NewCipher(keyFile)
	if err != nil {
		t.Fatalf("创建 Cipher 失败: %v", err)
	}
	plain := "Bearer abc123"
	nonce, ct, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	got, err := c.Decrypt(nonce, ct)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != plain {
		t.Fatalf("往返不一致: got %q want %q", got, plain)
	}
	// 二次加载同一密钥文件应能解密
	c2, err := NewCipher(keyFile)
	if err != nil {
		t.Fatalf("二次加载密钥失败: %v", err)
	}
	if got2, err := c2.Decrypt(nonce, ct); err != nil || got2 != plain {
		t.Fatalf("二次加载后解密失败: %v %q", err, got2)
	}
}

// TestDecryptFailedCode 校验密钥丢失/损坏时返回 50011 而非 panic。
func TestDecryptFailedCode(t *testing.T) {
	dir := t.TempDir()
	c1, err := NewCipher(filepath.Join(dir, "k1"))
	if err != nil {
		t.Fatalf("创建 Cipher 失败: %v", err)
	}
	nonce, ct, err := c1.Encrypt("secret-value")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	c2, err := NewCipher(filepath.Join(dir, "k2")) // 另一把密钥
	if err != nil {
		t.Fatalf("创建 Cipher 失败: %v", err)
	}
	if _, err := c2.Decrypt(nonce, ct); err == nil {
		t.Fatal("用错误密钥解密应失败")
	} else if apperr.CodeOf(err) != apperr.SecretDecryptFailed {
		t.Fatalf("错误码应为 50011，实际 %d", apperr.CodeOf(err))
	}
}

// TestMaskAndUnmask 校验出参脱敏为 *** 与入参 *** 保持原值（TASKS §1.10）。
func TestMaskAndUnmask(t *testing.T) {
	keys := DefaultSecretKeys(model.KindHTTP)
	params := map[string]any{
		"url":     "http://172.31.68.9:8080/api/stat",
		"headers": map[string]any{"Authorization": "Bearer real-token"},
	}
	masked := MaskConnParams(params, keys)
	got, ok := GetByPath(masked, "headers.Authorization")
	if !ok || got != MaskedValue {
		t.Fatalf("敏感字段未脱敏: %v", got)
	}
	if masked["url"] != "http://172.31.68.9:8080/api/stat" {
		t.Fatal("非敏感字段不应被修改")
	}

	// 入参为 *** 时保持库中原值
	stored := map[string]any{"headers": map[string]any{"Authorization": "Bearer real-token"}}
	incoming := map[string]any{"headers": map[string]any{"Authorization": MaskedValue}}
	merged := UnmaskConnParams(stored, incoming, keys)
	v, _ := GetByPath(merged, "headers.Authorization")
	if v != "Bearer real-token" {
		t.Fatalf("*** 未回填原值: %v", v)
	}
}

// TestCollectAndApplySecrets 校验敏感字段摘出与回写。
func TestCollectAndApplySecrets(t *testing.T) {
	keys := []string{"password", "headers.Authorization"}
	params := map[string]any{
		"password": "p1",
		"headers":  map[string]any{"Authorization": "Bearer x"},
		"url":      "http://x",
	}
	secrets := CollectSecrets(params, keys)
	if len(secrets) != 2 || secrets["password"] != "p1" {
		t.Fatalf("敏感字段摘出失败: %+v", secrets)
	}
	masked := MaskConnParams(params, keys)
	applied := ApplySecrets(masked, secrets)
	v, _ := GetByPath(applied, "headers.Authorization")
	if v != "Bearer x" {
		t.Fatalf("回写失败: %v", v)
	}
	// 脱敏值不应被摘出（表示未变更）
	if s := CollectSecrets(masked, keys); len(s) != 0 {
		t.Fatalf("脱敏值不应被摘出: %+v", s)
	}
}

// TestIsMasked 校验脱敏判定。
func TestIsMasked(t *testing.T) {
	if !IsMasked(MaskedValue) {
		t.Fatal("*** 应判定为脱敏")
	}
	if IsMasked("abc") || IsMasked(123) {
		t.Fatal("非脱敏值判定错误")
	}
}

// TestDefaultSecretKeys 校验各类数据源的默认敏感键声明。
func TestDefaultSecretKeys(t *testing.T) {
	if len(DefaultSecretKeys(model.KindHTTP)) == 0 {
		t.Fatal("HTTP 应声明 Authorization 等敏感键")
	}
	if len(DefaultSecretKeys(model.KindVideo)) == 0 {
		t.Fatal("视频应声明敏感键")
	}
	merged := MergeSecretKeys([]string{"custom"}, model.KindHTTP)
	if merged[0] != "custom" {
		t.Fatalf("用户声明的敏感键应优先: %+v", merged)
	}
}
