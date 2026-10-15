// Package errors 是平台统一错误码的 Go 绑定。
// 权威词表见 docs/contracts/02-统一错误码注册表.md，本包不得新增
// 未登记的错误码；新增域码必须先更新注册表再改本文件。
package errors

import (
	"fmt"
	"net/http"
	"sort"
)

// Spec 描述一类错误的稳定语义。
//
// BaseCode 是 contracts/02 §4 要求随响应一并返回的「基础码」：域码给出具体语义，
// 基础码供客户端做通用处理（重试、提示、退避）。基础码自身的 BaseCode 等于其 Code。
type Spec struct {
	Code       string
	BaseCode   string // 域码映射到的 12 个基础码之一；基础码自身填自身
	HTTPStatus int
	Retryable  bool
	Audit      bool // 产生该错误时应记录审计事件
}

// AppError 是携带稳定错误码的应用错误。
type AppError struct {
	Code      string
	Message   string
	RequestID string
	Details   map[string]any
	// Cause 保存底层错误，仅供内部诊断，不进入对外响应。
	Cause error
}

// 基础错误码（contracts/02 §2 十二项）。
var baseSpecs = []Spec{
	{Code: CodeInvalidArgument, HTTPStatus: http.StatusBadRequest},
	{Code: CodeUnauthenticated, HTTPStatus: http.StatusUnauthorized, Audit: true},
	{Code: CodePermissionDenied, HTTPStatus: http.StatusForbidden, Audit: true},
	{Code: CodeNotFound, HTTPStatus: http.StatusNotFound},
	{Code: CodeConflict, HTTPStatus: http.StatusConflict, Audit: true},
	{Code: CodeValidationFailed, HTTPStatus: http.StatusUnprocessableEntity, Audit: true},
	{Code: CodeRateLimited, HTTPStatus: http.StatusTooManyRequests, Retryable: true},
	{Code: CodeUnavailable, HTTPStatus: http.StatusServiceUnavailable, Retryable: true},
	{Code: CodeResourceExhausted, HTTPStatus: http.StatusTooManyRequests, Audit: true},
	{Code: CodeDeadlineExceeded, HTTPStatus: http.StatusGatewayTimeout, Audit: true},
	{Code: CodeExecutionUnknown, HTTPStatus: http.StatusAccepted, Audit: true},
	{Code: CodeInternal, HTTPStatus: http.StatusInternalServerError, Audit: true},
}

// 域错误码（contracts/02 §3-§8）。
// 每个域码必须声明其 BaseCode（12 基础码之一），供 HTTP 状态与客户端通用处理派生。
var domainSpecs = []Spec{
	// IAM
	{Code: CodeIAMTenantInactive, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodeIAMSessionRevoked, BaseCode: CodeUnauthenticated},
	// AUTHZ
	{Code: CodeAUTHZPolicyInvalid, BaseCode: CodeInvalidArgument, Audit: true},
	{Code: CodeAUTHZPolicyEvaluationError, BaseCode: CodeInternal, Audit: true},
	{Code: CodeAUTHZScopeEscalation, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodeApprovalRequired, BaseCode: CodePermissionDenied, Audit: true},
	// PLUGIN
	{Code: CodePluginNotFound, BaseCode: CodeNotFound},
	{Code: CodePluginVersionConflict, BaseCode: CodeConflict},
	{Code: CodePluginManifestInvalid, BaseCode: CodeValidationFailed},
	{Code: CodePluginIsolationViolation, BaseCode: CodePermissionDenied, Audit: true},
	// EXEC
	{Code: CodeExecInvalidTransition, BaseCode: CodeConflict, Audit: true},
	{Code: CodeExecLeaseLost, BaseCode: CodeConflict, Retryable: true},
	{Code: CodeExecCheckpointCorrupt, BaseCode: CodeInternal, Audit: true},
	// ERP
	{Code: CodeERPAdapterError, BaseCode: CodeUnavailable, Retryable: true},
	{Code: CodeERPReceiptMismatch, BaseCode: CodeValidationFailed, Audit: true},
	{Code: CodeERPDuplicateOperation, BaseCode: CodeConflict, Audit: true},
	{Code: CodeIdempotencyConflict, BaseCode: CodeConflict, Audit: true},
	// ONTOLOGY / SEMANTIC
	{Code: CodeOntologyValidationFailed, BaseCode: CodeValidationFailed},
	{Code: CodeSemanticQueryInvalid, BaseCode: CodeInvalidArgument},
	// MODEL
	{Code: CodeModelTimeout, BaseCode: CodeDeadlineExceeded, Retryable: true},
	{Code: CodeModelOutputInvalid, BaseCode: CodeValidationFailed},
	{Code: CodeModelProviderError, BaseCode: CodeUnavailable, Retryable: true},
	{Code: CodeBudgetExceeded, BaseCode: CodeResourceExhausted, Audit: true},
	// AUDIT / EVIDENCE
	{Code: CodeAuditWriteFailed, BaseCode: CodeUnavailable, Retryable: true, Audit: true},
	{Code: CodeEvidenceIntegrityBroken, BaseCode: CodeConflict, Audit: true},
	// EVALUATION
	{Code: CodeEvaluationGateBlocked, BaseCode: CodeConflict, Audit: true},

	// ---- contracts/02 §3.1–§3.6 契约域码（对外投影的唯一真源）----
	{Code: CodeRequestValidationFailed, BaseCode: CodeInvalidArgument},
	{Code: CodeAUTHZPermissionDenied, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodeAUTHZApprovalRequired, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodeAUTHZScopeViolation, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodePluginInvalidInput, BaseCode: CodeInvalidArgument},
	{Code: CodePluginUnsupportedOperation, BaseCode: CodeInvalidArgument},
	{Code: CodePluginPermissionDenied, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodePluginDependencyUnavail, BaseCode: CodeUnavailable, Retryable: true},
	{Code: CodePluginRateLimited, BaseCode: CodeRateLimited, Retryable: true},
	{Code: CodePluginDeadlineExceeded, BaseCode: CodeDeadlineExceeded},
	{Code: CodePluginResourceExhausted, BaseCode: CodeResourceExhausted, Audit: true},
	{Code: CodePluginInternalError, BaseCode: CodeInternal, Audit: true},
	{Code: CodePluginResultInvalid, BaseCode: CodeValidationFailed, Audit: true},
	{Code: CodeModelDataPolicyDenied, BaseCode: CodePermissionDenied, Audit: true},
	{Code: CodeModelBudgetExceeded, BaseCode: CodeResourceExhausted, Audit: true},
	{Code: CodeModelCapabilityUnavailable, BaseCode: CodeUnavailable},
	{Code: CodeModelProviderUnavailable, BaseCode: CodeUnavailable, Retryable: true},
	{Code: CodeBusCapacityExceeded, BaseCode: CodeResourceExhausted, Audit: true},
	{Code: CodeBusDuplicateEvent, BaseCode: CodeConflict},
	{Code: CodeBusSequenceGap, BaseCode: CodeConflict, Audit: true},
	{Code: CodeERPOperationUnknown, BaseCode: CodeExecutionUnknown, Audit: true},
	{Code: CodeERPReceiptInvalid, BaseCode: CodeValidationFailed, Audit: true},
	{Code: CodeERPIdempotencyConflict, BaseCode: CodeConflict, Audit: true},
	{Code: CodeERPAdapterUnavailable, BaseCode: CodeUnavailable, Retryable: true},
	{Code: CodeERPBusinessRejected, BaseCode: CodeValidationFailed},
}

// 基础码常量（contracts/02 §2，共 12 个）。
const (
	CodeInvalidArgument   = "INVALID_ARGUMENT"
	CodeUnauthenticated   = "UNAUTHENTICATED"
	CodePermissionDenied  = "PERMISSION_DENIED"
	CodeNotFound          = "NOT_FOUND"
	CodeConflict          = "CONFLICT"
	CodeValidationFailed  = "VALIDATION_FAILED"
	CodeRateLimited       = "RATE_LIMITED"
	CodeUnavailable       = "UNAVAILABLE"
	CodeResourceExhausted = "RESOURCE_EXHAUSTED"
	CodeDeadlineExceeded  = "DEADLINE_EXCEEDED"
	CodeExecutionUnknown  = "EXECUTION_UNKNOWN"
	CodeInternal          = "INTERNAL"
)

// 域码常量（contracts/02 §3）。
const (
	CodeApprovalRequired    = "APPROVAL_REQUIRED"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeBudgetExceeded      = "BUDGET_EXCEEDED"
)

// 域码常量（contracts/02 §3-§8）。
const (
	CodeIAMTenantInactive = "IAM_TENANT_INACTIVE"
	CodeIAMSessionRevoked = "IAM_SESSION_REVOKED"

	CodeAUTHZPolicyInvalid         = "AUTHZ_POLICY_INVALID"
	CodeAUTHZPolicyEvaluationError = "AUTHZ_POLICY_EVALUATION_ERROR"
	CodeAUTHZScopeEscalation       = "AUTHZ_SCOPE_ESCALATION"

	CodePluginNotFound           = "PLUGIN_NOT_FOUND"
	CodePluginVersionConflict    = "PLUGIN_VERSION_CONFLICT"
	CodePluginManifestInvalid    = "PLUGIN_MANIFEST_INVALID"
	CodePluginIsolationViolation = "PLUGIN_ISOLATION_VIOLATION"

	CodeExecInvalidTransition = "EXEC_INVALID_TRANSITION"
	CodeExecLeaseLost         = "EXEC_LEASE_LOST"
	CodeExecCheckpointCorrupt = "EXEC_CHECKPOINT_CORRUPT"

	CodeERPAdapterError       = "ERP_ADAPTER_ERROR"
	CodeERPReceiptMismatch    = "ERP_RECEIPT_MISMATCH"
	CodeERPDuplicateOperation = "ERP_DUPLICATE_OPERATION"

	CodeOntologyValidationFailed = "ONTOLOGY_VALIDATION_FAILED"
	CodeSemanticQueryInvalid     = "SEMANTIC_QUERY_INVALID"

	CodeModelTimeout       = "MODEL_TIMEOUT"
	CodeModelOutputInvalid = "MODEL_OUTPUT_INVALID"
	CodeModelProviderError = "MODEL_PROVIDER_ERROR"

	CodeAuditWriteFailed        = "AUDIT_WRITE_FAILED"
	CodeEvidenceIntegrityBroken = "EVIDENCE_INTEGRITY_BROKEN"

	CodeEvaluationGateBlocked = "EVALUATION_GATE_BLOCKED"
)

// 契约域码常量（contracts/02 §3.1–§3.6 逐条落地）。
// 这些是契约文档直接定义的域码；实现与对外投影统一以本注册表为唯一真源。
const (
	CodeRequestValidationFailed = "REQUEST_VALIDATION_FAILED"

	CodeAUTHZPermissionDenied = "AUTHZ_PERMISSION_DENIED"
	CodeAUTHZApprovalRequired = "AUTHZ_APPROVAL_REQUIRED"
	CodeAUTHZScopeViolation   = "AUTHZ_SCOPE_VIOLATION"

	CodePluginInvalidInput         = "PLUGIN_INVALID_INPUT"
	CodePluginUnsupportedOperation = "PLUGIN_UNSUPPORTED_OPERATION"
	CodePluginPermissionDenied     = "PLUGIN_PERMISSION_DENIED"
	CodePluginDependencyUnavail    = "PLUGIN_DEPENDENCY_UNAVAILABLE"
	CodePluginRateLimited          = "PLUGIN_RATE_LIMITED"
	CodePluginDeadlineExceeded     = "PLUGIN_DEADLINE_EXCEEDED"
	CodePluginResourceExhausted    = "PLUGIN_RESOURCE_EXHAUSTED"
	CodePluginInternalError        = "PLUGIN_INTERNAL_ERROR"
	CodePluginResultInvalid        = "PLUGIN_RESULT_INVALID"

	CodeModelDataPolicyDenied      = "MODEL_DATA_POLICY_DENIED"
	CodeModelBudgetExceeded        = "MODEL_BUDGET_EXCEEDED"
	CodeModelCapabilityUnavailable = "MODEL_CAPABILITY_UNAVAILABLE"
	CodeModelProviderUnavailable   = "MODEL_PROVIDER_UNAVAILABLE"

	CodeBusCapacityExceeded = "BUS_CAPACITY_EXCEEDED"
	CodeBusDuplicateEvent   = "BUS_DUPLICATE_EVENT"
	CodeBusSequenceGap      = "BUS_SEQUENCE_GAP"

	CodeERPOperationUnknown    = "ERP_OPERATION_UNKNOWN"
	CodeERPReceiptInvalid      = "ERP_RECEIPT_INVALID"
	CodeERPIdempotencyConflict = "ERP_IDEMPOTENCY_CONFLICT"
	CodeERPAdapterUnavailable  = "ERP_ADAPTER_UNAVAILABLE"
	CodeERPBusinessRejected    = "ERP_BUSINESS_REJECTED"
)

var registry = func() map[string]Spec {
	m := make(map[string]Spec)
	for _, s := range append(append([]Spec{}, baseSpecs...), domainSpecs...) {
		if s.BaseCode == "" {
			s.BaseCode = s.Code // 基础码自身
		}
		m[s.Code] = s
	}
	return m
}()

// BaseCode 返回该错误码对应的基础码（contracts/02 §4 响应字段 base_code）。
// 未登记的码返回 INTERNAL（fail-closed）。
func BaseCode(code string) string {
	if spec, ok := registry[code]; ok {
		return spec.BaseCode
	}
	return CodeInternal
}

// BaseCodes 返回 contracts/02 §2 的 12 个基础码（排序后）。
// 供 CI 门禁 tools/contract-lint 断言基础码集合与契约逐项相等。
func BaseCodes() []string {
	out := make([]string, 0, len(baseSpecs))
	for _, s := range baseSpecs {
		out = append(out, s.Code)
	}
	sort.Strings(out)
	return out
}

// AllCodes 返回全部已登记错误码（基础码 + 域码，排序后）。
// 供 CI 门禁 tools/contract-lint 断言"代码中出现的码 ⊆ 契约注册表"。
func AllCodes() []string {
	out := make([]string, 0, len(registry))
	for code := range registry {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// DomainCodes 返回全部域码（不含基础码，排序后）。
func DomainCodes() []string {
	out := make([]string, 0, len(domainSpecs))
	for _, s := range domainSpecs {
		out = append(out, s.Code)
	}
	sort.Strings(out)
	return out
}

// Projection 返回对外响应（contracts/02 §4）所需的投影三元组。
//
// HTTP 状态与 retryable 一律取自**基础码**（contracts/02 §2 为基础码定义这两个语义），
// 域码只提供具体 code 与更精确的语义。未登记的码 fail-closed 收敛为 INTERNAL，
// 且 ok=false，调用方据此把 code 也收敛为 INTERNAL（禁止未登记码泄漏到对外契约）。
func Projection(code string) (base string, httpStatus int, retryable bool, ok bool) {
	spec, found := registry[code]
	if !found {
		internal := registry[CodeInternal]
		return CodeInternal, internal.HTTPStatus, internal.Retryable, false
	}
	baseSpec, ok2 := registry[spec.BaseCode]
	if !ok2 {
		internal := registry[CodeInternal]
		return CodeInternal, internal.HTTPStatus, internal.Retryable, false
	}
	return baseSpec.Code, baseSpec.HTTPStatus, baseSpec.Retryable, true
}

// Lookup 返回错误码的规范语义；未登记的码返回 false。
func Lookup(code string) (Spec, bool) {
	s, ok := registry[code]
	return s, ok
}

// E 按错误码构造 AppError。未登记的码按 INTERNAL 处理（fail-closed）。
func E(code, message string) *AppError {
	spec, ok := registry[code]
	if !ok {
		spec = registry[CodeInternal]
	}
	return &AppError{Code: spec.Code, Message: message}
}

// Ef 是 E 的格式化版本。
func Ef(code, format string, args ...any) *AppError {
	return E(code, fmt.Sprintf(format, args...))
}

// Error 实现 error 接口。对外消息不含底层错误细节（第十六篇 §6.2）。
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
	}
	return e.Code + ": " + e.Message
}

// WithDetails 附加结构化详情（会经权限过滤后返回给调用方）。
func (e *AppError) WithDetails(kv map[string]any) *AppError {
	e.Details = kv
	return e
}

// WithRequestID 附加请求关联 ID。
func (e *AppError) WithRequestID(rid string) *AppError {
	e.RequestID = rid
	return e
}

// HTTPStatus 返回该错误对应的 HTTP 状态码。
// 域码的 HTTP 状态由其基础码派生（contracts/02 §2 为基础码定义 HTTP 映射）。
func (e *AppError) HTTPStatus() int {
	spec, ok := registry[e.Code]
	if !ok {
		return http.StatusInternalServerError
	}
	if base, ok := registry[spec.BaseCode]; ok {
		return base.HTTPStatus
	}
	return http.StatusInternalServerError
}

// Retryable 返回调用方是否可参考重试。
func (e *AppError) Retryable() bool {
	if spec, ok := registry[e.Code]; ok {
		return spec.Retryable
	}
	return false
}

// NeedsAudit 返回是否应记录审计事件。
func (e *AppError) NeedsAudit() bool {
	if spec, ok := registry[e.Code]; ok {
		return spec.Audit
	}
	return false
}

// As 尝试将 err 转换为 *AppError；不是则包装为 INTERNAL。
func As(err error) *AppError {
	if err == nil {
		return nil
	}
	if ae, ok := err.(*AppError); ok {
		return ae
	}
	return &AppError{Code: CodeInternal, Message: "内部错误", Cause: err}
}

// Wrap 用错误码包装底层错误，保留诊断链。
func Wrap(err error, code, message string) *AppError {
	ae := E(code, message)
	ae.Cause = err
	return ae
}
