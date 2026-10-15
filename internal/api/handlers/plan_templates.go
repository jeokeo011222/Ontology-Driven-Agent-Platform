package handlers

import (
	"net/http"

	"odap/internal/platform/errors"
)

// planTemplatesWriteUnavailable 说明**写入**能力当前未落地。
//
// 缺陷 #24 记录：本文件此前对 GET/POST **一律**返回 503，并断言
// "plan-templates 资产在 MVP 数据库 schema 中无对应持久表"。那句断言是错的——
// 模板资产与执行计划共用 planning.plans / planning.plan_revisions，以
// status = 'TEMPLATE' 判别，种子（datasets/finance-demo/plans.json）已写入真实模板行。
// 把一句过期的设计假设当成实现，代价是整页恒为空态且报"依赖服务不可用"。
//
// 现状：GET 已按真实数据落地；写入路径 MVP 未实现，模板由数据集种子注入。
// 写入仍返回 503 并说明，**绝不返回伪成功**。
const planTemplatesWriteUnavailable = "plan-templates 写入端点未在 MVP 实现；" +
	"模板资产当前由数据集种子注入（GET /api/v1/plan-templates 可读真实模板）"

// ListPlanTemplates 处理 GET /api/v1/plan-templates（cursor 分页）。
func (h *Handler) ListPlanTemplates(w http.ResponseWriter, r *http.Request) {
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
	res, err := h.deps.Store.ListPlanTemplates(r.Context(), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreatePlanTemplate 处理 POST /api/v1/plan-templates。
func (h *Handler) CreatePlanTemplate(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	h.fail(w, r, errors.E(errors.CodeUnavailable, planTemplatesWriteUnavailable))
}
