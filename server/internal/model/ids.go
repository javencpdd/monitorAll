// Package model 定义 MonitorAll 的全部领域模型：枚举、数据源/通道、统一 Frame 契约、
// 各 payloadType 的 payload 结构以及看板/卡片。所有字段的 json tag 与架构文档 §4/§5
// 逐字一致，任何修改都必须同步前端类型定义。
package model

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// ————————————————— ID 生成：唯一生成处，禁止其它文件自己拼 ID —————————————————

// ID 前缀常量。
const (
	PrefixDataSource = "ds_"
	PrefixChannel    = "ch_"
	PrefixCard       = "cd_"
	PrefixDashboard  = "db_"
	PrefixTemp       = "tmp_"
	PrefixImport     = "im_"
)

// IDRandomLen 为随机部分长度（base36 字符数）。
const IDRandomLen = 16

// base36 字符集。
const base36Chars = "0123456789abcdefghijklmnopqrstuvwxyz"

// newID 生成 <前缀><16 位 base36 随机串>。
func newID(prefix string) string {
	var sb strings.Builder
	sb.WriteString(prefix)
	max := big.NewInt(int64(len(base36Chars)))
	for i := 0; i < IDRandomLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand 不可用时退化为时间戳低位，保证不 panic
			sb.WriteByte(base36Chars[0])
			continue
		}
		sb.WriteByte(base36Chars[n.Int64()])
	}
	return sb.String()
}

// NewDataSourceID 生成数据源 ID（ds_ 前缀）。
func NewDataSourceID() string { return newID(PrefixDataSource) }

// NewChannelID 生成通道 ID（ch_ 前缀）。
func NewChannelID() string { return newID(PrefixChannel) }

// NewCardID 生成卡片 ID（cd_ 前缀）。
func NewCardID() string { return newID(PrefixCard) }

// NewDashboardID 生成看板 ID（db_ 前缀）。
func NewDashboardID() string { return newID(PrefixDashboard) }

// NewTempID 生成临时通道 ID（tmp_ 前缀，仅用于测试连接等不落库场景）。
func NewTempID() string { return newID(PrefixTemp) }

// NewImportID 生成导入文件 ID（im_ 前缀）。
func NewImportID() string { return newID(PrefixImport) }

// ————————————————— 通用工具 —————————————————

// NowMs 返回当前毫秒时间戳（全项目时间字段统一 int64 ms）。
func NowMs() int64 { return timeNowMs() }

// EncodeJSON 把任意值编码为 JSON（用于持久化 blob 列）。
func EncodeJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseInt64 把 any 里的数字统一转成 int64（JSON 反序列化后数字为 float64）。
func ParseInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

// ParseFloat64 把 any 里的数字统一转成 float64。
func ParseFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}
