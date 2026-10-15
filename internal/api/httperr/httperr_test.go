package httperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apperrors "odap/internal/platform/errors"
)

// appErr 直接构造携带任意域码的 AppError（字段导出，绕过 platform/errors
// 的 E() 归一化，用于验证对外投影表本身）。
func appErr(code, msg string, details map[string]any) error {
	return &apperrors.AppError{Code: code, Message: msg, Details: details}
}

func TestRequiredMappings(t *testing.T) {
	cases := []struct {
		name       string
		code       string
		wantBase   string
		wantStatus int
		wantRetry  bool
	}{
		{"AUTHZ_PERMISSION_DENIED", "AUTHZ_PERMISSION_DENIED", BasePermissionDenied, http.StatusForbidden, false},
		{"ERP_OPERATION_UNKNOWN", "ERP_OPERATION_UNKNOWN", BaseExecutionUnknown, http.StatusAccepted, false},
		{"ONTOLOGY_VALIDATION_FAILED", "ONTOLOGY_VALIDATION_FAILED", BaseValidationFailed, http.StatusUnprocessableEntity, false},
		{"MODEL_BUDGET_EXCEEDED", "MODEL_BUDGET_EXCEEDED", BaseResourceExhausted, http.StatusTooManyRequests, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BaseCode(tc.code); got != tc.wantBase {
				t.Fatalf("BaseCode(%s)=%s want %s", tc.code, got, tc.wantBase)
			}
			if got := HTTPStatus(tc.code); got != tc.wantStatus {
				t.Fatalf("HTTPStatus(%s)=%d want %d", tc.code, got, tc.wantStatus)
			}
			if got := Retryable(tc.code); got != tc.wantRetry {
				t.Fatalf("Retryable(%s)=%v want %v", tc.code, got, tc.wantRetry)
			}
		})
	}
}

func TestExecutionUnknownIsAcceptedAndNotRetryable(t *testing.T) {
	resp, status := FromError(appErr("ERP_OPERATION_UNKNOWN",
		"操作结果尚未确认，已转入对账",
		map[string]any{"operation_id": "op_001", "reconciliation_id": "rec_9"}), "req_456")
	if status != http.StatusAccepted {
		t.Fatalf("EXECUTION_UNKNOWN status=%d want 202", status)
	}
	if resp.Error.Code != "ERP_OPERATION_UNKNOWN" || resp.Error.BaseCode != BaseExecutionUnknown {
		t.Fatalf("code/base mismatch: %+v", resp.Error)
	}
	if resp.Error.Retryable {
		t.Fatalf("EXECUTION_UNKNOWN must not be retryable")
	}
	if resp.RequestID != "req_456" {
		t.Fatalf("request_id not propagated: %q", resp.RequestID)
	}
	if resp.Error.Details["operation_id"] != "op_001" {
		t.Fatalf("details not propagated: %+v", resp.Error.Details)
	}
}

func TestUnknownDomainCodeFailsClosed(t *testing.T) {
	resp, status := FromError(appErr("TOTALLY_UNREGISTERED_CODE", "boom", nil), "r1")
	if status != http.StatusInternalServerError {
		t.Fatalf("unknown code status=%d want 500", status)
	}
	if resp.Error.Code != BaseInternal || resp.Error.BaseCode != BaseInternal {
		t.Fatalf("unknown code must collapse to INTERNAL, got %+v", resp.Error)
	}
}

func TestNonAppErrorCollapsesWithoutLeakingCause(t *testing.T) {
	resp, status := FromError(errors.New("pq: password authentication failed for user \"odap_app\""), "r2")
	if status != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", status)
	}
	if resp.Error.Message == "" || resp.Error.Code != BaseInternal {
		t.Fatalf("unexpected body: %+v", resp.Error)
	}
	if got := resp.Error.Message; got == "pq: password authentication failed for user \"odap_app\"" {
		t.Fatalf("internal error text leaked to client: %q", got)
	}
}

func TestPlatformBaseConstantsAliasMapping(t *testing.T) {
	// platform/errors 的"基础常量"在契约中的归属（同义收敛）。
	cases := map[string]string{
		"APPROVAL_REQUIRED":    BasePermissionDenied,
		"IDEMPOTENCY_CONFLICT": BaseConflict,
		"BUDGET_EXCEEDED":      BaseResourceExhausted,
		"VALIDATION_FAILED":    BaseValidationFailed,
		"EXEC_LEASE_LOST":      BaseConflict,
	}
	for code, want := range cases {
		if got := BaseCode(code); got != want {
			t.Fatalf("BaseCode(%s)=%s want %s", code, got, want)
		}
	}
}

func TestRetryableBases(t *testing.T) {
	if !Retryable(BaseUnavailable) || !Retryable(BaseRateLimited) {
		t.Fatalf("UNAVAILABLE/RATE_LIMITED must be retryable")
	}
	if Retryable(BaseValidationFailed) || Retryable(BaseExecutionUnknown) {
		t.Fatalf("VALIDATION_FAILED/EXECUTION_UNKNOWN must not be retryable")
	}
}

func TestWriteProducesContractJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, "req_abc", appErr("AUTHZ_PERMISSION_DENIED", "", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not json: %v", err)
	}
	if body["request_id"] != "req_abc" {
		t.Fatalf("request_id missing: %v", body)
	}
	eb, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object missing: %v", body)
	}
	for _, k := range []string{"code", "base_code", "message", "retryable"} {
		if _, ok := eb[k]; !ok {
			t.Fatalf("error.%s missing: %v", k, eb)
		}
	}
	if eb["code"] != "AUTHZ_PERMISSION_DENIED" || eb["base_code"] != BasePermissionDenied {
		t.Fatalf("unexpected error payload: %v", eb)
	}
}

func TestWriteCodePlaceholder(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteCode(rec, "req_x", BaseUnavailable, "依赖未接入")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}
