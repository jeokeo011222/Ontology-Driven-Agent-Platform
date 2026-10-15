package handlers

import (
	"net/http"

	"odap/internal/platform/errors"
	"odap/internal/platform/runstate"
)

// taskView 是异步任务状态视图（contracts/03 §1.8）。
//
// MVP 说明：异步任务在数据模型上不单独建表（migrations/platform 无 tasks 表），
// 任务引用直接指向其承载资源。因此本端点把 Run 投影为任务状态：任务 ID =
// run_id，kind = "RUN"。对外语义（QUEUED/RUNNING/SUCCEEDED/FAILED/CANCELLED）
// 由 Run 状态派生，绝不把 Run 终态翻译为"业务已成功"（contracts/01 §5.1）。
type taskView struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	State     string  `json:"state"`
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	ErrorCode *string `json:"error_code,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	StatusURL string  `json:"status_url"`
	Href      string  `json:"href"`
}

// GetTask 处理 GET /api/v1/tasks/{id}。
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	taskID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	run, err := h.deps.Store.GetRun(r.Context(), taskID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	steps, err := h.deps.Store.ListRunSteps(r.Context(), taskID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	updated := run.CreatedAt
	if run.StartedAt != nil {
		updated = *run.StartedAt
	}
	if run.FinishedAt != nil {
		updated = *run.FinishedAt
	}

	view := taskView{
		ID:        run.ID,
		Kind:      "RUN",
		State:     taskStateOf(run.Status),
		Status:    run.Status,
		Progress:  progressOf(run.Status, len(steps)),
		ErrorCode: run.ErrorCode,
		CreatedAt: run.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt: updated.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		StatusURL: "/api/v1/tasks/" + run.ID,
		Href:      "/api/v1/runs/" + run.ID,
	}
	h.writeJSON(w, http.StatusOK, view)
}

// taskStateOf 把 Run 状态投影为任务状态（coarse-grained，供轮询客户端）。
func taskStateOf(status string) string {
	switch status {
	case runstate.Completed:
		return "SUCCEEDED"
	case runstate.Failed, runstate.Expired:
		return "FAILED"
	case runstate.Cancelled:
		return "CANCELLED"
	case runstate.Created:
		return "QUEUED"
	default:
		return "RUNNING"
	}
}

// progressOf 依据 Run 状态与步骤数给出粗略进度（0..1）。
func progressOf(status string, steps int) float64 {
	switch status {
	case runstate.Completed:
		return 1
	case runstate.Created:
		return 0
	case runstate.ContextBuilding, runstate.Ready, runstate.Planning, runstate.PlanValidating:
		return 0.1
	default:
		if steps == 0 {
			return 0.5
		}
		return 0.5
	}
}

// notFoundOrInternal 把不存在的任务统一报 NOT_FOUND。
func notFoundOrInternal(err error) error {
	return errors.Wrap(err, errors.CodeNotFound, "任务不存在或不在可见范围")
}
