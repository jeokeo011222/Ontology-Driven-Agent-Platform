// Package repository 提供 Mock ERP 的存储抽象与内存实现。
//
// 内存实现优先（第七篇 §9.3 MVP 可以先用内存）；若切换 PostgreSQL，
// 只需按 mockerp/migrations/000001_init.sql 的 schema 另写一份 Repository 实现，
// 连接串取 MOCKERP_DATABASE_URL，上层 service/api 无需改动。
package repository

import (
	"context"
	"sync"

	"odap/mockerp/internal/domain"
)

// Repository 是 Mock ERP 的存储接口。
type Repository interface {
	CreateObject(ctx context.Context, obj *domain.Object) error
	GetObject(ctx context.Context, objectType, objectID string) (*domain.Object, error)
	// FindObjectAnyType 在未知 object_type 时按 ID 反查对象（用于类型推断）。
	FindObjectAnyType(ctx context.Context, objectID string) (*domain.Object, error)
	ListObjects(ctx context.Context, objectType string) ([]*domain.Object, error)
	UpdateObject(ctx context.Context, obj *domain.Object) error

	PutOperation(ctx context.Context, op *domain.Operation) error
	GetOperation(ctx context.Context, id string) (*domain.Operation, error)
	GetOperationByIdempotencyKey(ctx context.Context, key string) (*domain.Operation, error)
}

// Memory 是 Repository 的进程内实现。
type Memory struct {
	mu       sync.RWMutex
	objects  map[string]map[string]*domain.Object // objectType -> objectID -> object
	ops      map[string]*domain.Operation         // operationID -> operation
	byKey    map[string]string                    // idempotency key -> operationID（首次写入者胜出）
	opByCode map[string]string                    // operation code -> operationID
}

// NewMemory 创建内存仓储。
func NewMemory() *Memory {
	return &Memory{
		objects:  map[string]map[string]*domain.Object{},
		ops:      map[string]*domain.Operation{},
		byKey:    map[string]string{},
		opByCode: map[string]string{},
	}
}

// CreateObject 创建对象；同类型同 ID 已存在则报冲突。
func (m *Memory) CreateObject(_ context.Context, obj *domain.Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.objects[obj.ObjectType]
	if bucket == nil {
		bucket = map[string]*domain.Object{}
		m.objects[obj.ObjectType] = bucket
	}
	if _, exists := bucket[obj.ObjectID]; exists {
		return domain.ErrObjectExists
	}
	bucket[obj.ObjectID] = obj.Clone()
	return nil
}

// GetObject 按类型与 ID 读取对象。
func (m *Memory) GetObject(_ context.Context, objectType, objectID string) (*domain.Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[objectType][objectID]
	if !ok {
		return nil, domain.ErrObjectNotFound
	}
	return obj.Clone(), nil
}

// FindObjectAnyType 跨类型按 ID 反查对象。
func (m *Memory) FindObjectAnyType(_ context.Context, objectID string) (*domain.Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, bucket := range m.objects {
		if obj, ok := bucket[objectID]; ok {
			return obj.Clone(), nil
		}
	}
	return nil, domain.ErrObjectNotFound
}

// ListObjects 列出某类型的全部对象。
func (m *Memory) ListObjects(_ context.Context, objectType string) ([]*domain.Object, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*domain.Object, 0, len(m.objects[objectType]))
	for _, obj := range m.objects[objectType] {
		out = append(out, obj.Clone())
	}
	return out, nil
}

// UpdateObject 覆盖写入对象（乐观并发由 service 层的版本检查负责）。
func (m *Memory) UpdateObject(_ context.Context, obj *domain.Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	bucket := m.objects[obj.ObjectType]
	if bucket == nil {
		return domain.ErrObjectNotFound
	}
	if _, ok := bucket[obj.ObjectID]; !ok {
		return domain.ErrObjectNotFound
	}
	bucket[obj.ObjectID] = obj.Clone()
	return nil
}

// PutOperation 记录一条远端操作；同幂等键的首次写入者胜出（供查询）。
func (m *Memory) PutOperation(_ context.Context, op *domain.Operation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *op
	m.ops[op.ID] = &cp
	m.opByCode[op.ID] = op.ID
	if op.IdempotencyKey != "" {
		if _, exists := m.byKey[op.IdempotencyKey]; !exists {
			m.byKey[op.IdempotencyKey] = op.ID
		}
	}
	return nil
}

// GetOperation 按远端操作 ID 读取。
func (m *Memory) GetOperation(_ context.Context, id string) (*domain.Operation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	op, ok := m.ops[id]
	if !ok {
		return nil, domain.ErrOperationNotFound
	}
	return op, nil
}

// GetOperationByIdempotencyKey 按幂等键读取（返回首次写入的操作）。
func (m *Memory) GetOperationByIdempotencyKey(_ context.Context, key string) (*domain.Operation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byKey[key]
	if !ok {
		return nil, domain.ErrOperationNotFound
	}
	op, ok := m.ops[id]
	if !ok {
		return nil, domain.ErrOperationNotFound
	}
	return op, nil
}
