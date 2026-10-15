// Package erp 定义 ODAP 统一 ERP Port 的接口契约与领域类型（第七篇 §2、§3）。
//
// 职责边界（第七篇 §1.3）：
//   - Port 只声明"平台需要什么业务能力"，不依赖任何具体 ERP 的私有协议；
//   - Adapter 负责把 Port 能力映射到真实系统的协议、字段与状态；
//   - Operation Gateway 负责校验、幂等、账本与执行边界，禁止绕过授权直接写入 ERP。
//
// 本包只承载契约与纯领域判定，不做持久化、不做远端调用。
package erp

import (
	"context"
	"time"

	"odap/internal/platform/errors"
)

// ConnectionID 标识一条已配置的 ERP 连接（第七篇 §4.3）。
type ConnectionID string

// ObjectID 是规范对象在目标 ERP 中的标识。
type ObjectID string

// OperationID 是远端操作的标识；本地操作主键见 integration.erp_operations.id。
type OperationID string

// OperationStatus 是远端操作结果状态集（第七篇 §3.4）。
//
// 语义要点：
//   - ACCEPTED 表示对方已接受请求/任务，不等于业务已完成；
//   - REJECTED 表示操作被业务明确拒绝，而非普通网络错误；
//   - UNKNOWN 表示当前无法确认外部操作的最终结果，是一等状态，
//     绝不能被静默降级为 FAILED。
type OperationStatus string

const (
	StatusAccepted     OperationStatus = "ACCEPTED"
	StatusInProgress   OperationStatus = "IN_PROGRESS"
	StatusSucceeded    OperationStatus = "SUCCEEDED"
	StatusFailed       OperationStatus = "FAILED"
	StatusRejected     OperationStatus = "REJECTED"
	StatusUnknown      OperationStatus = "UNKNOWN"
	StatusReconciling  OperationStatus = "RECONCILING"
	StatusManualReview OperationStatus = "MANUAL_REVIEW"
)

// IsTerminal 报告该远端状态是否为终态（可写 finished_at）。
func (s OperationStatus) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusRejected:
		return true
	default:
		return false
	}
}

// ERPError 是 Adapter 统一错误模型（第七篇 §16）。
// 结构化错误码让 Harness 能采取确定性策略，而不是让模型根据自由文本猜测是否重试。
type ERPError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// Capabilities 是当前连接实际支持的能力声明（第七篇 §4.2）。
//
// 能力声明由 Adapter 注册、管理员审核并经过连接测试；Agent 不得自行声明
// 某个 ERP 支持幂等、回滚或操作查询。
type Capabilities struct {
	ConnectionID   string `json:"connection_id"`
	AdapterID      string `json:"adapter_id"`
	AdapterVersion string `json:"adapter_version"`

	// Objects 是支持的对象类型；Operations 是支持的业务命令（如 expense.submit）。
	Objects    []string `json:"objects"`
	Operations []string `json:"operations"`

	// IdempotencyLevel 是远端幂等能力分级 L1|L2|L3（第七篇 §8.3）。
	IdempotencyLevel string `json:"idempotency_level"`
	// SupportsVersionCheck 报告是否支持可靠的对象版本校验（ETag/版本号/乐观锁）。
	SupportsVersionCheck bool `json:"supports_version_check"`
	// SupportsQuery 报告操作结果查询能力：
	// none | by_operation_id | by_idempotency_key | full。
	SupportsQuery string `json:"supports_query"`

	// SupportsOperationLookup 报告是否可查询操作执行结果（§4.2 features.operation_lookup）。
	SupportsOperationLookup bool `json:"supports_operation_lookup"`
	// SupportsReceiptVerification 报告是否可核验回执（§4.2 features.receipt_verification）。
	SupportsReceiptVerification bool `json:"supports_receipt_verification"`
	// SupportsRemoteApproval 报告远端是否具备审批工作流（§4.2 features.remote_approval）。
	SupportsRemoteApproval bool `json:"supports_remote_approval"`
}

// GetObjectRequest 是单对象读取请求（第七篇 §3.2）。
type GetObjectRequest struct {
	TenantID     string
	ConnectionID ConnectionID
	ObjectType   string
	ObjectID     ObjectID
	Fields       []string
}

// ObjectResult 是单对象读取结果。
type ObjectResult struct {
	Object     CanonicalObject
	Version    string
	ObservedAt time.Time
}

// QueryObjectsRequest 是受控查询请求（第七篇 §3.2）。
// 不接受任意原生查询语言，仅接受白名单算子与字段。
type QueryObjectsRequest struct {
	TenantID     string
	ConnectionID ConnectionID
	ObjectType   string
	Filter       FilterExpression
	Fields       []string
	PageSize     int
	Cursor       string
	OrderBy      []OrderClause
}

// QueryResult 是查询响应。
type QueryResult struct {
	Items      []CanonicalObject
	NextCursor string
	HasMore    bool
}

// ExecuteRequest 是命令型写请求（第七篇 §3.3）。
type ExecuteRequest struct {
	TenantID     string
	ConnectionID ConnectionID
	OperationID  OperationID

	OperationType  string
	TargetObjectID ObjectID

	Payload       map[string]any
	PayloadDigest string

	IdempotencyKey  string
	ExpectedVersion string

	AuthorizationDecisionID string
	ApprovalID              string

	Deadline time.Time
	TraceID  string
}

// GetOperationRequest 是操作结果查询请求。
// 远端操作 ID 未知时（例如提交后响应丢失），可退化为按幂等键查询。
type GetOperationRequest struct {
	TenantID       string
	ConnectionID   ConnectionID
	OperationID    OperationID
	IdempotencyKey string
}

// OperationResult 是远端操作结果（第七篇 §3.4）。
// 调用结果（是否成功送达）与业务操作结果被刻意分开表达。
type OperationResult struct {
	OperationID OperationID
	Status      OperationStatus

	RemoteRef string
	ReceiptID string

	PayloadDigest string
	ResultVersion string

	Retryable bool
	Error     *ERPError

	StartedAt   *time.Time
	CompletedAt *time.Time
}

// HealthResult 是连接健康检查结果（第七篇 §4.4）。
type HealthResult struct {
	ConnectionID string
	Status       string
	Healthy      bool
	CheckedAt    time.Time
	Message      string
}

// 健康状态取值。
const (
	HealthHealthy     = "HEALTHY"
	HealthDegraded    = "DEGRADED"
	HealthUnavailable = "UNAVAILABLE"
)

// ExternalRef 指向 ERP 原生对象（第七篇 §2.4）。
type ExternalRef struct {
	ConnectionID     string `json:"connection_id"`
	NativeObjectType string `json:"native_object_type"`
	NativeID         string `json:"native_id"`
}

// 数据质量取值（第七篇 §2.4）。不能把缺失字段默认填充后标记为 VERIFIED。
const (
	DataQualityVerified  = "VERIFIED"
	DataQualityPartial   = "PARTIAL"
	DataQualityStale     = "STALE"
	DataQualityConflict  = "CONFLICT"
	DataQualityUnmapped  = "UNMAPPED"
	DataQualityDefaulted = "DEFAULTED"
)

// CanonicalObject 是 ODAP 规范业务对象模型的公共外壳（第七篇 §2.3/§2.4）。
// 核心字段有明确类型与语义；与任务无关的扩展字段放入 Fields。
type CanonicalObject struct {
	ObjectType     string         `json:"object_type"`
	ObjectID       string         `json:"object_id"`
	TenantID       string         `json:"tenant_id"`
	ExternalRef    ExternalRef    `json:"external_ref"`
	SourceVersion  string         `json:"source_version"`
	MappingVersion string         `json:"mapping_version"`
	ObservedAt     time.Time      `json:"observed_at"`
	DataQuality    string         `json:"data_quality"`
	Fields         map[string]any `json:"fields,omitempty"`
}

// FilterExpression 是受控查询过滤表达式（第七篇 §3.2）。
// Op 只允许白名单算子，非白名单算子在校验时返回 CodeValidationFailed。
type FilterExpression struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

// OrderClause 是查询排序子句（第七篇 §3.2）。
type OrderClause struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc"`
}

// filterOps 是受控过滤算子白名单。
var filterOps = map[string]bool{
	"eq":       true,
	"ne":       true,
	"in":       true,
	"not_in":   true,
	"gt":       true,
	"gte":      true,
	"lt":       true,
	"lte":      true,
	"like":     true,
	"contains": true,
}

// ValidFilterOp 报告 op 是否在受控白名单内。
func ValidFilterOp(op string) bool { return filterOps[op] }

// FilterOps 返回白名单算子的稳定有序列表（供能力发现与文档生成）。
func FilterOps() []string {
	return []string{"eq", "ne", "in", "not_in", "gt", "gte", "lt", "lte", "like", "contains"}
}

// Validate 校验过滤表达式。空表达式视为不过滤（合法）。
func (f FilterExpression) Validate() error {
	if f.Field == "" && f.Op == "" {
		return nil // 未设置过滤条件
	}
	if f.Field == "" {
		return errors.E(errors.CodeValidationFailed, "erp: 过滤表达式缺少 field")
	}
	if !ValidFilterOp(f.Op) {
		return errors.Ef(errors.CodeValidationFailed,
			"erp: 非白名单过滤算子 %q（允许：eq/ne/in/not_in/gt/gte/lt/lte/like/contains）", f.Op)
	}
	return nil
}

// Validate 校验查询请求：受控算子白名单 + 基本结构。
func (r QueryObjectsRequest) Validate() error {
	if r.ObjectType == "" {
		return errors.E(errors.CodeValidationFailed, "erp: 查询缺少 object_type")
	}
	if r.PageSize < 0 {
		return errors.E(errors.CodeValidationFailed, "erp: page_size 不能为负数")
	}
	return r.Filter.Validate()
}

// Port 是平台级统一 ERP 接口（第七篇 §3.1）。
// 不要求所有 ERP 原生提供同名 API；Adapter 可编排多个远端接口完成一个业务动作，
// 但必须显式声明中间状态与副作用，不得把多个外部调用伪装成原子事务。
type Port interface {
	// Capabilities 返回当前连接的已审核能力声明。
	Capabilities(ctx context.Context, connectionID ConnectionID) (Capabilities, error)
	// GetObject 读取单个规范对象。
	GetObject(ctx context.Context, req GetObjectRequest) (ObjectResult, error)
	// QueryObjects 执行受控查询。
	QueryObjects(ctx context.Context, req QueryObjectsRequest) (QueryResult, error)
	// Execute 执行一个明确的业务命令（写操作）。
	Execute(ctx context.Context, req ExecuteRequest) (OperationResult, error)
	// GetOperation 查询某个远端操作的执行结果（用于对账）。
	GetOperation(ctx context.Context, req GetOperationRequest) (OperationResult, error)
	// Health 返回连接健康检查结果。
	Health(ctx context.Context, connectionID ConnectionID) (HealthResult, error)
}
