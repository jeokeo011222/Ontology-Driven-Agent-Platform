package erp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ── 操作账本状态（integration.erp_operations.status）────────────────────────
//
// 注意：这组状态描述"平台账本里这条操作处于哪一步"，与 Port 的
// OperationStatus（远端结果）是两套语义，不可混用。
const (
	LedgerPending         = "PENDING"
	LedgerValidating      = "VALIDATING"
	LedgerRejected        = "REJECTED"
	LedgerWaitingApproval = "WAITING_APPROVAL"
	LedgerReady           = "READY"
	LedgerDispatched      = "DISPATCHED"
	LedgerExecuting       = "EXECUTING"
	LedgerSucceeded       = "SUCCEEDED"
	LedgerFailed          = "FAILED"
	LedgerUnknown         = "UNKNOWN"
	LedgerReconciling     = "RECONCILING"
	LedgerManualReview    = "MANUAL_REVIEW"
)

// 回执类型（integration.erp_receipts.receipt_type）：一次操作可有多条回执。
const (
	ReceiptSubmit    = "submit"
	ReceiptQuery     = "query"
	ReceiptReconcile = "reconcile"
)

// DefaultReconcileThreshold 是连续对账仍无法确认结果后转入人工复核的默认次数（第七篇 §18）。
const DefaultReconcileThreshold = 5

// Operation 映射 integration.erp_operations 的全部列。
type Operation struct {
	ID              ids.ID
	TenantID        ids.ID
	RunID           ids.ID
	ToolCallID      ids.ID
	AdapterKey      string
	ExternalSystem  string
	ObjectType      string
	ObjectID        string
	Action          string
	IdempotencyKey  string
	ExpectedVersion string
	PayloadSnapshot map[string]any
	PayloadDigest   string
	ApprovalID      ids.ID
	Status          string
	CreatedAt       time.Time
	DispatchedAt    *time.Time
	FinishedAt      *time.Time

	// NeedsReconcile 不是数据库列：当 Prepare 命中一条 UNKNOWN 的既有操作时置位，
	// 提示调用方"不要新建，而应转入对账"（第七篇 §8.2）。
	NeedsReconcile bool
}

// Receipt 映射 integration.erp_receipts。
type Receipt struct {
	ID                    ids.ID
	TenantID              ids.ID
	OperationID           ids.ID
	ReceiptType           string
	ExternalOperationID   string
	ExternalObjectVersion string
	ResultStatus          string
	ResponseDigest        string
	ResponsePayload       map[string]any
	ReceivedAt            time.Time
}

// StatusTransition 描述一次受条件约束的账本状态跃迁。
// From 为空表示不限制前置状态（慎用）；RowsAffected=0 表示前置条件不满足，
// 由调用方决定按并发冲突或非法跃迁处理。
type StatusTransition struct {
	From          []string
	To            string
	At            time.Time
	SetDispatched bool
	SetFinished   bool
}

// OperationStore 抽象账本持久化，便于在单测中注入内存实现。
// PostgreSQL 实现见 store.go。
type OperationStore interface {
	// Insert 写入一条操作账本；幂等键冲突时返回 ErrIdempotencyExists。
	Insert(ctx context.Context, tenantID ids.ID, op *Operation) error
	// GetByID 按本地主键读取；不存在返回 CodeNotFound。
	GetByID(ctx context.Context, tenantID ids.ID, operationID ids.ID) (*Operation, error)
	// GetByIdempotencyKey 按 (tenant, external_system, idempotency_key) 读取；不存在返回 CodeNotFound。
	GetByIdempotencyKey(ctx context.Context, tenantID ids.ID, externalSystem, key string) (*Operation, error)
	// ListByRun 返回某 Run 名下的全部操作账本（按创建时间升序）。
	ListByRun(ctx context.Context, tenantID ids.ID, runID ids.ID) ([]*Operation, error)
	// UpdateStatus 执行条件状态更新；返回是否真正更新了行。
	UpdateStatus(ctx context.Context, tenantID ids.ID, operationID ids.ID, tr StatusTransition) (bool, error)
	// AppendReceipt 追加一条回执。
	AppendReceipt(ctx context.Context, tenantID ids.ID, r *Receipt) error
	// CountReceipts 统计某操作指定类型的回执条数。
	CountReceipts(ctx context.Context, tenantID ids.ID, operationID ids.ID, receiptType string) (int, error)
	// LatestReceipt 取某操作指定类型的最新回执；不存在返回 CodeNotFound。
	LatestReceipt(ctx context.Context, tenantID ids.ID, operationID ids.ID, receiptType string) (*Receipt, error)
}

// PortResolver 按租户与外部系统解析已配置并审核过的 Adapter。
type PortResolver interface {
	Resolve(ctx context.Context, tenantID ids.ID, externalSystem string) (Port, error)
}

// 授权决策效果。
const (
	AuthzEffectAllow           = "ALLOW"
	AuthzEffectDeny            = "DENY"
	AuthzEffectRequireApproval = "REQUIRE_APPROVAL"
)

// AuthzRequest 是写入前的授权检查输入。
type AuthzRequest struct {
	TenantID       ids.ID
	RunID          ids.ID
	StepID         ids.ID
	ExternalSystem string
	OperationType  string
	ObjectType     string
	ObjectID       string
}

// AuthzDecision 是授权决策结果（Effect + 可追踪的决策引用）。
type AuthzDecision struct {
	Effect     string
	DecisionID string
}

// AuthzChecker 在每次写入前重新判定授权（第七篇 §7.1：授权决策引用不能代替重新验证）。
type AuthzChecker interface {
	Check(ctx context.Context, req AuthzRequest) (AuthzDecision, error)
}

// ApprovalChecker 查询审批是否已通过。
type ApprovalChecker interface {
	IsApproved(ctx context.Context, tenantID ids.ID, approvalID string) (bool, error)
}

// PrepareInput 是一次业务意图的完整输入（第七篇 §7.1/§7.2）。
type PrepareInput struct {
	TenantID ids.ID
	RunID    ids.ID
	StepID   ids.ID

	ExternalSystem string
	AdapterKey     string
	ObjectType     string
	ObjectID       string
	Action         string
	// OperationType 参与幂等键派生；为空时回退为 Action。
	OperationType string

	Payload         map[string]any
	ExpectedVersion string

	AuthorizationDecisionID string
	ApprovalID              string
}

// OperationGateway 是 ODAP 与 ERP 之间唯一受控的写入通道（第七篇 §7）。
//
// 事务纪律（第七篇 §7.3）：禁止"BEGIN → CALL remote → COMMIT"伪事务。
// 本地状态被拆成有明确边界的持久化阶段：
//
//	短事务写账本 → 事务外远端调用 → 短事务落结果 + 回执。
type OperationGateway struct {
	Pool      *pgxpool.Pool
	Clock     clock.Clock
	Ports     PortResolver
	Authz     AuthzChecker
	Approvals ApprovalChecker
	Store     OperationStore

	// ReconcileThreshold 是连续对账仍无法确认结果后转入 MANUAL_REVIEW 的阈值。
	ReconcileThreshold int
}

// NewGateway 构造使用 PostgreSQL 账本实现的 Gateway。
func NewGateway(pool *pgxpool.Pool, clk clock.Clock, ports PortResolver, authz AuthzChecker, approvals ApprovalChecker) *OperationGateway {
	return &OperationGateway{
		Pool:      pool,
		Clock:     clk,
		Ports:     ports,
		Authz:     authz,
		Approvals: approvals,
		Store:     NewPGStore(pool),
	}
}

func (g *OperationGateway) now() time.Time {
	if g.Clock == nil {
		return clock.RealClock{}.Now()
	}
	return g.Clock.Now().UTC()
}

func (g *OperationGateway) store() (OperationStore, error) {
	if g == nil || g.Store == nil {
		return nil, errors.E(errors.CodeInternal, "erp: gateway 未配置 OperationStore")
	}
	return g.Store, nil
}

func (g *OperationGateway) threshold() int {
	if g.ReconcileThreshold > 0 {
		return g.ReconcileThreshold
	}
	return DefaultReconcileThreshold
}

// Prepare 创建操作意图（第七篇 §7.1）：
//
//	① 计算规范化 payload_digest；
//	② 授权检查（非 ALLOW → CodePermissionDenied / CodeApprovalRequired）；
//	③ 需要审批且未通过 → 落库 WAITING_APPROVAL 并返回（绝不提前调用 Adapter）；
//	④ 生成幂等键；
//	⑤ 在远端调用之前写入操作账本；
//	⑥ 幂等写冲突按 §8.2 处理：摘要一致返回已有操作；摘要不同 → IDEMPOTENCY_CONFLICT；
//	   在途返回其当前状态；UNKNOWN 返回该操作并标记需对账（绝不新建第二笔）。
func (g *OperationGateway) Prepare(ctx context.Context, in PrepareInput) (*Operation, error) {
	st, err := g.store()
	if err != nil {
		return nil, err
	}
	if in.TenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "erp: tenant id 不能为空")
	}
	if in.ExternalSystem == "" || in.AdapterKey == "" {
		return nil, errors.E(errors.CodeValidationFailed, "erp: external_system 与 adapter_key 必填")
	}
	if in.ObjectType == "" || in.Action == "" {
		return nil, errors.E(errors.CodeValidationFailed, "erp: object_type 与 action 必填")
	}
	opType := in.OperationType
	if opType == "" {
		opType = in.Action
	}

	// ① 规范化 payload 摘要（可信服务计算，不信任 Agent 提交的摘要）。
	digest, err := PayloadDigest(in.Payload)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeValidationFailed, "erp: 规范化 payload 失败")
	}

	// ② 授权检查。
	requiresApproval := in.ApprovalID != ""
	if g.Authz != nil {
		dec, err := g.Authz.Check(ctx, AuthzRequest{
			TenantID:       in.TenantID,
			RunID:          in.RunID,
			StepID:         in.StepID,
			ExternalSystem: in.ExternalSystem,
			OperationType:  opType,
			ObjectType:     in.ObjectType,
			ObjectID:       in.ObjectID,
		})
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "erp: 授权检查失败")
		}
		switch dec.Effect {
		case AuthzEffectAllow:
			// 放行
		case AuthzEffectRequireApproval:
			if g.Approvals == nil {
				return nil, errors.E(errors.CodeApprovalRequired,
					"erp: 操作需要人工审批，但网关未配置审批检查器")
			}
			requiresApproval = true
		default: // DENY 及任何未知效果一律拒绝（fail-closed）
			return nil, errors.Ef(errors.CodePermissionDenied,
				"erp: 授权拒绝（decision=%s，effect=%s）", dec.DecisionID, dec.Effect)
		}
	}

	// ③ 需要审批但未通过：落库 WAITING_APPROVAL，不调用 Adapter。
	targetStatus := LedgerReady
	if requiresApproval {
		approved := false
		if in.ApprovalID != "" && g.Approvals != nil {
			ok, err := g.Approvals.IsApproved(ctx, in.TenantID, in.ApprovalID)
			if err != nil {
				return nil, errors.Wrap(err, errors.CodeInternal, "erp: 审批状态查询失败")
			}
			approved = ok
		}
		if !approved {
			targetStatus = LedgerWaitingApproval
		}
	}

	// ④ 生成幂等键（生成权归 Gateway）。
	key := IdempotencyKey(in.TenantID, IntentParts{
		RunID:          in.RunID,
		StepID:         in.StepID,
		OperationType:  opType,
		TargetObjectID: in.ObjectID,
		PayloadDigest:  digest,
	})

	op := &Operation{
		ID:              ids.New(),
		TenantID:        in.TenantID,
		RunID:           in.RunID,
		AdapterKey:      in.AdapterKey,
		ExternalSystem:  in.ExternalSystem,
		ObjectType:      in.ObjectType,
		ObjectID:        in.ObjectID,
		Action:          in.Action,
		IdempotencyKey:  key,
		ExpectedVersion: in.ExpectedVersion,
		PayloadSnapshot: clonePayload(in.Payload),
		PayloadDigest:   digest,
		Status:          targetStatus,
		CreatedAt:       g.now(),
	}
	if in.ApprovalID != "" {
		if approvalID, err := ids.Parse(in.ApprovalID); err == nil {
			op.ApprovalID = approvalID
		}
	}

	// ⑤⑥ 幂等冲突处理 + 写入账本。
	existing, err := st.GetByIdempotencyKey(ctx, in.TenantID, in.ExternalSystem, key)
	if err != nil {
		if errors.As(err).Code != errors.CodeNotFound {
			return nil, err
		}
		if err := st.Insert(ctx, in.TenantID, op); err != nil {
			if isIdempotencyExists(err) {
				// 并发插入竞争：以先落库的记录为准，复查后按 §8.2 处理。
				existing, err2 := st.GetByIdempotencyKey(ctx, in.TenantID, in.ExternalSystem, key)
				if err2 != nil {
					return nil, err2
				}
				return resolveExisting(existing, digest)
			}
			return nil, err
		}
		return op, nil
	}
	return resolveExisting(existing, digest)
}

// resolveExisting 实现 §8.2 的既有记录处置规则。
func resolveExisting(existing *Operation, digest string) (*Operation, error) {
	if existing.PayloadDigest != digest {
		return nil, errors.Ef(errors.CodeIdempotencyConflict,
			"erp: 幂等键 %s 已绑定到不同的请求摘要（拒绝覆盖已发送的操作内容）", existing.IdempotencyKey)
	}
	if existing.Status == LedgerUnknown {
		// 原操作结果未知：不新建，交由对账处理。
		existing.NeedsReconcile = true
	}
	return existing, nil
}

// Dispatch 派发一个已就绪的操作（第七篇 §7.3）：
//
//	条件 UPDATE（PENDING/READY → DISPATCHED）→ 事务外 Port.Execute → 短事务落结果 + 回执。
//
// 是否允许从"结果未知/在途"状态重发，由远端幂等能力分级决定（第七篇 §8.3）：
// L3 在 UNKNOWN 后禁止自动重发（此处以条件状态集实施硬门禁）。
func (g *OperationGateway) Dispatch(ctx context.Context, tenantID ids.ID, operationID ids.ID) (*OperationResult, error) {
	st, err := g.store()
	if err != nil {
		return nil, err
	}
	if tenantID.IsZero() || operationID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "erp: tenantID 与 operationID 必填")
	}

	op, err := st.GetByID(ctx, tenantID, operationID)
	if err != nil {
		return nil, err
	}

	// 先解析 Adapter（无副作用），以确定幂等能力分级。
	port, err := g.resolvePort(ctx, tenantID, op.ExternalSystem)
	if err != nil {
		return nil, err
	}
	level, err := g.idempotencyLevel(ctx, port, op)
	if err != nil {
		return nil, err
	}

	allowed := dispatchableStatuses(level)
	if !contains(allowed, op.Status) {
		// 包含 UNKNOWN/MANUAL_REVIEW 在 L2/L3 下的自动重发拦截。
		return nil, errors.Ef(errors.CodeExecInvalidTransition,
			"erp: 操作 %s 当前状态 %s 不允许派发（幂等级别 %s）", operationID, op.Status, level)
	}

	// 短事务：条件状态跃迁。
	updated, err := st.UpdateStatus(ctx, tenantID, operationID, StatusTransition{
		From:          allowed,
		To:            LedgerDispatched,
		At:            g.now(),
		SetDispatched: true,
	})
	if err != nil {
		return nil, err
	}
	if !updated {
		// 并发派发竞争：另一个执行者已抢先推进状态。
		return nil, errors.Ef(errors.CodeExecInvalidTransition,
			"erp: 操作 %s 已被其他执行者推进，拒绝重复派发", operationID)
	}

	// 事务外远端调用。远端调用失败/超时绝不回滚账本，而是据实落 UNKNOWN。
	started := g.now()
	req := ExecuteRequest{
		TenantID:                tenantID.String(),
		ConnectionID:            ConnectionID(op.ExternalSystem),
		OperationID:             OperationID(op.ID.String()),
		OperationType:           op.Action,
		TargetObjectID:          ObjectID(op.ObjectID),
		Payload:                 clonePayload(op.PayloadSnapshot),
		PayloadDigest:           op.PayloadDigest,
		IdempotencyKey:          op.IdempotencyKey,
		ExpectedVersion:         op.ExpectedVersion,
		AuthorizationDecisionID: "",
		TraceID:                 op.ID.String(),
	}
	if op.ApprovalID != (ids.ID{}) {
		req.ApprovalID = op.ApprovalID.String()
	}
	result, execErr := port.Execute(ctx, req)
	res := classifyResult(op, level, result, execErr, started, g.now())

	finalStatus := ledgerStatusFor(res.Status)
	if _, err := st.UpdateStatus(ctx, tenantID, operationID, StatusTransition{
		From:        []string{LedgerDispatched, LedgerExecuting},
		To:          finalStatus,
		At:          g.now(),
		SetFinished: OperationStatus(res.Status).IsTerminal(),
	}); err != nil {
		return nil, err
	}

	receipt := &Receipt{
		ID:                    ids.New(),
		TenantID:              tenantID,
		OperationID:           operationID,
		ReceiptType:           ReceiptSubmit,
		ExternalOperationID:   res.RemoteRef,
		ExternalObjectVersion: res.ResultVersion,
		ResultStatus:          string(res.Status),
		ResponseDigest:        res.PayloadDigest,
		ResponsePayload:       receiptPayload(res),
		ReceivedAt:            g.now(),
	}
	if err := st.AppendReceipt(ctx, tenantID, receipt); err != nil {
		return nil, err
	}
	if res.ReceiptID == "" {
		res.ReceiptID = receipt.ID.String()
	}
	return res, nil
}

// GetOperation 读取账本中的操作。
func (g *OperationGateway) GetOperation(ctx context.Context, tenantID ids.ID, operationID ids.ID) (*Operation, error) {
	st, err := g.store()
	if err != nil {
		return nil, err
	}
	return st.GetByID(ctx, tenantID, operationID)
}

// Reconcile 消除未知结果（第七篇 §17.4）：
//
//	调 Port.GetOperation → 确认成功则 SUCCEEDED + 回执；确认未执行则 FAILED（可重试）；
//	仍无法确认则保持 UNKNOWN 并累计对账次数，超阈值转 MANUAL_REVIEW。
func (g *OperationGateway) Reconcile(ctx context.Context, tenantID ids.ID, operationID ids.ID) (*OperationResult, error) {
	st, err := g.store()
	if err != nil {
		return nil, err
	}
	op, err := st.GetByID(ctx, tenantID, operationID)
	if err != nil {
		return nil, err
	}
	// 已是终态：直接返回账本结论，不再打扰远端。
	if isLedgerTerminal(op.Status) {
		return resultFromLedger(op), nil
	}

	port, err := g.resolvePort(ctx, tenantID, op.ExternalSystem)
	if err != nil {
		return nil, err
	}
	level, err := g.idempotencyLevel(ctx, port, op)
	if err != nil {
		return nil, err
	}

	// 远端操作 ID 未知时（提交后响应丢失），退化为按幂等键查询。
	remoteRef := ""
	if r, err := st.LatestReceipt(ctx, tenantID, operationID, ReceiptSubmit); err == nil && r != nil {
		remoteRef = r.ExternalOperationID
	}
	started := g.now()
	remote, callErr := port.GetOperation(ctx, GetOperationRequest{
		TenantID:       tenantID.String(),
		ConnectionID:   ConnectionID(op.ExternalSystem),
		OperationID:    OperationID(remoteRef),
		IdempotencyKey: op.IdempotencyKey,
	})
	res := classifyResult(op, level, remote, callErr, started, g.now())

	switch res.Status {
	case StatusSucceeded, StatusFailed, StatusRejected:
		final := ledgerStatusFor(res.Status)
		if _, err := st.UpdateStatus(ctx, tenantID, operationID, StatusTransition{
			From:        reconcileFrom(),
			To:          final,
			At:          g.now(),
			SetFinished: true,
		}); err != nil {
			return nil, err
		}
		if res.Status == StatusFailed {
			// 对账确认为"未执行/失败"：按已确认未执行判定是否可安全重试（§8.3）。
			res.Retryable = CanAutoRetry(level, OutcomeNotExecuted)
		}
	default:
		// 仍无法确认：保持 UNKNOWN，仅记录对账回执并累计次数。
		res.Status = StatusUnknown
		res.Retryable = CanAutoRetry(level, OutcomeUnknown)
	}

	if err := st.AppendReceipt(ctx, tenantID, &Receipt{
		ID:                    ids.New(),
		TenantID:              tenantID,
		OperationID:           operationID,
		ReceiptType:           ReceiptReconcile,
		ExternalOperationID:   res.RemoteRef,
		ExternalObjectVersion: res.ResultVersion,
		ResultStatus:          string(res.Status),
		ResponseDigest:        res.PayloadDigest,
		ResponsePayload:       receiptPayload(res),
		ReceivedAt:            g.now(),
	}); err != nil {
		return nil, err
	}

	if res.Status == StatusUnknown || res.Status == StatusReconciling {
		// 保持 UNKNOWN；统计对账次数，超阈值转人工复核。
		count, err := st.CountReceipts(ctx, tenantID, operationID, ReceiptReconcile)
		if err != nil {
			return nil, err
		}
		if count >= g.threshold() {
			if _, err := st.UpdateStatus(ctx, tenantID, operationID, StatusTransition{
				From: reconcileFrom(),
				To:   LedgerManualReview,
				At:   g.now(),
			}); err != nil {
				return nil, err
			}
			res.Status = StatusManualReview
			res.Retryable = false
			return res, nil
		}
		if _, err := st.UpdateStatus(ctx, tenantID, operationID, StatusTransition{
			From: reconcileFrom(),
			To:   LedgerUnknown,
			At:   g.now(),
		}); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// ── 内部辅助 ────────────────────────────────────────────────────────────────

func (g *OperationGateway) resolvePort(ctx context.Context, tenantID ids.ID, externalSystem string) (Port, error) {
	if g.Ports == nil {
		return nil, errors.E(errors.CodeInternal, "erp: gateway 未配置 PortResolver")
	}
	port, err := g.Ports.Resolve(ctx, tenantID, externalSystem)
	if err != nil {
		return nil, err
	}
	if port == nil {
		return nil, errors.Ef(errors.CodeERPAdapterError,
			"erp: 外部系统 %q 没有可用的 Adapter", externalSystem)
	}
	return port, nil
}

func (g *OperationGateway) idempotencyLevel(ctx context.Context, port Port, op *Operation) (string, error) {
	caps, err := port.Capabilities(ctx, ConnectionID(op.ExternalSystem))
	if err != nil {
		return "", errors.Wrap(err, errors.CodeERPAdapterError, "erp: 读取连接能力失败")
	}
	return NormalizeLevel(caps.IdempotencyLevel), nil
}

// dispatchableStatuses 返回在当前幂等级别下允许派发的状态集合。
func dispatchableStatuses(level string) []string {
	from := []string{LedgerPending, LedgerReady}
	if CanAutoRetry(level, OutcomeNotExecuted) {
		// 已确认未执行：允许在 FAILED 上安全重试。
		from = append(from, LedgerFailed)
	}
	if CanAutoRetry(level, OutcomeUnknown) {
		// 原生幂等（L1）：结果未知/在途亦可同键重放，由远端去重拦截。
		from = append(from, LedgerUnknown, LedgerReconciling, LedgerDispatched, LedgerExecuting)
	}
	return from
}

// reconcileFrom 返回允许被对账收敛的前置状态集合。
func reconcileFrom() []string {
	return []string{LedgerDispatched, LedgerExecuting, LedgerUnknown, LedgerReconciling}
}

func classifyResult(op *Operation, level string, r OperationResult, err error, started, finished time.Time) *OperationResult {
	if err == nil {
		if r.Status == "" {
			r.Status = StatusSucceeded
		}
		if r.OperationID == "" {
			r.OperationID = OperationID(op.ID.String())
		}
		if r.PayloadDigest == "" {
			r.PayloadDigest = op.PayloadDigest
		}
		r.StartedAt = &started
		r.CompletedAt = &finished
		if r.Status == StatusUnknown {
			r.Retryable = CanAutoRetry(level, OutcomeUnknown)
		}
		return &r
	}

	ae := errors.As(err)
	res := &OperationResult{
		OperationID:   OperationID(op.ID.String()),
		PayloadDigest: op.PayloadDigest,
		StartedAt:     &started,
		CompletedAt:   &finished,
		Error: &ERPError{
			Code:      ae.Code,
			Message:   ae.Message,
			Retryable: ae.Retryable(),
		},
	}
	switch ae.Code {
	case errors.CodeValidationFailed, errors.CodePermissionDenied, errors.CodeNotFound:
		// 明确未执行（校验/权限/对象不存在）：可判定为 FAILED 且未产生副作用。
		res.Status = StatusFailed
		res.Retryable = CanAutoRetry(level, OutcomeNotExecuted)
	default:
		// 其余（超时、连接中断、适配器错误等）一律视为结果未知，绝不静默当失败。
		res.Status = StatusUnknown
		res.Retryable = CanAutoRetry(level, OutcomeUnknown)
	}
	return res
}

// ledgerStatusFor 把远端结果状态映射为账本状态。
func ledgerStatusFor(s OperationStatus) string {
	switch s {
	case StatusSucceeded:
		return LedgerSucceeded
	case StatusFailed:
		return LedgerFailed
	case StatusRejected:
		return LedgerRejected
	case StatusUnknown:
		return LedgerUnknown
	case StatusReconciling:
		return LedgerReconciling
	case StatusManualReview:
		return LedgerManualReview
	case StatusAccepted, StatusInProgress:
		return LedgerExecuting
	default:
		return LedgerUnknown
	}
}

func isLedgerTerminal(status string) bool {
	switch status {
	case LedgerSucceeded, LedgerFailed, LedgerRejected:
		return true
	default:
		return false
	}
}

// resultFromLedger 由账本终态还原一个结果快照。
func resultFromLedger(op *Operation) *OperationResult {
	res := &OperationResult{
		OperationID:   OperationID(op.ID.String()),
		PayloadDigest: op.PayloadDigest,
		Retryable:     false,
	}
	switch op.Status {
	case LedgerSucceeded:
		res.Status = StatusSucceeded
	case LedgerFailed:
		res.Status = StatusFailed
	case LedgerRejected:
		res.Status = StatusRejected
	default:
		res.Status = StatusUnknown
	}
	if op.FinishedAt != nil {
		res.CompletedAt = op.FinishedAt
	}
	return res
}

func receiptPayload(res *OperationResult) map[string]any {
	p := map[string]any{
		"status":         string(res.Status),
		"operation_id":   string(res.OperationID),
		"payload_digest": res.PayloadDigest,
	}
	if res.RemoteRef != "" {
		p["remote_ref"] = res.RemoteRef
	}
	if res.ReceiptID != "" {
		p["receipt_id"] = res.ReceiptID
	}
	if res.ResultVersion != "" {
		p["result_version"] = res.ResultVersion
	}
	if res.Error != nil {
		errObj := map[string]any{
			"code":      res.Error.Code,
			"message":   res.Error.Message,
			"retryable": res.Error.Retryable,
		}
		if len(res.Error.Details) > 0 {
			errObj["details"] = res.Error.Details
		}
		p["error"] = errObj
	}
	return p
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func clonePayload(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ── 规范化 payload 摘要 ─────────────────────────────────────────────────────

// PayloadDigest 返回规范化 payload 的 SHA-256（hex）。
// nil 视为空对象 {}。
func PayloadDigest(payload map[string]any) (string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON 把任意 JSON 值编码为规范化字节序列：
//   - 对象键按字典序排序（确定性）；
//   - 数字统一为最简十进制形式（消除 1、1.0、1e0 的歧义）；
//   - 不输出任何多余空白。
//
// 摘要必须由可信服务按规范化规则计算，不能直接相信 Agent 提交的摘要（第七篇 §3.3）。
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSONString(buf, t)
	case float64:
		buf.WriteString(canonicalNumber(t))
	case float32:
		buf.WriteString(canonicalNumber(float64(t)))
	case int:
		buf.WriteString(strconv.FormatInt(int64(t), 10))
	case int32:
		buf.WriteString(strconv.FormatInt(int64(t), 10))
	case int64:
		buf.WriteString(strconv.FormatInt(t, 10))
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return err
		}
		buf.WriteString(canonicalNumber(f))
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		// 兜底：encoding/json 对结构体与 map 同样按键序输出，结果确定。
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}

func canonicalNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func writeJSONString(buf *bytes.Buffer, s string) {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal 对 string 不会失败（非法 UTF-8 也会被替换）。
		buf.WriteString(`""`)
		return
	}
	buf.Write(b)
}
