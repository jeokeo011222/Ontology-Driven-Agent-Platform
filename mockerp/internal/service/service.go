// Package service 实现 Mock ERP 的业务逻辑：对象读写、命令执行、
// 状态跃迁、版本检查、幂等去重与故障注入（第七篇 §9.2）。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"odap/internal/platform/ids"
	"odap/mockerp/internal/domain"
	"odap/mockerp/internal/repository"
)

// 故障注入类型（第七篇 §11.1）。
const (
	FaultTimeout   = "timeout"
	FaultReject    = "reject"
	FaultUnknown   = "unknown"
	FaultDuplicate = "duplicate"
	FaultDelay     = "delay"
)

// Service 是 Mock ERP 的应用服务。
type Service struct {
	repo repository.Repository

	mu     sync.Mutex
	faults []domain.Fault
	seq    int
}

// New 创建服务。
func New(repo repository.Repository) *Service {
	return &Service{repo: repo}
}

// CreateObjectInput 是创建对象请求。
type CreateObjectInput struct {
	ObjectType string
	ObjectID   string
	Status     string
	Attributes map[string]any
}

// CreateObject 创建一个业务对象；未指定 ID 时自动生成。
func (s *Service) CreateObject(ctx context.Context, in CreateObjectInput) (*domain.Object, error) {
	if in.ObjectType == "" {
		return nil, domain.ErrInvalidRequest
	}
	objectID := in.ObjectID
	if objectID == "" {
		objectID = "obj-" + ids.New().String()
	}
	status := in.Status
	if status == "" {
		if v, ok := in.Attributes["status"].(string); ok && v != "" {
			status = v
		} else {
			status = domain.StatusDraft
		}
	}
	now := time.Now().UTC()
	obj := &domain.Object{
		ObjectType: in.ObjectType,
		ObjectID:   objectID,
		Status:     status,
		Version:    1,
		Attributes: in.Attributes,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.repo.CreateObject(ctx, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// GetObject 读取对象。
func (s *Service) GetObject(ctx context.Context, objectType, objectID string) (*domain.Object, error) {
	return s.repo.GetObject(ctx, objectType, objectID)
}

// QueryRequest 是查询请求。
type QueryRequest struct {
	ObjectType string
	Status     string
	Filter     *Filter
	Limit      int
}

// Filter 是受控过滤条件（MVP 仅支持 eq/contains）。
type Filter struct {
	Field string
	Op    string
	Value any
}

// QueryObjects 按类型返回对象列表（受控过滤 + 上限）。
func (s *Service) QueryObjects(ctx context.Context, req QueryRequest) ([]*domain.Object, bool, error) {
	if req.ObjectType == "" {
		return nil, false, domain.ErrInvalidRequest
	}
	all, err := s.repo.ListObjects(ctx, req.ObjectType)
	if err != nil {
		return nil, false, err
	}
	out := make([]*domain.Object, 0, len(all))
	for _, obj := range all {
		if req.Status != "" && !strings.EqualFold(obj.Status, req.Status) {
			continue
		}
		if req.Filter != nil && !matchFilter(obj, req.Filter) {
			continue
		}
		out = append(out, obj)
	}
	hasMore := false
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
		hasMore = true
	}
	return out, hasMore, nil
}

func matchFilter(obj *domain.Object, f *Filter) bool {
	var got any
	switch f.Field {
	case "status":
		got = obj.Status
	default:
		got = obj.Attributes[f.Field]
	}
	switch f.Op {
	case "eq":
		return fmt.Sprint(got) == fmt.Sprint(f.Value)
	case "contains":
		return strings.Contains(fmt.Sprint(got), fmt.Sprint(f.Value))
	default:
		return false
	}
}

// Command 是一次写命令。
type Command struct {
	Operation       string
	ObjectType      string
	ObjectID        string
	Payload         map[string]any
	ExpectedVersion string
	IdempotencyKey  string
}

// ExecOutcome 是命令执行结果。
type ExecOutcome struct {
	Operation *domain.Operation
	Status    string
	Replay    bool
	// ResponseDropped 表示"已提交但响应丢失"（timeout 故障），对应 §11.2 场景。
	ResponseDropped bool
}

// Execute 执行命令：幂等去重 → 故障注入 → 状态跃迁 → 记录操作。
func (s *Service) Execute(ctx context.Context, cmd Command) (*ExecOutcome, error) {
	if cmd.Operation == "" || cmd.ObjectID == "" {
		return nil, domain.ErrInvalidRequest
	}
	digest := requestDigest(cmd)
	fault := s.matchFault(cmd.Operation)
	dup := fault != nil && fault.Kind == FaultDuplicate

	// 幂等去重：同键同摘要返回同一结果；同键不同摘要返回冲突（第七篇 §10.3）。
	// duplicate 故障刻意跳过去重，用于模拟远端重复接收。
	if !dup && cmd.IdempotencyKey != "" {
		existing, err := s.repo.GetOperationByIdempotencyKey(ctx, cmd.IdempotencyKey)
		switch {
		case err == nil:
			if existing.RequestDigest != digest {
				return nil, domain.ErrIdempotencyConflict
			}
			return &ExecOutcome{Operation: existing, Status: existing.ResponseStatus, Replay: true}, nil
		case !stderrors.Is(err, domain.ErrOperationNotFound):
			return nil, err
		}
	}

	if fault != nil {
		if fault.Kind == FaultDelay && fault.DelayMS > 0 {
			select {
			case <-time.After(time.Duration(fault.DelayMS) * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if fault.Kind == FaultReject {
			// 业务拒绝：不产生任何副作用。
			return nil, domain.ErrRejected
		}
	}

	obj, err := s.applyCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}

	externalID := fmt.Sprintf("op-%06d", s.nextSeq())
	op := &domain.Operation{
		ID:                  externalID,
		IdempotencyKey:      cmd.IdempotencyKey,
		Operation:           cmd.Operation,
		ObjectType:          obj.ObjectType,
		ObjectID:            obj.ObjectID,
		RequestDigest:       digest,
		ResponseStatus:      domain.OpStatusSucceeded,
		ExternalOperationID: "ERP-" + externalID,
		ReceivedAt:          time.Now().UTC(),
		ResponsePayload: map[string]any{
			"object_type": obj.ObjectType,
			"object_id":   obj.ObjectID,
			"status":      obj.Status,
			"version":     obj.Version,
		},
	}

	// 先落远端操作流水，再决定响应形态（模拟"已提交"）。
	dropped := false
	if fault != nil && (fault.Kind == FaultTimeout || fault.Kind == FaultUnknown) {
		op.ResponseStatus = domain.OpStatusUnknown
		dropped = fault.Kind == FaultTimeout
	}
	if err := s.repo.PutOperation(ctx, op); err != nil {
		return nil, err
	}
	return &ExecOutcome{Operation: op, Status: op.ResponseStatus, ResponseDropped: dropped}, nil
}

func (s *Service) applyCommand(ctx context.Context, cmd Command) (*domain.Object, error) {
	objectType := cmd.ObjectType
	if objectType == "" {
		found, err := s.repo.FindObjectAnyType(ctx, cmd.ObjectID)
		if err != nil {
			return nil, err
		}
		objectType = found.ObjectType
	}
	obj, err := s.repo.GetObject(ctx, objectType, cmd.ObjectID)
	if err != nil {
		return nil, err
	}
	if cmd.ExpectedVersion != "" {
		want, perr := strconv.ParseInt(strings.Trim(cmd.ExpectedVersion, `"`), 10, 64)
		if perr != nil || obj.Version != want {
			return nil, domain.ErrVersionConflict
		}
	}
	next := obj.Clone()
	if err := transition(next, cmd); err != nil {
		return nil, err
	}
	next.Version = obj.Version + 1
	next.UpdatedAt = time.Now().UTC()
	if err := s.repo.UpdateObject(ctx, next); err != nil {
		return nil, err
	}
	return next, nil
}

// transition 是显式状态机：数据库 CHECK 只能约束单字段取值，
// 不能取代完整业务跃迁规则（第七篇 §9.5）。
func transition(obj *domain.Object, cmd Command) error {
	switch {
	case strings.HasSuffix(cmd.Operation, ".submit"):
		if obj.Status != domain.StatusDraft {
			return domain.ErrInvalidTransition
		}
		obj.Status = domain.StatusPendingApproval
	case strings.HasSuffix(cmd.Operation, ".approve"):
		if obj.Status != domain.StatusPendingApproval {
			return domain.ErrInvalidTransition
		}
		obj.Status = domain.StatusApproved
	case strings.HasSuffix(cmd.Operation, ".reject"):
		if obj.Status != domain.StatusPendingApproval {
			return domain.ErrInvalidTransition
		}
		obj.Status = domain.StatusRejected
	case strings.HasSuffix(cmd.Operation, ".cancel"):
		if obj.Status != domain.StatusDraft && obj.Status != domain.StatusPendingApproval {
			return domain.ErrInvalidTransition
		}
		obj.Status = domain.StatusCancelled
	default:
		// 通用属性更新（如 expense.create_draft）。
		if obj.Attributes == nil {
			obj.Attributes = map[string]any{}
		}
		for k, v := range cmd.Payload {
			obj.Attributes[k] = v
		}
	}
	return nil
}

// GetOperation 按远端操作 ID 查询。
func (s *Service) GetOperation(ctx context.Context, id string) (*domain.Operation, error) {
	return s.repo.GetOperation(ctx, id)
}

// GetOperationByIdempotencyKey 按幂等键查询（对账路径：提交响应丢失后使用）。
func (s *Service) GetOperationByIdempotencyKey(ctx context.Context, key string) (*domain.Operation, error) {
	return s.repo.GetOperationByIdempotencyKey(ctx, key)
}

// SetFaults 配置故障注入规则；clear 为 true 时先清空既有规则。
func (s *Service) SetFaults(faults []domain.Fault, clear bool) []domain.Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	if clear {
		s.faults = nil
	}
	s.faults = append(s.faults, faults...)
	out := make([]domain.Fault, len(s.faults))
	copy(out, s.faults)
	return out
}

// ListFaults 返回当前故障规则快照。
func (s *Service) ListFaults() []domain.Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Fault, len(s.faults))
	copy(out, s.faults)
	return out
}

func (s *Service) matchFault(operation string) *domain.Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.faults {
		if s.faults[i].OperationType == "" || s.faults[i].OperationType == operation {
			f := s.faults[i]
			return &f
		}
	}
	return nil
}

func (s *Service) nextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Health 返回服务健康状态。
type Health struct {
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
}

// Health 恒为健康（供 /mock/health 与 Adapter 健康检查使用）。
func (s *Service) Health() Health {
	return Health{Status: "ok", Time: time.Now().UTC()}
}

// requestDigest 计算请求摘要：结构体字段序固定，payload 为 map（encoding/json 按键序输出），
// 因此结果确定，可安全用于幂等去重比较。
func requestDigest(cmd Command) string {
	type digestInput struct {
		Operation       string         `json:"operation"`
		ObjectType      string         `json:"object_type"`
		ObjectID        string         `json:"object_id"`
		Payload         map[string]any `json:"payload"`
		ExpectedVersion string         `json:"expected_version"`
	}
	b, err := json.Marshal(digestInput{
		Operation:       cmd.Operation,
		ObjectType:      cmd.ObjectType,
		ObjectID:        cmd.ObjectID,
		Payload:         cmd.Payload,
		ExpectedVersion: cmd.ExpectedVersion,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
