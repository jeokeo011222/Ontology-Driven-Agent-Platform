package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// A2A 远程智能体注册表（模块 C「Server 发布 + 注册表」的登记面）
//
// 端点（路由装配由 cmd/api 负责）：
//   GET    /api/v1/a2a/remote-agents          列表（cursor 10/页 + q/status）
//   POST   /api/v1/a2a/remote-agents          登记
//   GET    /api/v1/a2a/remote-agents/{id}     详情
//   PATCH  /api/v1/a2a/remote-agents/{id}     更新（PATCH 语义空值不改）
//   DELETE /api/v1/a2a/remote-agents/{id}     软删除
//   POST   /api/v1/a2a/remote-agents/{id}/probe 连通性探测（真实 GET Agent Card）
//
// 写端点统一「五件套」：echoIdempotency + 审计 + SSE + 软删 + platform/errors。
// 凭据只允许环境变量引用（auth_header_ref），明文绝不落库/日志/响应。
// ---------------------------------------------------------------------------

// a2aProbeTimeout 是探测远端 Agent Card 的 HTTP 超时。
const a2aProbeTimeout = 10 * time.Second

// a2aAgentCardPath 是 A2A 协议规定的 Agent Card 发现路径。
const a2aAgentCardPath = "/.well-known/agent.json"

// a2aCardMaxBytes 是 Agent Card 响应体上限（防御远端超大响应）。
const a2aCardMaxBytes = 1 << 20

// createRemoteAgentRequest 是 POST /api/v1/a2a/remote-agents 的请求体。
type createRemoteAgentRequest struct {
	AgentKey       string          `json:"agent_key"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	BaseURL        string          `json:"base_url"`
	AuthHeaderName string          `json:"auth_header_name,omitempty"`
	AuthHeaderRef  string          `json:"auth_header_ref,omitempty"`
	Capabilities   json.RawMessage `json:"capabilities,omitempty"`
	ToolWhitelist  []string        `json:"tool_whitelist,omitempty"`
}

// ListRemoteAgents 处理 GET /api/v1/a2a/remote-agents。
func (h *Handler) ListRemoteAgents(w http.ResponseWriter, r *http.Request) {
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
	q := r.URL.Query()
	res, err := h.deps.Store.ListRemoteAgents(r.Context(), limit,
		q.Get("cursor"), q.Get("status"), q.Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// CreateRemoteAgent 处理 POST /api/v1/a2a/remote-agents。
func (h *Handler) CreateRemoteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createRemoteAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.CreateRemoteAgent(r.Context(), store.CreateRemoteAgentInput{
		AgentKey:       req.AgentKey,
		Name:           req.Name,
		Description:    req.Description,
		BaseURL:        req.BaseURL,
		AuthHeaderName: req.AuthHeaderName,
		AuthHeaderRef:  req.AuthHeaderRef,
		Capabilities:   req.Capabilities,
		ToolWhitelist:  req.ToolWhitelist,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "A2ARemoteAgentCreated", "a2a.remote_agent.create", "SUCCESS",
		"a2a_remote_agent", mapStr(out["id"]), ids.ID{},
		map[string]any{"agent_key": mapStr(out["agent_key"]), "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "A2ARemoteAgentCreated", sse.Important,
		map[string]any{"remote_agent_id": mapStr(out["id"]), "agent_key": mapStr(out["agent_key"])})
	h.writeJSON(w, http.StatusCreated, out)
}

// GetRemoteAgent 处理 GET /api/v1/a2a/remote-agents/{id}。
func (h *Handler) GetRemoteAgent(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	agentID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.deps.Store.GetRemoteAgent(r.Context(), agentID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// updateRemoteAgentRequest 是 PATCH /api/v1/a2a/remote-agents/{id} 的请求体
// （PATCH 语义：空值不改；agent_key 不可变）。
type updateRemoteAgentRequest struct {
	Name           string          `json:"name,omitempty"`
	Description    string          `json:"description,omitempty"`
	BaseURL        string          `json:"base_url,omitempty"`
	AuthHeaderName string          `json:"auth_header_name,omitempty"`
	AuthHeaderRef  string          `json:"auth_header_ref,omitempty"`
	Status         string          `json:"status,omitempty"`
	Capabilities   json.RawMessage `json:"capabilities,omitempty"`
	ToolWhitelist  []string        `json:"tool_whitelist,omitempty"`
}

// UpdateRemoteAgent 处理 PATCH /api/v1/a2a/remote-agents/{id}。
func (h *Handler) UpdateRemoteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	agentID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req updateRemoteAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	out, err := h.deps.Store.UpdateRemoteAgent(r.Context(), agentID, store.UpdateRemoteAgentInput{
		Name:           req.Name,
		Description:    req.Description,
		BaseURL:        req.BaseURL,
		AuthHeaderName: req.AuthHeaderName,
		AuthHeaderRef:  req.AuthHeaderRef,
		Status:         req.Status,
		Capabilities:   req.Capabilities,
		ToolWhitelist:  req.ToolWhitelist,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "A2ARemoteAgentUpdated", "a2a.remote_agent.update", "SUCCESS",
		"a2a_remote_agent", agentID.String(), ids.ID{},
		map[string]any{"agent_key": mapStr(out["agent_key"]), "status": mapStr(out["status"]),
			"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "A2ARemoteAgentUpdated", sse.Important,
		map[string]any{"remote_agent_id": agentID.String()})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteRemoteAgent 处理 DELETE /api/v1/a2a/remote-agents/{id}（软删除）。
func (h *Handler) DeleteRemoteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	agentID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteRemoteAgent(r.Context(), agentID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "A2ARemoteAgentDeleted", "a2a.remote_agent.delete", "SUCCESS",
		"a2a_remote_agent", agentID.String(), ids.ID{},
		map[string]any{"idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "A2ARemoteAgentDeleted", sse.Important,
		map[string]any{"remote_agent_id": agentID.String()})
	h.writeJSON(w, http.StatusOK, map[string]any{"id": agentID.String(), "status": "DELETED"})
}

// a2aAgentCardSummary 是探测成功时回显的对方卡片摘要（不含技能明细全量）。
type a2aAgentCardSummary struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Version         string `json:"version,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	URL             string `json:"url,omitempty"`
	SkillsCount     int    `json:"skills_count"`
}

// a2aRemoteCard 是远端 Agent Card 的宽松解析目标（只要求 name/skills 字段存在）。
type a2aRemoteCard struct {
	Name            *string `json:"name"`
	Description     string  `json:"description"`
	Version         string  `json:"version"`
	ProtocolVersion string  `json:"protocolVersion"`
	URL             string  `json:"url"`
	Skills          *[]any  `json:"skills"`
	// SkillsCount 由形状校验阶段填充（json:"-" 不参与反序列化）。
	SkillsCount int `json:"-"`
}

// ProbeRemoteAgent 处理 POST /api/v1/a2a/remote-agents/{id}/probe。
//
// 服务端向 {base_url}/.well-known/agent.json 发起真实 GET（超时 10s）：
//   - auth_header_name 非空时，凭据取自 os.Getenv(auth_header_ref)；环境变量
//     未设置 → 明确报错「环境变量 X 未配置」，**绝不**以空凭据继续探测；
//   - 成功解析 JSON（name/skills 字段存在）→ 更新 last_probe_ok=true 并回显
//     对方卡片摘要；
//   - 任何失败 → last_probe_ok=false + 错误信息，probe 端点返回明确错误。
//
// 探测是写操作（更新探测结论），同样过幂等回显 + 审计 + SSE。
func (h *Handler) ProbeRemoteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	agentID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	row, err := h.deps.Store.GetRemoteAgent(r.Context(), agentID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)
	baseURL := strings.TrimRight(mapStr(row["base_url"]), "/")
	headerName := mapStr(row["auth_header_name"])

	card, probeErr := h.fetchA2AAgentCard(r, baseURL, headerName, mapStr(row["auth_header_ref"]))
	if probeErr != nil {
		// 探测失败是预期内的诊断结果（非系统故障）：写结论 + 审计 REJECTED + 明确报错。
		_ = h.deps.Store.UpdateRemoteAgentProbe(r.Context(), agentID, false, probeErr.Error())
		h.recordAudit(r, id, "A2ARemoteAgentProbed", "a2a.remote_agent.probe", "REJECTED",
			"a2a_remote_agent", agentID.String(), ids.ID{},
			map[string]any{"agent_key": mapStr(row["agent_key"]), "ok": false})
		h.publish(id.TenantID.String(), "A2ARemoteAgentProbed", sse.Important,
			map[string]any{"remote_agent_id": agentID.String(), "ok": false})
		h.fail(w, r, probeErr)
		return
	}

	summary := a2aAgentCardSummary{
		Name:            *card.Name, // fetchA2AAgentCard 已校验非空
		Description:     card.Description,
		Version:         card.Version,
		ProtocolVersion: card.ProtocolVersion,
		URL:             card.URL,
		SkillsCount:     card.SkillsCount,
	}
	if err := h.deps.Store.UpdateRemoteAgentProbe(r.Context(), agentID, true, ""); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "A2ARemoteAgentProbed", "a2a.remote_agent.probe", "SUCCESS",
		"a2a_remote_agent", agentID.String(), ids.ID{},
		map[string]any{"agent_key": mapStr(row["agent_key"]), "ok": true,
			"remote_name": summary.Name})
	h.publish(id.TenantID.String(), "A2ARemoteAgentProbed", sse.Important,
		map[string]any{"remote_agent_id": agentID.String(), "ok": true})
	h.writeJSON(w, http.StatusOK, map[string]any{"probe": map[string]any{
		"ok": true, "agent_card": summary}})
}

// fetchA2AAgentCard 真实请求远端 Agent Card 并做最小形状校验。
// 返回校验通过的卡片；失败返回带明确原因的错误（不含凭据）。
func (h *Handler) fetchA2AAgentCard(r *http.Request, baseURL, headerName, headerRef string) (*a2aRemoteCard, error) {
	if baseURL == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "远程智能体未配置 base_url，无法探测")
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+a2aAgentCardPath, nil)
	if err != nil {
		return nil, errors.Ef(errors.CodeInvalidArgument, "base_url 不合法，无法构造探测请求：%v", err)
	}
	// 出站认证：仅在登记了认证头名时注入凭据；凭据只来自环境变量引用，
	// env 未设置即拒绝 —— 带空凭据探测会把「401 拒绝」与「凭据未配置」两种
	// 成因混在一起，掩盖真实问题，fail-closed 才能给出可行动的错误信息。
	if headerName != "" {
		if headerRef == "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"该远程智能体配置了认证头 "+headerName+" 但未配置凭据环境变量引用（auth_header_ref），无法发起探测")
		}
		secret := os.Getenv(headerRef)
		if secret == "" {
			return nil, errors.Ef(errors.CodeUnavailable,
				"环境变量 %s 未配置：无法设置认证头 %s，探测拒绝以空凭据进行", headerRef, headerName)
		}
		req.Header.Set(headerName, secret)
	}
	client := &http.Client{Timeout: a2aProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Ef(errors.CodeUnavailable, "探测失败：无法连接 %s：%v", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Ef(errors.CodeUnavailable, "探测失败：远端 %s 返回 HTTP %d", baseURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, a2aCardMaxBytes+1))
	if err != nil {
		return nil, errors.Ef(errors.CodeUnavailable, "探测失败：读取远端响应体失败：%v", err)
	}
	if len(body) > a2aCardMaxBytes {
		return nil, errors.E(errors.CodeValidationFailed, "探测失败：远端 Agent Card 超过 1MB 上限")
	}
	var card a2aRemoteCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, errors.Ef(errors.CodeValidationFailed, "探测失败：%s 不是合法的 JSON Agent Card（%v）", a2aAgentCardPath, err)
	}
	if card.Name == nil || strings.TrimSpace(*card.Name) == "" {
		return nil, errors.E(errors.CodeValidationFailed, "探测失败：Agent Card 缺少 name 字段，不是合法的 A2A 卡片")
	}
	if card.Skills == nil {
		return nil, errors.E(errors.CodeValidationFailed, "探测失败：Agent Card 缺少 skills 字段，不是合法的 A2A 卡片")
	}
	card.SkillsCount = len(*card.Skills)
	return &card, nil
}
