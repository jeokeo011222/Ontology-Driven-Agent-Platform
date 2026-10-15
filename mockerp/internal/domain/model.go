// Package domain 定义 Mock ERP 的业务对象、操作流水与故障规则（第七篇 §9）。
//
// 边界：Mock ERP 是独立业务事实来源，其业务数据与 ODAP 平台的操作账本物理分离
// （决策 B）。本包不依赖 ODAP 平台内部包，保持服务可独立部署。
package domain

import (
	stderrors "errors"
	"time"
)

// 业务对象状态（对齐 mockerp/migrations/000001_init.sql 的 CHECK 约束）。
const (
	StatusDraft           = "DRAFT"
	StatusSubmitted       = "SUBMITTED"
	StatusPendingApproval = "PENDING_APPROVAL"
	StatusApproved        = "APPROVED"
	StatusRejected        = "REJECTED"
	StatusPaid            = "PAID"
	StatusCancelled       = "CANCELLED"
)

// 远端操作结果状态（与 ODAP Port 的 OperationStatus 语义一致）。
const (
	OpStatusSucceeded = "SUCCEEDED"
	OpStatusFailed    = "FAILED"
	OpStatusRejected  = "REJECTED"
	OpStatusUnknown   = "UNKNOWN"
)

// 领域错误哨兵。Handler 依据它们映射 HTTP 状态码。
var (
	ErrObjectNotFound      = stderrors.New("mock-erp: 对象不存在")
	ErrObjectExists        = stderrors.New("mock-erp: 对象已存在")
	ErrVersionConflict     = stderrors.New("mock-erp: 对象版本冲突")
	ErrIdempotencyConflict = stderrors.New("mock-erp: 幂等键已绑定到不同的请求")
	ErrInvalidTransition   = stderrors.New("mock-erp: 非法状态跃迁")
	ErrInvalidRequest      = stderrors.New("mock-erp: 请求参数非法")
	ErrRejected            = stderrors.New("mock-erp: 业务操作被拒绝")
	ErrOperationNotFound   = stderrors.New("mock-erp: 操作不存在")
)

// Object 是 Mock ERP 中的一个业务对象（对应 erp.expense_claims 等的通用化外壳）。
type Object struct {
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id"`
	Status     string         `json:"status,omitempty"`
	Version    int64          `json:"version"`
	Attributes map[string]any `json:"attributes,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// Clone 返回对象深拷贝（Attributes 浅拷贝足够，值语义由调用方保证）。
func (o *Object) Clone() *Object {
	if o == nil {
		return nil
	}
	cp := *o
	if o.Attributes != nil {
		cp.Attributes = make(map[string]any, len(o.Attributes))
		for k, v := range o.Attributes {
			cp.Attributes[k] = v
		}
	}
	return &cp
}

// Operation 是 Mock ERP 自身对已接收操作的记录（对应 erp.operation_log）。
// 它记录"模拟远端实际接收并执行了什么"，与 ODAP 的集成账本互不复用。
type Operation struct {
	ID                  string         `json:"operation_id"`
	IdempotencyKey      string         `json:"idempotency_key,omitempty"`
	Operation           string         `json:"operation"`
	ObjectType          string         `json:"object_type"`
	ObjectID            string         `json:"object_id"`
	RequestDigest       string         `json:"request_digest"`
	ResponseStatus      string         `json:"status"`
	ResponsePayload     map[string]any `json:"response_payload,omitempty"`
	ExternalOperationID string         `json:"external_operation_id"`
	ReceivedAt          time.Time      `json:"received_at"`
}

// Fault 是一条故障注入规则；OperationType 为空表示全局生效（第七篇 §11）。
type Fault struct {
	Kind          string `json:"kind"`
	OperationType string `json:"operation_type,omitempty"`
	DelayMS       int    `json:"delay_ms,omitempty"`
}
