// Package apperr 定义全局统一错误码、AppError 类型以及错误码到 HTTP 状态码的映射。
// 约定：任何返回给 REST 的错误都必须经 apperr 包装，禁止直接把底层 error 暴露给前端。
package apperr

import (
	"fmt"
	"net/http"
	"strings"
)

// Code 为业务错误码，与架构文档 §7.2 错误码表逐条对应。
type Code int

// 全部 16 个错误码（含 OK）。
const (
	// OK 表示成功。
	OK Code = 0

	// BadRequest 报文 / JSON 解析失败。
	BadRequest Code = 40000
	// InvalidParam 参数校验失败（可带字段级 detail）。
	InvalidParam Code = 40001
	// InvalidURL RTMP / WS / HTTP URL 非法。
	InvalidURL Code = 40002
	// UnsupportedType 不支持的 kind / protocol / renderer。
	UnsupportedType Code = 40003
	// NotFound 资源不存在。
	NotFound Code = 40004
	// RevisionConflict 看板 revision 不匹配（并发覆盖）。
	RevisionConflict Code = 40009
	// RendererIncompatible 渲染器不接受该 payloadType。
	RendererIncompatible Code = 40010

	// SourceConnectFailed 数据源连接失败（测试连接）。
	SourceConnectFailed Code = 50001
	// AdapterError 适配器内部错误。
	AdapterError Code = 50002
	// MediaMTXUnreachable MediaMTX API 不可达。
	MediaMTXUnreachable Code = 50003
	// SampleTimeout 样例帧超时（3s 内无数据）。
	SampleTimeout Code = 50004
	// StoreError 持久化失败。
	StoreError Code = 50010
	// SecretDecryptFailed 敏感字段解密失败（密钥丢失）。
	SecretDecryptFailed Code = 50011

	// ChannelUnavailable 通道暂不可用。
	ChannelUnavailable Code = 50301

	// Internal 未分类内部错误。
	Internal Code = 50900
)

// httpStatusMap 错误码 → HTTP 状态码。
var httpStatusMap = map[Code]int{
	OK:                    http.StatusOK,
	BadRequest:            http.StatusBadRequest,
	InvalidParam:          http.StatusBadRequest,
	InvalidURL:            http.StatusBadRequest,
	UnsupportedType:       http.StatusBadRequest,
	NotFound:              http.StatusNotFound,
	RevisionConflict:      http.StatusConflict,
	RendererIncompatible:  http.StatusBadRequest,
	SourceConnectFailed:   http.StatusBadGateway,
	AdapterError:          http.StatusInternalServerError,
	MediaMTXUnreachable:   http.StatusBadGateway,
	SampleTimeout:         http.StatusGatewayTimeout,
	StoreError:            http.StatusInternalServerError,
	SecretDecryptFailed:   http.StatusInternalServerError,
	ChannelUnavailable:    http.StatusServiceUnavailable,
	Internal:              http.StatusInternalServerError,
}

// messageMap 错误码默认文案（未显式给 message 时使用）。
var messageMap = map[Code]string{
	OK:                   "ok",
	BadRequest:           "bad request",
	InvalidParam:         "invalid param",
	InvalidURL:           "invalid url",
	UnsupportedType:      "unsupported type",
	NotFound:             "not found",
	RevisionConflict:     "revision conflict",
	RendererIncompatible: "renderer incompatible",
	SourceConnectFailed:  "source connect failed",
	AdapterError:         "adapter error",
	MediaMTXUnreachable:  "mediamtx unreachable",
	SampleTimeout:        "sample timeout",
	StoreError:           "store error",
	SecretDecryptFailed:  "secret decrypt failed",
	ChannelUnavailable:   "channel unavailable",
	Internal:             "internal error",
}

// AppError 是全局统一错误类型。
type AppError struct {
	// Code 为业务错误码。
	Code Code `json:"code"`
	// Message 为面向用户的可读文案。
	Message string `json:"message"`
	// Detail 为可选的字段级明细（表单红字用）。
	Detail any `json:"detail,omitempty"`
	// Err 为底层错误，不序列化给前端。
	Err error `json:"-"`
}

// New 构造一个 AppError；msg 为空时使用错误码默认文案。
func New(c Code, msg string) *AppError {
	if strings.TrimSpace(msg) == "" {
		msg = DefaultMessage(c)
	}
	return &AppError{Code: c, Message: msg}
}

// Newf 带格式化参数地构造 AppError。
func Newf(c Code, format string, args ...any) *AppError {
	return New(c, fmt.Sprintf(format, args...))
}

// Wrap 用错误码包装底层 error。
func Wrap(err error, c Code, msg string) *AppError {
	e := New(c, msg)
	e.Err = err
	return e
}

// WithDetail 追加字段级明细，返回自身以便链式调用。
func (e *AppError) WithDetail(detail any) *AppError {
	e.Detail = detail
	return e
}

// Error 实现 error 接口。
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap 返回底层错误，便于 errors.Is / errors.As。
func (e *AppError) Unwrap() error { return e.Err }

// HTTPStatus 返回该错误对应的 HTTP 状态码。
func (e *AppError) HTTPStatus() int { return HTTPStatus(e.Code) }

// HTTPStatus 返回错误码对应的 HTTP 状态码，未知码回落 500。
func HTTPStatus(c Code) int {
	if s, ok := httpStatusMap[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// DefaultMessage 返回错误码默认文案。
func DefaultMessage(c Code) string {
	if m, ok := messageMap[c]; ok {
		return m
	}
	return messageMap[Internal]
}

// FromError 把任意 error 转成 *AppError：已是 AppError 则原样返回，否则包装为 Internal。
func FromError(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if ok := asAppError(err, &ae); ok {
		return ae
	}
	return Wrap(err, Internal, err.Error())
}

// asAppError 使用 errors.As 语义但避免引入额外依赖的手写展开。
func asAppError(err error, target **AppError) bool {
	for err != nil {
		if ae, ok := err.(*AppError); ok {
			*target = ae
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// CodeOf 返回任意 error 对应的业务错误码，非 AppError 返回 Internal。
func CodeOf(err error) Code {
	if err == nil {
		return OK
	}
	if ae := FromError(err); ae != nil {
		return ae.Code
	}
	return Internal
}
