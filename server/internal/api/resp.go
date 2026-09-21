// Package api 实现控制面 REST（Gin）：统一响应封装、中间件、数据源/通道、
// 看板/卡片、MediaMTX 查询与系统接口（架构 §7）。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/monitorall/monitorall/internal/apperr"
)

// Response 为全局统一响应体（{code,message,data} + 可选 traceId）。
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
	TraceID string `json:"traceId,omitempty"`
}

// OK 返回成功响应（data 永远存在，无内容时为 null）。
func OK(c *gin.Context, data any) {
	if data == nil {
		data = map[string]any{}
	}
	c.JSON(http.StatusOK, Response{
		Code:    int(apperr.OK),
		Message: apperr.DefaultMessage(apperr.OK),
		Data:    data,
		TraceID: traceIDOf(c),
	})
}

// Created 返回 201 成功响应。
func Created(c *gin.Context, data any) {
	if data == nil {
		data = map[string]any{}
	}
	c.JSON(http.StatusCreated, Response{
		Code:    int(apperr.OK),
		Message: apperr.DefaultMessage(apperr.OK),
		Data:    data,
		TraceID: traceIDOf(c),
	})
}

// Fail 统一渲染错误：HTTP status 与 code 一致，data 为 null。
func Fail(c *gin.Context, err error) {
	ae := apperr.FromError(err)
	if ae == nil {
		ae = apperr.New(apperr.Internal, "unknown error")
	}
	c.JSON(ae.HTTPStatus(), Response{
		Code:    int(ae.Code),
		Message: ae.Message,
		Data:    nil,
		TraceID: traceIDOf(c),
	})
}

// FailCode 直接用错误码 + 文案渲染（detail 携带字段级信息时通过 WithDetail 追加）。
func FailCode(c *gin.Context, code apperr.Code, msg string) {
	Fail(c, apperr.New(code, msg))
}

// traceIDOf 从 gin 上下文取出请求 ID（由 mw 写入）。
func traceIDOf(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
