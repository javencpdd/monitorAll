package apperr

import (
	"errors"
	"net/http"
	"testing"
)

// TestHTTPStatusMapping 校验 16 个错误码与 HTTP 状态码的映射（架构 §7.2）。
func TestHTTPStatusMapping(t *testing.T) {
	cases := map[Code]int{
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
	for code, status := range cases {
		if got := HTTPStatus(code); got != status {
			t.Fatalf("错误码 %d 的 HTTP 状态错误: got %d want %d", code, got, status)
		}
	}
	if len(httpStatusMap) != 16 {
		t.Fatalf("错误码表应含 16 项，实际 %d", len(httpStatusMap))
	}
}

// TestCodeValues 校验错误码数值与文档一致。
func TestCodeValues(t *testing.T) {
	if OK != 0 || BadRequest != 40000 || NotFound != 40004 || RevisionConflict != 40009 ||
		SourceConnectFailed != 50001 || SampleTimeout != 50004 || ChannelUnavailable != 50301 || Internal != 50900 {
		t.Fatal("错误码常量值与文档不一致")
	}
}

// TestWrapAndUnwrap 校验错误包装与 errors.As 语义。
func TestWrapAndUnwrap(t *testing.T) {
	base := errors.New("dial tcp 172.31.68.227:9090: connection refused")
	e := Wrap(base, SourceConnectFailed, "连接 rosbridge 失败")
	if !errors.Is(e, base) {
		t.Fatal("Wrap 后应可用 errors.Is 找到底层错误")
	}
	if e.Code != SourceConnectFailed {
		t.Fatalf("错误码错误: %d", e.Code)
	}
	if e.HTTPStatus() != http.StatusBadGateway {
		t.Fatalf("HTTP 状态错误: %d", e.HTTPStatus())
	}
}

// TestFromError 校验任意 error → AppError 的兜底（非 AppError 归为 Internal）。
func TestFromError(t *testing.T) {
	if FromError(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
	if CodeOf(errors.New("boom")) != Internal {
		t.Fatal("未知错误应归为 Internal")
	}
	if CodeOf(New(NotFound, "")) != NotFound {
		t.Fatal("AppError 应保留原错误码")
	}
}

// TestDefaultMessage 校验默认文案回填。
func TestDefaultMessage(t *testing.T) {
	e := New(NotFound, "")
	if e.Message != "not found" {
		t.Fatalf("默认文案回填失败: %s", e.Message)
	}
	e2 := Newf(InvalidParam, "字段 %s 非法", "rtmpUrl")
	if e2.Message != "字段 rtmpUrl 非法" {
		t.Fatalf("格式化文案错误: %s", e2.Message)
	}
}
