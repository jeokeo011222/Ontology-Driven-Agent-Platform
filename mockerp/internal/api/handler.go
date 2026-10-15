// Package api 提供 Mock ERP 的 HTTP 路由（第七篇 §10）。
//
// 路由：
//
//	POST /mock/objects                              创建对象
//	GET  /mock/objects/{type}/{id}                  读取对象
//	POST /mock/objects/query                        受控查询
//	POST /mock/operations                           执行命令（读 Idempotency-Key 去重）
//	GET  /mock/operations/{id}                      查询远端操作状态
//	GET  /mock/operations/by-idempotency-key/{key}  按幂等键查询（对账路径）
//	GET  /mock/health                               健康检查
//	POST /mock/faults                               配置故障注入
package api

import (
	"encoding/json"
	stderrors "errors"
	"log/slog"
	"net/http"

	"odap/mockerp/internal/domain"
	"odap/mockerp/internal/service"
)

// Handler 承载 Mock ERP 的 HTTP 接口。
type Handler struct {
	svc    *service.Service
	logger *slog.Logger
	mux    *http.ServeMux
}

// NewHandler 构造路由处理器。
func NewHandler(svc *service.Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{svc: svc, logger: logger, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /mock/objects", h.createObject)
	h.mux.HandleFunc("GET /mock/objects/{type}/{id}", h.getObject)
	h.mux.HandleFunc("POST /mock/objects/query", h.queryObjects)
	h.mux.HandleFunc("POST /mock/operations", h.execute)
	h.mux.HandleFunc("GET /mock/operations/by-idempotency-key/{key}", h.getOperationByIdempotencyKey)
	h.mux.HandleFunc("GET /mock/operations/{id}", h.getOperation)
	h.mux.HandleFunc("GET /mock/health", h.health)
	h.mux.HandleFunc("POST /mock/faults", h.setFaults)
	return h
}

// ServeHTTP 实现 http.Handler。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// ── 请求体 DTO ─────────────────────────────────────────────────────────────

type createObjectRequest struct {
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id"`
	Status     string         `json:"status"`
	Attributes map[string]any `json:"attributes"`
}

type filterDTO struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type queryRequest struct {
	ObjectType string         `json:"object_type"`
	Status     string         `json:"status"`
	Filter     *filterDTO     `json:"filter"`
	Attributes map[string]any `json:"attributes"`
	Limit      int            `json:"limit"`
}

type executeRequest struct {
	Operation       string         `json:"operation"`
	ObjectType      string         `json:"object_type"`
	ObjectID        string         `json:"object_id"`
	Payload         map[string]any `json:"payload"`
	ExpectedVersion string         `json:"expected_version"`
}

type faultsRequest struct {
	Clear         bool           `json:"clear"`
	Kind          string         `json:"kind"`
	OperationType string         `json:"operation_type"`
	DelayMS       int            `json:"delay_ms"`
	Faults        []domain.Fault `json:"faults"`
}

// ── Handler 实现 ───────────────────────────────────────────────────────────

func (h *Handler) createObject(w http.ResponseWriter, r *http.Request) {
	var req createObjectRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Attributes == nil {
		req.Attributes = map[string]any{}
	}
	obj, err := h.svc.CreateObject(r.Context(), service.CreateObjectInput{
		ObjectType: req.ObjectType,
		ObjectID:   req.ObjectID,
		Status:     req.Status,
		Attributes: req.Attributes,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"object": obj})
}

func (h *Handler) getObject(w http.ResponseWriter, r *http.Request) {
	obj, err := h.svc.GetObject(r.Context(), r.PathValue("type"), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": obj})
}

func (h *Handler) queryObjects(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if !decode(w, r, &req) {
		return
	}
	var filter *service.Filter
	if req.Filter != nil {
		filter = &service.Filter{Field: req.Filter.Field, Op: req.Filter.Op, Value: req.Filter.Value}
	}
	items, hasMore, err := h.svc.QueryObjects(r.Context(), service.QueryRequest{
		ObjectType: req.ObjectType,
		Status:     req.Status,
		Filter:     filter,
		Limit:      req.Limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": hasMore})
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	var req executeRequest
	if !decode(w, r, &req) {
		return
	}
	expected := req.ExpectedVersion
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" && expected == "" {
		expected = ifMatch
	}
	outcome, err := h.svc.Execute(r.Context(), service.Command{
		Operation:       req.Operation,
		ObjectType:      req.ObjectType,
		ObjectID:        req.ObjectID,
		Payload:         req.Payload,
		ExpectedVersion: expected,
		IdempotencyKey:  r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	op := outcome.Operation
	body := map[string]any{
		"operation_id":          op.ID,
		"status":                outcome.Status,
		"object_type":           op.ObjectType,
		"object_id":             op.ObjectID,
		"external_operation_id": op.ExternalOperationID,
		"receipt_id":            "rcpt-" + op.ID,
		"replay":                outcome.Replay,
		"operation":             op,
	}
	status := http.StatusOK
	if outcome.ResponseDropped {
		// §11.2：ERP 已提交但响应丢失，客户端应收到超时并转入对账。
		status = http.StatusGatewayTimeout
		body["error"] = map[string]any{
			"code":    "ERP_TIMEOUT_UNKNOWN",
			"message": "已提交但响应丢失，结果未知，需对账确认",
		}
	}
	if op.ResponsePayload != nil {
		if v, ok := op.ResponsePayload["version"]; ok {
			body["result_version"] = v
		}
	}
	writeJSON(w, status, body)
}

func (h *Handler) getOperation(w http.ResponseWriter, r *http.Request) {
	op, err := h.svc.GetOperation(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operation_id":          op.ID,
		"status":                op.ResponseStatus,
		"external_operation_id": op.ExternalOperationID,
		"receipt_id":            "rcpt-" + op.ID,
		"operation":             op,
	})
}

func (h *Handler) getOperationByIdempotencyKey(w http.ResponseWriter, r *http.Request) {
	op, err := h.svc.GetOperationByIdempotencyKey(r.Context(), r.PathValue("key"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operation_id":          op.ID,
		"status":                op.ResponseStatus,
		"external_operation_id": op.ExternalOperationID,
		"receipt_id":            "rcpt-" + op.ID,
		"operation":             op,
	})
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.Health())
}

func (h *Handler) setFaults(w http.ResponseWriter, r *http.Request) {
	var req faultsRequest
	if !decode(w, r, &req) {
		return
	}
	faults := req.Faults
	if len(faults) == 0 && req.Kind != "" {
		faults = []domain.Fault{{Kind: req.Kind, OperationType: req.OperationType, DelayMS: req.DelayMS}}
	}
	applied := h.svc.SetFaults(faults, req.Clear)
	writeJSON(w, http.StatusOK, map[string]any{"faults": applied})
}

// ── 辅助 ───────────────────────────────────────────────────────────────────

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeErrorCode(w, http.StatusBadRequest, "VALIDATION_FAILED", "缺少请求体")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "VALIDATION_FAILED", "请求体不是合法 JSON")
		return false
	}
	return true
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := statusFor(err)
	if status >= 500 {
		h.logger.Error("mock-erp 内部错误", "error", err, "path", r.URL.Path)
	}
	writeErrorCode(w, status, code, err.Error())
}

func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// statusFor 把领域错误映射为 HTTP 状态与错误码。
func statusFor(err error) (int, string) {
	switch {
	case stderrors.Is(err, domain.ErrObjectNotFound), stderrors.Is(err, domain.ErrOperationNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case stderrors.Is(err, domain.ErrObjectExists):
		return http.StatusConflict, "OBJECT_EXISTS"
	case stderrors.Is(err, domain.ErrIdempotencyConflict):
		return http.StatusConflict, "IDEMPOTENCY_CONFLICT"
	case stderrors.Is(err, domain.ErrVersionConflict):
		return http.StatusConflict, "ERP_VERSION_CONFLICT"
	case stderrors.Is(err, domain.ErrInvalidTransition):
		return http.StatusConflict, "ERP_INVALID_TRANSITION"
	case stderrors.Is(err, domain.ErrRejected):
		return http.StatusUnprocessableEntity, "ERP_REJECTED"
	case stderrors.Is(err, domain.ErrInvalidRequest):
		return http.StatusBadRequest, "VALIDATION_FAILED"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}
