package handlers

import (
	"net/http"

	"odap/internal/erp"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ERPCapabilities 处理 GET /api/v1/erp/capabilities。
//
// 能力声明来自 Adapter 注册并经连接测试（第七篇 §4.2）。本进程装配了 ERP Port
// 时返回真实能力；未装配（ODAP_ERP_ADAPTER 未配置）时返回 503 并说明原因。
func (h *Handler) ERPCapabilities(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.ERP == nil {
		h.fail(w, r, errors.E(errors.CodeUnavailable,
			"ERP Adapter 未装配（设置 ODAP_ERP_ADAPTER/ODAP_MOCK_ERP_BASE_URL 后启用）"))
		return
	}
	connID := r.URL.Query().Get("connection_id")
	if connID == "" {
		connID = h.deps.ERPConnectionID
	}
	if connID == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "connection_id 必填"))
		return
	}
	caps, err := h.deps.ERP.Capabilities(r.Context(), erp.ConnectionID(connID))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, caps)
}

// ReconcileEROperation 处理 POST /api/v1/erp/operations/{id}/reconcile。
//
// 单笔操作对账需要 integration.erp_operations 账本 + OperationGateway
// （PortResolver/AuthzChecker/ApprovalChecker 装配），该链路未接入本进程，
// 因此返回 503 并说明原因（绝不伪造对账结论）。
func (h *Handler) ReconcileEROperation(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	opID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ERPOperationReconcileUnavailable", "erp.operation.reconcile", "ERROR",
		"erp_operation", opID.String(), ids.ID{}, map[string]any{"operation_id": opID.String()})
	h.fail(w, r, errors.E(errors.CodeUnavailable,
		"单笔 ERP 操作对账需装配 OperationGateway（PortResolver/授权/审批校验），当前进程未接入"))
}

// ListRunErpOperations 处理 GET /api/v1/runs/{id}/erp-operations。
//
// 任务详情页「外部操作与对账」页签的数据源：Run 与 ERP 操作**并列展示**
// （contracts/01 §5.1）。此前该路由未注册，前端恒收到 404——这是以实战为
// 基础的硬性缺口，不是体验瑕疵：没有它，用户看不到一笔外部写操作的真实
// 账本状态（是否已派发、是否待对账、是否转人工复核）。
func (h *Handler) ListRunErpOperations(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.ErpStore == nil {
		h.fail(w, r, errors.E(errors.CodeUnavailable,
			"ERP 操作账本未装配，无法读取该任务的外部操作记录"))
		return
	}
	runID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ops, err := h.deps.ErpStore.ListByRun(r.Context(), id.TenantID, runID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(ops))
	for _, op := range ops {
		items = append(items, map[string]any{
			"id":               op.ID.String(),
			"run_id":           op.RunID.String(),
			"adapter_key":      op.AdapterKey,
			"external_system":  op.ExternalSystem,
			"object_type":      op.ObjectType,
			"object_id":        op.ObjectID,
			"action":           op.Action,
			"idempotency_key":  op.IdempotencyKey,
			"approval_id":      op.ApprovalID.String(),
			"status":           op.Status,
			"payload_snapshot": op.PayloadSnapshot,
			"payload_digest":   op.PayloadDigest,
			"created_at":       op.CreatedAt,
			"dispatched_at":    op.DispatchedAt,
			"finished_at":      op.FinishedAt,
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
