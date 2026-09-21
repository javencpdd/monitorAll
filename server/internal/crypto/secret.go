// Package crypto 提供敏感字段的落盘加密（AES-256-GCM）、密钥文件管理以及
// 「出参脱敏 *** / 入参 *** 保持原值」的双向处理（架构 §12.4）。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/monitorall/monitorall/internal/apperr"
	"github.com/monitorall/monitorall/internal/model"
)

// ————————————————— 常量 —————————————————

// MaskedValue 为敏感字段在 API 出参中的脱敏占位符。
const MaskedValue = "***"

// KeyLen 为 AES-256 密钥长度（字节）。
const KeyLen = 32

// NonceLen 为 GCM 建议 nonce 长度（字节）。
const NonceLen = 12

// keyFilePerm 为密钥文件权限（仅属主可读写）。
const keyFilePerm = 0o600

// pathSeparator 为 connParams 内嵌字段路径的分隔符，如 headers.Authorization。
const pathSeparator = "."

// IsMasked 判断一个值是否为脱敏占位符。
func IsMasked(v any) bool {
	s, ok := v.(string)
	return ok && s == MaskedValue
}

// Cipher 封装 AES-256-GCM 加解密能力。
type Cipher struct {
	key  []byte
	path string
}

// NewCipher 创建 Cipher：密钥文件不存在则生成（0600），存在则加载。
func NewCipher(path string) (*Cipher, error) {
	if path == "" {
		return nil, errors.New("密钥文件路径为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建密钥目录失败: %w", err)
		}
	}
	key, err := loadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	return &Cipher{key: key, path: path}, nil
}

// Path 返回密钥文件路径。
func (c *Cipher) Path() string { return c.path }

// loadOrCreateKey 读取或生成 32 字节随机密钥。
func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == KeyLen {
			return data, nil
		}
		if len(data) == KeyLen*2 { // 兼容 hex 文本格式
			key := make([]byte, KeyLen)
			if _, err := hexDecode(data, key); err == nil {
				return key, nil
			}
		}
		return nil, fmt.Errorf("密钥文件 %s 格式非法（长度 %d）", path, len(data))
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取密钥文件失败: %w", err)
	}

	key := make([]byte, KeyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("生成随机密钥失败: %w", err)
	}
	if err := os.WriteFile(path, key, keyFilePerm); err != nil {
		return nil, fmt.Errorf("写入密钥文件失败: %w", err)
	}
	return key, nil
}

// hexDecode 把 hex 文本解码到 dst（避免引入 encoding/hex 之外无谓依赖的轻量实现）。
func hexDecode(src []byte, dst []byte) (int, error) {
	s := strings.TrimSpace(string(src))
	if len(s) != KeyLen*2 {
		return 0, errors.New("hex 长度不匹配")
	}
	for i := 0; i < KeyLen; i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			return 0, errors.New("非法 hex 字符")
		}
		dst[i] = hi<<4 | lo
	}
	return KeyLen, nil
}

// hexVal 返回单个 hex 字符的数值。
func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// Encrypt 把明文加密为 nonce + 密文。
func (c *Cipher) Encrypt(plain string) (nonce []byte, ciphertext []byte, err error) {
	if c == nil {
		return nil, nil, errors.New("cipher 未初始化")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ct := gcm.Seal(nil, nonce, []byte(plain), nil)
	return nonce, ct, nil
}

// Decrypt 解密；失败时返回 apperr.SecretDecryptFailed（密钥丢失场景）。
func (c *Cipher) Decrypt(nonce []byte, ciphertext []byte) (string, error) {
	if c == nil {
		return "", apperr.New(apperr.SecretDecryptFailed, "cipher 未初始化")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", apperr.Wrap(err, apperr.SecretDecryptFailed, "构造 AES 失败")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", apperr.Wrap(err, apperr.SecretDecryptFailed, "构造 GCM 失败")
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", apperr.Wrap(err, apperr.SecretDecryptFailed, "敏感字段解密失败，请重新录入凭据")
	}
	return string(plain), nil
}

// ————————————————— 敏感键声明与脱敏处理 —————————————————

// DefaultSecretKeys 返回各 kind 的默认敏感键路径（架构 §12.4）。
func DefaultSecretKeys(kind model.DataSourceKind) []string {
	switch kind {
	case model.KindHTTP:
		return []string{"headers.Authorization", "headers.authorization", "headers.X-Token", "password", "token"}
	case model.KindROS:
		return []string{"password", "token", "secret"}
	case model.KindVideo:
		return []string{"password", "token"}
	default:
		return []string{}
	}
}

// GetByPath 按点分路径从 map 中取值（支持一层嵌套，如 headers.Authorization）。
func GetByPath(params map[string]any, path string) (any, bool) {
	if params == nil || path == "" {
		return nil, false
	}
	segs := strings.Split(path, pathSeparator)
	cur := any(params)
	for i, seg := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		if i == len(segs)-1 {
			return v, true
		}
		cur = v
	}
	return nil, false
}

// SetByPath 按点分路径写值到 map（中间节点缺失时自动创建 map[string]any）。
func SetByPath(params map[string]any, path string, value any) bool {
	if params == nil || path == "" {
		return false
	}
	segs := strings.Split(path, pathSeparator)
	cur := params
	for i, seg := range segs {
		if i == len(segs)-1 {
			cur[seg] = value
			return true
		}
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	return false
}

// DeepCopyParams 深拷贝连接参数（避免修改入参影响调用方）。
func DeepCopyParams(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		switch val := v.(type) {
		case map[string]any:
			out[k] = DeepCopyParams(val)
		case map[string]string:
			cp := make(map[string]string, len(val))
			for kk, vv := range val {
				cp[kk] = vv
			}
			out[k] = cp
		case []any:
			cp := make([]any, len(val))
			copy(cp, val)
			out[k] = cp
		case []string:
			cp := make([]string, len(val))
			copy(cp, val)
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}

// MaskConnParams 返回一份脱敏副本：所有敏感键的值替换为 "***"。
func MaskConnParams(params map[string]any, secretKeys []string) map[string]any {
	out := DeepCopyParams(params)
	for _, key := range secretKeys {
		if _, ok := GetByPath(out, key); ok {
			SetByPath(out, key, MaskedValue)
		}
	}
	return out
}

// CollectSecrets 摘出所有敏感键的明文（path → 明文字符串），供落盘加密。
// 值为 MaskedValue 的条目会被跳过（表示未变更）。
func CollectSecrets(params map[string]any, secretKeys []string) map[string]string {
	out := make(map[string]string)
	for _, key := range secretKeys {
		v, ok := GetByPath(params, key)
		if !ok {
			continue
		}
		if IsMasked(v) {
			continue
		}
		if s, ok := v.(string); ok {
			out[key] = s
			continue
		}
		out[key] = fmt.Sprintf("%v", v)
	}
	return out
}

// ApplySecrets 把明文敏感值写回参数副本（用于适配器实际建连时）。
func ApplySecrets(params map[string]any, secrets map[string]string) map[string]any {
	out := DeepCopyParams(params)
	for path, val := range secrets {
		SetByPath(out, path, val)
	}
	return out
}

// UnmaskConnParams 处理「入参 *** 表示保持原值」的约定（TASKS §1.10）：
// incoming 中敏感键为 *** 时，用 storedParams 中已解密的真实值回填。
func UnmaskConnParams(storedParams map[string]any, incoming map[string]any, secretKeys []string) map[string]any {
	out := DeepCopyParams(incoming)
	if len(secretKeys) == 0 {
		return out
	}
	for _, key := range secretKeys {
		v, ok := GetByPath(out, key)
		if !ok {
			// 入参未给该键：保持库里的原值
			if sv, sok := GetByPath(storedParams, key); sok {
				SetByPath(out, key, sv)
			}
			continue
		}
		if IsMasked(v) {
			if sv, sok := GetByPath(storedParams, key); sok {
				SetByPath(out, key, sv)
			}
		}
	}
	return out
}

// MergeSecretKeys 合并用户声明与默认敏感键，去重后返回。
func MergeSecretKeys(declared []string, kind model.DataSourceKind) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(declared)+4)
	for _, k := range append(append([]string{}, declared...), DefaultSecretKeys(kind)...) {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}
