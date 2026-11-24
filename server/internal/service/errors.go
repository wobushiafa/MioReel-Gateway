package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"mioreel-gateway/server/internal/resolver"
	"mioreel-gateway/server/internal/signer"
	"mioreel-gateway/server/internal/upstream"
)

// GatewayError represents a structured API error mapped to docs/05-error-codes.md.
type GatewayError struct {
	Code          int
	HTTPStatus    int
	Message       string
	UpstreamCode  string
	RetryAfterSec int
}

func (e *GatewayError) Error() string {
	return e.Message
}

func NewBadRequest(msg string) *GatewayError {
	return &GatewayError{Code: 40001, HTTPStatus: http.StatusOK, Message: msg}
}

func NewUnauthorized(msg string) *GatewayError {
	return &GatewayError{Code: 40101, HTTPStatus: http.StatusUnauthorized, Message: msg}
}

func NewForbidden(msg string) *GatewayError {
	return &GatewayError{Code: 40301, HTTPStatus: http.StatusForbidden, Message: msg}
}

func NewNotFound(msg, upstreamCode string) *GatewayError {
	return &GatewayError{Code: 40401, HTTPStatus: http.StatusOK, Message: msg, UpstreamCode: upstreamCode}
}

func NewRateLimited(retryAfterSec int) *GatewayError {
	if retryAfterSec <= 0 {
		retryAfterSec = 2
	}
	return &GatewayError{
		Code:          42901,
		HTTPStatus:    http.StatusOK,
		Message:       "请求过于频繁，请稍后重试",
		RetryAfterSec: retryAfterSec,
	}
}

// MapError translates internal/upstream/domain errors into canonical gateway error codes.
func MapError(err error) *GatewayError {
	if err == nil {
		return nil
	}
	var ge *GatewayError
	if errors.As(err, &ge) {
		return ge
	}

	var se *signer.SigningError
	if errors.As(err, &se) {
		// Never expose internal signing material details externally.
		return &GatewayError{
			Code:       50001,
			HTTPStatus: http.StatusOK,
			Message:    "服务端签名异常，请上报 request_id",
		}
	}
	if errors.Is(err, signer.ErrMissingConfiguration) {
		return &GatewayError{
			Code:       50001,
			HTTPStatus: http.StatusOK,
			Message:    "服务端签名配置缺失",
		}
	}

	var de *resolver.DomainError
	if errors.As(err, &de) {
		return mapCodeString(de.Code, de.Message, false)
	}

	var ue *upstream.UpstreamError
	if errors.As(err, &ue) {
		return mapCodeString(ue.Code, ue.Message, ue.RoutesExhausted)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return &GatewayError{
			Code:         50401,
			HTTPStatus:   http.StatusOK,
			Message:      "解析超时 (upstream=ETIMEDOUT)",
			UpstreamCode: "ETIMEDOUT",
		}
	}

	return &GatewayError{
		Code:       50001,
		HTTPStatus: http.StatusOK,
		Message:    "服务端内部错误",
	}
}

func mapCodeString(upCode, detail string, routesExhausted bool) *GatewayError {
	switch upCode {
	case "MISSING_EPISODE":
		return &GatewayError{
			Code:         40401,
			HTTPStatus:   http.StatusOK,
			Message:      fmt.Sprintf("剧集或分集不存在 (upstream=%s)", upCode),
			UpstreamCode: upCode,
		}
	case "DETAIL_REJECTED", "BUSINESS_REJECTED", "SECURITY_BLOCKED", "HTTP_403":
		return &GatewayError{
			Code:         40901,
			HTTPStatus:   http.StatusOK,
			Message:      fmt.Sprintf("上游拒绝请求: %s (upstream=%s)", detail, upCode),
			UpstreamCode: upCode,
		}
	case "MISSING_MODEL", "NO_STREAM", "MEDIA_REJECTED", "MEDIA_TYPE_INVALID", "MEDIA_URL_EXPIRED":
		return &GatewayError{
			Code:         42201,
			HTTPStatus:   http.StatusOK,
			Message:      fmt.Sprintf("该集暂无兼容片源: %s (upstream=%s)", detail, upCode),
			UpstreamCode: upCode,
		}
	case "HTTP_429":
		return &GatewayError{
			Code:          42901,
			HTTPStatus:    http.StatusOK,
			Message:       "上游触发限流 (upstream=HTTP_429)",
			UpstreamCode:  upCode,
			RetryAfterSec: 2,
		}
	case "ETIMEDOUT", "TIMEOUT":
		return &GatewayError{
			Code:         50401,
			HTTPStatus:   http.StatusOK,
			Message:      fmt.Sprintf("解析超时 (upstream=%s)", upCode),
			UpstreamCode: upCode,
		}
	case "CANCELLED":
		return &GatewayError{
			Code:         50401,
			HTTPStatus:   http.StatusOK,
			Message:      "请求已取消或超时 (upstream=CANCELLED)",
			UpstreamCode: upCode,
		}
	default:
		if strings.HasPrefix(upCode, "MEDIA_HTTP_") {
			return &GatewayError{
				Code:         42201,
				HTTPStatus:   http.StatusOK,
				Message:      fmt.Sprintf("候选直链探活失败 (upstream=%s)", upCode),
				UpstreamCode: upCode,
			}
		}
		if routesExhausted {
			return &GatewayError{
				Code:         50301,
				HTTPStatus:   http.StatusOK,
				Message:      fmt.Sprintf("上游视频模型接口全部路由失败: routesExhausted, last=%s", upCode),
				UpstreamCode: upCode,
			}
		}
		return &GatewayError{
			Code:         50201,
			HTTPStatus:   http.StatusOK,
			Message:      fmt.Sprintf("上游网络或响应异常 (upstream=%s)", upCode),
			UpstreamCode: upCode,
		}
	}
}
