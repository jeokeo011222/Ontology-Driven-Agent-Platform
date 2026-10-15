package errors

import "testing"

func TestRegistryComplete(t *testing.T) {
	// contracts/02 §2：恰好 12 个基础码必须全部登记
	base := []string{
		CodeInvalidArgument, CodeUnauthenticated, CodePermissionDenied, CodeNotFound,
		CodeConflict, CodeValidationFailed, CodeRateLimited, CodeUnavailable,
		CodeResourceExhausted, CodeDeadlineExceeded, CodeExecutionUnknown, CodeInternal,
	}
	if len(base) != 12 {
		t.Fatalf("基础码数量 = %d，contracts/02 §2 要求恰好 12", len(base))
	}
	for _, code := range base {
		spec, ok := Lookup(code)
		if !ok {
			t.Fatalf("base code %s not registered", code)
		}
		if spec.BaseCode != code {
			t.Fatalf("base code %s 的 BaseCode = %s，应为自身", code, spec.BaseCode)
		}
	}
	for _, code := range []string{
		CodeExecInvalidTransition, CodeERPReceiptMismatch, CodeAuditWriteFailed,
		CodeModelTimeout, CodeEvaluationGateBlocked, CodeAUTHZPolicyEvaluationError,
		CodeApprovalRequired, CodeIdempotencyConflict, CodeBudgetExceeded,
	} {
		if _, ok := Lookup(code); !ok {
			t.Fatalf("domain code %s not registered", code)
		}
	}
}

func TestEveryDomainCodeMapsToABaseCode(t *testing.T) {
	baseSet := map[string]bool{
		CodeInvalidArgument: true, CodeUnauthenticated: true, CodePermissionDenied: true,
		CodeNotFound: true, CodeConflict: true, CodeValidationFailed: true,
		CodeRateLimited: true, CodeUnavailable: true, CodeResourceExhausted: true,
		CodeDeadlineExceeded: true, CodeExecutionUnknown: true, CodeInternal: true,
	}
	for _, s := range domainSpecs {
		if s.BaseCode == "" {
			t.Fatalf("域码 %s 未声明 BaseCode", s.Code)
		}
		if !baseSet[s.BaseCode] {
			t.Fatalf("域码 %s 的 BaseCode %s 不是合法基础码", s.Code, s.BaseCode)
		}
	}
}

func TestUnknownCodeFallsBackToInternal(t *testing.T) {
	ae := E("TOTALLY_MADE_UP", "boom")
	if ae.Code != CodeInternal {
		t.Fatalf("unregistered code mapped to %s, want INTERNAL (fail-closed)", ae.Code)
	}
	if ae.HTTPStatus() != 500 {
		t.Fatalf("HTTPStatus = %d, want 500", ae.HTTPStatus())
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	// contracts/02 §2 基础码 HTTP 映射（逐项照抄，不得自由发挥）
	cases := map[string]int{
		CodeInvalidArgument:  400,
		CodeUnauthenticated:  401,
		CodePermissionDenied: 403,
		CodeNotFound:         404,
		CodeConflict:         409,
		CodeValidationFailed: 422,
		CodeRateLimited:      429,
		CodeUnavailable:      503,
		CodeExecutionUnknown: 202,
		CodeInternal:         500,
	}
	for code, want := range cases {
		if got := E(code, "x").HTTPStatus(); got != want {
			t.Fatalf("%s HTTPStatus = %d, want %d", code, got, want)
		}
	}
	// 域码 HTTP 由其基础码派生
	if got := E(CodeOntologyValidationFailed, "x").HTTPStatus(); got != 422 {
		t.Fatalf("ONTOLOGY_VALIDATION_FAILED HTTPStatus = %d, want 422（base VALIDATION_FAILED）", got)
	}
	if got := E(CodeApprovalRequired, "x").HTTPStatus(); got != 403 {
		t.Fatalf("APPROVAL_REQUIRED HTTPStatus = %d, want 403（base PERMISSION_DENIED）", got)
	}
}

func TestBaseCodeMapping(t *testing.T) {
	cases := map[string]string{
		CodePermissionDenied:         CodePermissionDenied,
		CodeApprovalRequired:         CodePermissionDenied,
		CodeIdempotencyConflict:      CodeConflict,
		CodeBudgetExceeded:           CodeResourceExhausted,
		CodeOntologyValidationFailed: CodeValidationFailed,
		CodeModelTimeout:             CodeDeadlineExceeded,
	}
	for code, want := range cases {
		if got := BaseCode(code); got != want {
			t.Fatalf("BaseCode(%s) = %s, want %s", code, got, want)
		}
	}
	// 未登记的码 fail-closed 到 INTERNAL
	if got := BaseCode("TOTALLY_MADE_UP"); got != CodeInternal {
		t.Fatalf("BaseCode(未知) = %s, want INTERNAL", got)
	}
}

func TestRetryableAndAuditFlags(t *testing.T) {
	if !E(CodeUnavailable, "x").Retryable() {
		t.Error("UNAVAILABLE should be retryable")
	}
	if E(CodeValidationFailed, "x").Retryable() {
		t.Error("VALIDATION_FAILED should not be retryable")
	}
	if !E(CodePermissionDenied, "x").NeedsAudit() {
		t.Error("PERMISSION_DENIED should require audit")
	}
	if !E(CodeExecutionUnknown, "x").NeedsAudit() {
		t.Error("EXECUTION_UNKNOWN should require audit")
	}
}

func TestAsWrapsForeignError(t *testing.T) {
	ae := As(errString("db down"))
	if ae.Code != CodeInternal {
		t.Fatalf("As wrapped code = %s, want INTERNAL", ae.Code)
	}
	if ae.Error() == "" {
		t.Fatal("Error() empty")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
