package handlers

import (
	"net/http"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// createEvaluationRequest 是 POST /api/v1/evaluations 的请求体。
type createEvaluationRequest struct {
	SubjectType       string `json:"subject_type"`
	SubjectID         string `json:"subject_id"`
	SubjectRevision   string `json:"subject_revision"`
	DatasetRevisionID string `json:"dataset_revision_id"`
	SuiteRevision     string `json:"suite_revision"`
	EnvironmentDigest string `json:"environment_digest"`
}

// ListEvaluations 处理 GET /api/v1/evaluations。
func (h *Handler) ListEvaluations(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListEvaluations(r.Context(), r.URL.Query().Get("status"), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateEvaluation 处理 POST /api/v1/evaluations。
func (h *Handler) CreateEvaluation(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createEvaluationRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	datasetRevisionID, err := ids.Parse(req.DatasetRevisionID)
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "dataset_revision_id 必须是 UUID"))
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateEvaluation(r.Context(), store.CreateEvaluationInput{
		SubjectType:       req.SubjectType,
		SubjectID:         req.SubjectID,
		SubjectRevision:   req.SubjectRevision,
		DatasetRevisionID: datasetRevisionID,
		SuiteRevision:     req.SuiteRevision,
		EnvironmentDigest: req.EnvironmentDigest,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "EvaluationRunCreated", "evaluation.run", "SUCCESS", "evaluation",
		stringify(out["id"]), ids.ID{}, map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "EvaluationRunCreated", sse.Normal,
		map[string]any{"evaluation_id": stringify(out["id"])})
	h.writeJSON(w, http.StatusAccepted, out)
}

// GetEvaluation 处理 GET /api/v1/evaluations/{id}。
func (h *Handler) GetEvaluation(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	evalID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetEvaluation(r.Context(), evalID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}
