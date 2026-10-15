// Package httperr 把内部 *errors.AppError 投影为 contracts/02 §4 规定的
// HTTP 错误响应结构：{ "error": { code, base_code, message, retryable, details },
// "request_id": ... }。
//
// 职责边界（唯一真源）：
//   - **错误码注册表与 base_code 映射的唯一真源是 internal/platform/errors**
//     （其 baseSpecs 逐条落地 contracts/02 §2 的 12 个基础码，domainSpecs 落地 §3 域码）。
//     本包不再自带映射表——此前本包曾另持一张映射表，并与 platform/errors 发生漂移
//     （PLUGIN_MANIFEST_INVALID / ERP_RECEIPT_MISMATCH 两处 base_code 不一致），
//     故收敛为纯投影，杜绝"两张表各说各话"。
//   - HTTP 状态与 retryable 一律取自**基础码**（contracts/02 §2 为基础码定义这两个语义），
//     由 errors.Projection 给出。
//
// 未知域码一律 fail-closed：code 与 base_code 同时收敛为 INTERNAL，
// 避免未登记错误码泄漏到对外契约（contracts/02 §4 登记规则）。
package httperr

import (
	"encoding/json"
	"errors"
	"net/http"

	apperrors "odap/internal/platform/errors"
)

// 12 个基础码（contracts/02 §2）：直接引用 platform/errors 的常量，不再重复字面量。
const (
	BaseInvalidArgument   = apperrors.CodeInvalidArgument
	BaseUnauthenticated   = apperrors.CodeUnauthenticated
	BasePermissionDenied  = apperrors.CodePermissionDenied
	BaseNotFound          = apperrors.CodeNotFound
	BaseConflict          = apperrors.CodeConflict
	BaseValidationFailed  = apperrors.CodeValidationFailed
	BaseRateLimited       = apperrors.CodeRateLimited
	BaseUnavailable       = apperrors.CodeUnavailable
	BaseResourceExhausted = apperrors.CodeResourceExhausted
	BaseDeadlineExceeded  = apperrors.CodeDeadlineExceeded
	BaseExecutionUnknown  = apperrors.CodeExecutionUnknown
	BaseInternal          = apperrors.CodeInternal
)

// Spec 描述一个码的对外语义。
type Spec struct {
	BaseCode   string
	HTTPStatus int
	Retryable  bool
}

// ErrorBody 是 contracts/02 §4 的 error 对象。
type ErrorBody struct {
	Code      string         `json:"code"`
	BaseCode  string         `json:"base_code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// Response 是完整的错误响应体。
type Response struct {
	Error     ErrorBody `json:"error"`
	RequestID string    `json:"request_id"`
}

// BaseCode 返回域码投影后的基础码；未登记码返回 INTERNAL（fail-closed）。
func BaseCode(code string) string {
	base, _, _, _ := apperrors.Projection(code)
	return base
}

// HTTPStatus 返回域码对应的 HTTP 状态码（取自基础码；未知码一律 500）。
func HTTPStatus(code string) int {
	_, status, _, _ := apperrors.Projection(code)
	return status
}

// Retryable 返回域码是否可重试（取自基础码语义）。
func Retryable(code string) bool {
	_, _, retryable, _ := apperrors.Projection(code)
	return retryable
}

// SpecFor 返回某个码的对外投影三元组。
func SpecFor(code string) (Spec, bool) {
	base, status, retryable, ok := apperrors.Projection(code)
	return Spec{BaseCode: base, HTTPStatus: status, Retryable: retryable}, ok
}

// defaultMessage 为基础码提供脱敏的缺省文案（绝不回显底层 Cause）。
func defaultMessage(base string) string {
	switch base {
	case BaseInvalidArgument:
		return "请求参数不合法"
	case BaseUnauthenticated:
		return "未认证或身份已失效"
	case BasePermissionDenied:
		return "无权限执行该操作"
	case BaseNotFound:
		return "资源不存在"
	case BaseConflict:
		return "状态或版本冲突"
	case BaseValidationFailed:
		return "业务校验未通过"
	case BaseRateLimited:
		return "请求过于频繁，请稍后重试"
	case BaseUnavailable:
		return "依赖服务暂不可用"
	case BaseResourceExhausted:
		return "配额或预算不足"
	case BaseDeadlineExceeded:
		return "操作超时"
	case BaseExecutionUnknown:
		return "操作结果尚未确认，已转入对账"
	default:
		return "内部错误"
	}
}

// FromError 把任意 error 投影为响应体与 HTTP 状态码。
//
// 规则：
//   - *errors.AppError：code 取自 AppError.Code，base/状态/可重试由唯一注册表投影；
//   - 其它 error：收敛为 INTERNAL，绝不把原始错误文本回显给调用方；
//   - 未登记域码：code/base_code 同时收敛为 INTERNAL。
func FromError(err error, requestID string) (Response, int) {
	resp := Response{RequestID: requestID}
	if err == nil {
		resp.Error = ErrorBody{Code: BaseInternal, BaseCode: BaseInternal,
			Message: defaultMessage(BaseInternal)}
		return resp, http.StatusInternalServerError
	}

	var ae *apperrors.AppError
	if errors.As(err, &ae) && ae != nil {
		base, status, retryable, ok := apperrors.Projection(ae.Code)
		if ok {
			msg := ae.Message
			if msg == "" {
				msg = defaultMessage(base)
			}
			resp.Error = ErrorBody{
				Code:      ae.Code,
				BaseCode:  base,
				Message:   msg,
				Retryable: retryable,
				Details:   sanitizeDetails(ae.Details),
			}
			return resp, status
		}
	}

	// 非 AppError 或未登记域码：fail-closed 收敛为 INTERNAL。
	resp.Error = ErrorBody{Code: BaseInternal, BaseCode: BaseInternal,
		Message: defaultMessage(BaseInternal)}
	return resp, http.StatusInternalServerError
}

// sanitizeDetails 保证 details 可被安全序列化，且不携带空 map。
func sanitizeDetails(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Write 将错误按契约结构写入响应；requestID 由调用方从请求上下文取得，
// 避免本包依赖 middleware（防止 import cycle）。
func Write(w http.ResponseWriter, requestID string, err error) {
	resp, status := FromError(err, requestID)
	writeJSON(w, status, resp)
}

// WriteCode 用显式域码 + 消息构造错误后写出（供尚无对应领域服务的占位路径使用，
// 必须携带明确原因，禁止静默成功）。未登记码一律 fail-closed 收敛为 INTERNAL。
func WriteCode(w http.ResponseWriter, requestID, code, message string) {
	base, status, retryable, ok := apperrors.Projection(code)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, Response{
			Error: ErrorBody{Code: BaseInternal, BaseCode: BaseInternal,
				Message: defaultMessage(BaseInternal)},
			RequestID: requestID,
		})
		return
	}
	if message == "" {
		message = defaultMessage(base)
	}
	writeJSON(w, status, Response{
		Error: ErrorBody{
			Code:      code,
			BaseCode:  base,
			Message:   message,
			Retryable: retryable,
		},
		RequestID: requestID,
	})
}

// WriteJSON 写出任意 2xx/3xx 成功响应体（供处理器复用）。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
