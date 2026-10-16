// Package handlers 实现 contracts/03 §2 的 HTTP 处理器。
//
// 分层约定：handler 只做 DTO 校验/转换 + 调用领域服务或持久化适配器 +
// 错误映射；业务规则必须落在领域包（internal/harness、internal/authorization、
// internal/tools 等）。所有写操作读取 Idempotency-Key 头并透传；关键写操作
// 支持 If-Match 版本前置条件（不匹配返回 409 CONFLICT）。
//
// 诚实性约束：凡依赖尚未接入本进程的能力（工具执行器、本体构件加载、
// ERP OperationGateway 装配等），一律返回明确的 UNAVAILABLE/INTERNAL 并
// 在响应体说明原因，**绝不返回假成功**。
package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"odap/internal/action"
	"odap/internal/api/httperr"
	"odap/internal/api/middleware"
	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/audit"
	"odap/internal/erp"
	"odap/internal/execution/outbox"
	"odap/internal/harness"
	"odap/internal/llm"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
	"odap/internal/registry"
	"odap/internal/toolruntime"
)

// Deps 是处理器的依赖集合（由 cmd/api 装配；测试可注入子集）。
type Deps struct {
	Store     *store.Store
	Machine   *harness.Machine
	Scheduler *harness.Scheduler
	Outbox    *outbox.Writer
	Audit     *audit.Recorder
	Events    *sse.Hub
	Clock     clock.Clock
	Logger    *slog.Logger
	// ReadyFunc 是 /readyz 的探针；nil 时 readyz 返回 503（未配置探针）。
	ReadyFunc func(ctx context.Context) error

	// ERP 是可选的 ERP Port；未装配时 ERP 相关端点返回 UNAVAILABLE。
	// 连接解析顺序见 handlers.resolveErpPort：租户 DB 连接（erp_connections）
	// 优先，此处 env 装配的 Port 是显式标注的 legacy 回落。
	ERP             erp.Port
	ERPConnectionID string
	// ErpStore 是 ERP 操作账本（integration.erp_operations）的读面；
	// GET /runs/{id}/erp-operations（任务详情「外部操作与对账」）依赖它。
	ErpStore erp.OperationStore

	// Version 是 API 版本（/healthz 与 openapi info.version 对齐）。
	Version string

	// SessionService 用于 auth/login 签发会话（identity.SessionService）。
	// 未装配时 auth 端点返回 UNAVAILABLE。
	SessionService SessionIssuer

	// SecureCookies 为 true 时会话 Cookie 带 Secure 属性（仅 HTTPS 下下发）。
	// 生产环境应为 true；本地 http 开发/演示必须为 false，否则浏览器拒绝保存 Cookie。
	SecureCookies bool

	// LLM 是模型网关（生成类端点的唯一入口：本体/Agent/工具/工作流自动生成）。
	// 未装配时生成端点返回 UNAVAILABLE 并说明原因，**绝不返回假生成结果**。
	LLM LLMCompleter
	// DocsRoot 是制度文档根目录（本体自动生成读取来源）。
	// 未配置时使用 "docs/资料"（相对进程工作目录）。
	DocsRoot string
	// UploadsRoot 是上传文档的落盘根目录（AI 自动生成的 file 来源）。
	// 未配置时使用 "var/uploads"（相对进程工作目录）。
	//
	// 与 DocsRoot 分离：制度文档语料目录以只读方式挂载（仓库资产），
	// 用户上传属于运行期数据，必须落在可写目录，否则上传必然失败。
	UploadsRoot string

	// GraphStore 是语义图存储（本体校验的唯一数据面：装载 TBox/ABox 并执行
	// SHACL 校验）。未装配时 POST /ontologies/{id}/validate 返回 UNAVAILABLE
	// 并说明原因——校验必须真读图，因此无图存储时**无法**给出结论，
	// 而不是给出一个"没查到问题"的空结论。
	GraphStore graphstore.GraphStore

	// Catalog 是工具/智能体目录解析服务（registry.CatalogService）。
	// 工具调用链解析"可执行版本"的唯一真值来源；未装配时工具调用端点
	// 返回 UNAVAILABLE（解析器缺失时拒绝伪造一个版本语义）。
	Catalog *registry.CatalogService

	// ToolCache 是 READ 类工具执行结果的短 TTL 缓存（toolruntime.ResultCache）。
	// 可选：nil 时每次调用都真实执行（缓存只是增强项，绝不是正确性依赖）。
	ToolCache *toolruntime.ResultCache

	// KBEmbeddingModel 是知识库摄取（嵌入/抽取任务）点名路由的嵌入 ModelID
	// （环境变量 ODAP_KB_EMBEDDING_MODEL；必须是 Model Registry 已登记且声明
	// Embeddings 能力的模型）。留空时文档上传仍可解析分块，但创建摄取任务
	// 直接失败并明确报错——无嵌入模型就不可能建向量索引，fail-closed。
	KBEmbeddingModel string

	// Actions 是动作记录器（internal/action.Recorder）：runs/工具调用/推理/
	// 知识摄取/A2A 各业务流经 RecordActionForFlow 旁路记录 Action（PROV-O
	// 溯源 + 相似检索嵌入）。未装配时记录跳过并告警一次，绝不阻断业务流。
	Actions *action.Recorder
}

// LLMCompleter 是 LLM Gateway 的最小依赖面（生成类端点唯一入口）。
// *llm.Gateway 满足该接口；测试可注入替身。
type LLMCompleter interface {
	Complete(ctx context.Context, tenantID ids.ID, req llm.CompletionRequest) (llm.Completion, error)
}

// docsRoot 返回制度文档根目录（默认 docs/资料）。
func (h *Handler) docsRoot() string {
	if h.deps.DocsRoot != "" {
		return h.deps.DocsRoot
	}
	return "docs/资料"
}

// uploadsRoot 返回上传文档落盘根目录（默认 var/uploads）。
func (h *Handler) uploadsRoot() string {
	if h.deps.UploadsRoot != "" {
		return h.deps.UploadsRoot
	}
	return "var/uploads"
}

// llmGateway 返回已装配的模型网关；未装配返回错误（供生成类端点 fail-closed）。
func (h *Handler) llmGateway() (LLMCompleter, error) {
	if h.deps.LLM == nil {
		return nil, unavailable("模型网关未装配：无法调用大模型生成；请配置真实 HTTP 模型端点（ODAP_LLM_BASE_URL / ODAP_LLM_API_KEY）后重启 API")
	}
	return h.deps.LLM, nil
}

// IssuedSession 是签发后可序列化的最小会话投影。
type IssuedSession struct {
	SessionID        string
	TenantID         string
	SubjectID        string
	SubjectVersion   int64
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	RefreshPlain     string
}

// SessionIssuer 是 identity.SessionService 的最小依赖面。
type SessionIssuer interface {
	Issue(ctx context.Context, tenantID, subjectID ids.ID, subjectType, authMethod string, ttl, refreshTTL time.Duration) (IssuedSession, error)
}

// Handler 承载全部 HTTP 处理器方法。
type Handler struct {
	deps Deps
}

// New 构造 Handler。
func New(d Deps) *Handler {
	if d.Clock == nil {
		d.Clock = clock.RealClock{}
	}
	if d.Version == "" {
		d.Version = "1.0.0"
	}
	return &Handler{deps: d}
}

// SetERP 装配期注入 ERP Port（worker 进程没有 HTTP 装配链路，用显式注入
// 代替 env 解析散落；cmd/api 走 Deps 字面量，不经此方法）。
func (h *Handler) SetERP(port erp.Port, connectionID string) {
	h.deps.ERP = port
	h.deps.ERPConnectionID = connectionID
}

// maxBodyBytes 是请求体上限（防御超大 JSON）。
const maxBodyBytes = 1 << 20

// maxUploadBytes 是 multipart 上传请求体总上限（含表单字段）。
//
// 明显大于 maxBodyBytes：JSON 端点的上限不放宽（不扩大 DoS 面），仅
// 上传通道单独放开。该值必须与 nginx 的 client_max_body_size 对齐，
// 否则请求会在反代层被 413 拒绝（后端日志无痕，难以定位）。
const maxUploadBytes = 32 << 20 // 32 MiB

// decodeJSON 解析请求体；非法 JSON/超限返回 INVALID_ARGUMENT。
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.E(errors.CodeInvalidArgument, "请求体不能为空")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if stderrors.As(err, &maxErr) {
			return errors.E(errors.CodeInvalidArgument, "请求体过大")
		}
		return errors.E(errors.CodeInvalidArgument, "请求体不是合法 JSON 或包含未知字段")
	}
	return nil
}

// decodeJSONLoose 与 decodeJSON 相同，但允许未知字段。
func decodeJSONLoose(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.E(errors.CodeInvalidArgument, "请求体不能为空")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		return errors.E(errors.CodeInvalidArgument, "请求体不是合法 JSON")
	}
	return nil
}

// decodeOptionalJSON 允许空请求体。
func decodeOptionalJSON(r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := decodeJSONLoose(r, dst); err != nil {
		if stderrors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// identity 读取可信身份（纵深防御；路由已在认证链之后）。
func (h *Handler) identity(r *http.Request) (tenantctx.Identity, error) {
	return middleware.IdentityFromContext(r.Context())
}

// fail 按契约结构写出错误响应。
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	httperr.Write(w, middleware.RequestIDFromContext(r.Context()), err)
}

// writeJSON 写出成功响应。
func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	httperr.WriteJSON(w, status, v)
}

// pathID 解析路径参数中的 UUID。
func (h *Handler) pathID(r *http.Request, name string) (ids.ID, error) {
	id, err := ids.Parse(r.PathValue(name))
	if err != nil {
		return ids.ID{}, errors.E(errors.CodeInvalidArgument, "路径参数不合法: "+name)
	}
	return id, nil
}

// limitParam 解析 ?limit=（cursor 分页）。
func limitParam(r *http.Request, def int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return store.NormalizeLimit(0, def)
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.E(errors.CodeInvalidArgument, "limit 必须是整数")
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0, errors.E(errors.CodeInvalidArgument, "limit 过大")
		}
	}
	return store.NormalizeLimit(n, def)
}

// idempotencyKey 读取 Idempotency-Key（回写响应头，便于客户端核对）。
func idempotencyKey(r *http.Request) string {
	k := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(k) > 200 {
		k = k[:200]
	}
	return k
}

// echoIdempotency 把 Idempotency-Key 回写到响应头（透传语义，便于客户端核对）。
func echoIdempotency(w http.ResponseWriter, r *http.Request) {
	if k := idempotencyKey(r); k != "" {
		w.Header().Set("Idempotency-Key", k)
	}
}

// ifMatch 校验 If-Match 版本前置条件；不匹配返回 409 CONFLICT 并返回 false。
func (h *Handler) ifMatch(w http.ResponseWriter, r *http.Request, current string) bool {
	want := strings.TrimSpace(r.Header.Get("If-Match"))
	if want == "" || want == "*" {
		return true
	}
	want = strings.Trim(want, "\"")
	if want != current {
		h.fail(w, r, errors.E(errors.CodeConflict, "If-Match 版本前置条件不满足").
			WithDetails(map[string]any{"current_version": current}))
		return false
	}
	return true
}

// publish 把事件投递到进程内实时总线（SSE MVP 实现）。
func (h *Handler) publish(tenantID, eventType string, crit sse.Criticality, payload any) {
	if h.deps.Events == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.deps.Events.Publish(sse.Event{
		Type:        eventType,
		TenantID:    tenantID,
		Criticality: crit,
		Data:        data,
		OccurredAt:  h.now(),
	})
}

// now 返回当前时间。
func (h *Handler) now() time.Time {
	if h.deps.Clock == nil {
		return time.Now().UTC()
	}
	return h.deps.Clock.Now()
}

// appendOutbox 追加一条发件箱事件（若已装配）。
func (h *Handler) appendOutbox(r *http.Request, tenantID ids.ID, evType string, aggID ids.ID, payload map[string]any) error {
	if h.deps.Outbox == nil {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "序列化事件载荷失败")
	}
	_, err = h.deps.Outbox.Append(r.Context(), tenantID, outbox.Event{
		AggregateType: "RUN",
		AggregateID:   aggID,
		EventType:     evType,
		Payload:       b,
	})
	return err
}

// recordAudit 追加审计事件（若已装配）。
//
// 审计写入失败在普通路径下不阻断业务（第十一篇 §9.7 的 best-effort 分级），
// 但绝不静默：以 ERROR 级别留痕，便于监控与告警，避免"业务成功而审计丢失"
// 无声发生。高危操作（权限/策略/授权变更）应走 audit.WriteOrBlock 的
// CriticalityCritical 路径阻断业务，见 config_crud 等写操作处理器。
func (h *Handler) recordAudit(r *http.Request, id tenantctx.Identity, evType, action, outcome string,
	resType, resID string, runID ids.ID, payload map[string]any) {
	h.recordAuditCtx(r.Context(), id, evType, action, outcome, resType, resID, runID,
		middleware.RequestIDFromContext(r.Context()), payload)
}

// recordAuditCtx 是 recordAudit 的 context 版（无 HTTP 请求的调用方——
// worker 步进执行器——用同一条审计写入路径，RequestID 由调用方显式传入）。
func (h *Handler) recordAuditCtx(ctx context.Context, id tenantctx.Identity, evType, action, outcome string,
	resType, resID string, runID ids.ID, requestID string, payload map[string]any) {
	if h.deps.Audit == nil {
		return
	}
	b, _ := json.Marshal(payload)
	if _, err := h.deps.Audit.Append(ctx, audit.Event{
		TenantID:     id.TenantID,
		EventType:    evType,
		ActorType:    "USER",
		ActorID:      id.SubjectID.String(),
		ResourceType: resType,
		ResourceID:   resID,
		Action:       action,
		Outcome:      outcome,
		RequestID:    requestID,
		RunID:        runID,
		EventPayload: b,
	}); err != nil && h.deps.Logger != nil {
		h.deps.Logger.Error("审计写入失败（业务未阻断，需监控补写）",
			"event_type", evType, "action", action, "outcome", outcome,
			"resource_type", resType, "resource_id", resID, "error", err)
	}
}

// unavailable 构造带原因的 UNAVAILABLE（占位路径专用，绝不静默成功）。
func unavailable(reason string) error {
	return errors.E(errors.CodeUnavailable, reason)
}
