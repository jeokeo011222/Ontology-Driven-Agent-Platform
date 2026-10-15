// Package mock 提供两种 Mock ERP Adapter 实现（第七篇 §9/§10/§15.1）：
//
//   - HTTPAdapter：通过 REST 调用独立运行的 Mock ERP 服务（cmd/mock-erp）；
//   - InProcessAdapter：纯内存实现，供单元测试与契约测试注入，不依赖任何外部服务。
//
// Adapter 只负责协议转换、字段映射与远端调用，绝不自行批准业务操作。
package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"odap/internal/erp"
	"odap/internal/platform/errors"
)

// 故障注入类型（第七篇 §11.1），与 Mock ERP 服务的故障名保持一致。
const (
	FaultTimeout   = "timeout"   // 已执行副作用但响应丢失/超时
	FaultReject    = "reject"    // 业务拒绝，不产生副作用
	FaultUnknown   = "unknown"   // 已执行但结果对客户端未知
	FaultDuplicate = "duplicate" // 跳过幂等去重，强制重复接收
	FaultDelay     = "delay"     // 延迟返回
)

// Fault 描述一条故障注入规则；OperationType 为空表示全局生效。
type Fault struct {
	Kind          string
	OperationType string
	Delay         time.Duration
}

func (f Fault) matches(operationType string) bool {
	return f.OperationType == "" || f.OperationType == operationType
}

// ── InProcessAdapter ───────────────────────────────────────────────────────

// InProcessAdapter 是内存版 Mock ERP，实现 erp.Port。
// 业务数据与操作账本都保存在进程内存中，仅用于测试。
type InProcessAdapter struct {
	mu      sync.Mutex
	objects map[string]map[string]erp.CanonicalObject // objectType -> objectID -> object
	ops     map[string]erp.OperationResult            // remoteRef -> result
	byKey   map[string]string                         // idempotency key -> remoteRef
	byOpKey map[string]string                         // remoteRef -> idempotency key

	fault     *Fault
	execCalls int
	seq       int
}

// 断言接口实现。
var _ erp.Port = (*InProcessAdapter)(nil)
var _ erp.Port = (*HTTPAdapter)(nil)

// NewInProcessAdapter 创建内存 Mock ERP。
func NewInProcessAdapter() *InProcessAdapter {
	return &InProcessAdapter{
		objects: map[string]map[string]erp.CanonicalObject{},
		ops:     map[string]erp.OperationResult{},
		byKey:   map[string]string{},
		byOpKey: map[string]string{},
	}
}

// Capabilities 声明内存 Mock ERP 的能力：原生幂等（L1）、支持版本校验与操作查询。
func (a *InProcessAdapter) Capabilities(_ context.Context, connectionID erp.ConnectionID) (erp.Capabilities, error) {
	return erp.Capabilities{
		ConnectionID:                string(connectionID),
		AdapterID:                   "com.odap.adapter.mock-erp",
		AdapterVersion:              "1.0.0",
		Objects:                     []string{"Employee", "Department", "ExpenseClaim"},
		Operations:                  []string{"expense.create_draft", "expense.submit", "expense.get_status"},
		IdempotencyLevel:            erp.IdempotencyLevelL1,
		SupportsVersionCheck:        true,
		SupportsQuery:               "by_idempotency_key",
		SupportsOperationLookup:     true,
		SupportsReceiptVerification: true,
	}, nil
}

// SeedObject 预置一个对象（测试辅助）。
func (a *InProcessAdapter) SeedObject(obj erp.CanonicalObject) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.objects[obj.ObjectType] == nil {
		a.objects[obj.ObjectType] = map[string]erp.CanonicalObject{}
	}
	a.objects[obj.ObjectType][obj.ObjectID] = obj
}

// SetFault 设置故障注入规则（测试辅助）。
func (a *InProcessAdapter) SetFault(f *Fault) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fault = f
}

// Calls 返回 Execute 被调用的次数。
func (a *InProcessAdapter) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.execCalls
}

// GetObject 读取对象。
func (a *InProcessAdapter) GetObject(_ context.Context, req erp.GetObjectRequest) (erp.ObjectResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bucket := a.objects[req.ObjectType]
	if bucket == nil {
		return erp.ObjectResult{}, errors.Ef(errors.CodeNotFound, "mock: 未知对象类型 %q", req.ObjectType)
	}
	obj, ok := bucket[string(req.ObjectID)]
	if !ok {
		return erp.ObjectResult{}, errors.Ef(errors.CodeNotFound, "mock: 对象 %s/%s 不存在", req.ObjectType, req.ObjectID)
	}
	return erp.ObjectResult{Object: obj, Version: obj.SourceVersion, ObservedAt: time.Now().UTC()}, nil
}

// QueryObjects 按 object_type 过滤返回（支持受控 eq 过滤，其余忽略）。
func (a *InProcessAdapter) QueryObjects(_ context.Context, req erp.QueryObjectsRequest) (erp.QueryResult, error) {
	if err := req.Validate(); err != nil {
		return erp.QueryResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	items := make([]erp.CanonicalObject, 0)
	for _, obj := range a.objects[req.ObjectType] {
		if req.Filter.Op == "eq" && req.Filter.Field != "" {
			got, ok := obj.Fields[req.Filter.Field]
			if !ok || fmt.Sprint(got) != fmt.Sprint(req.Filter.Value) {
				continue
			}
		}
		items = append(items, obj)
	}
	return erp.QueryResult{Items: items}, nil
}

// Execute 执行命令：支持幂等去重、版本检查与故障注入。
func (a *InProcessAdapter) Execute(ctx context.Context, req erp.ExecuteRequest) (erp.OperationResult, error) {
	a.mu.Lock()
	a.execCalls++
	fault := a.fault
	if fault != nil && !fault.matches(req.OperationType) {
		fault = nil
	}
	// 幂等去重（duplicate 故障下跳过，模拟远端重复接收）。
	if fault == nil || fault.Kind != FaultDuplicate {
		if ref, ok := a.byKey[req.IdempotencyKey]; ok && req.IdempotencyKey != "" {
			res := a.ops[ref]
			a.mu.Unlock()
			return res, nil
		}
	}
	a.mu.Unlock()

	if fault != nil {
		if fault.Kind == FaultDelay && fault.Delay > 0 {
			select {
			case <-time.After(fault.Delay):
			case <-ctx.Done():
				return erp.OperationResult{}, ctx.Err()
			}
		}
		if fault.Kind == FaultReject {
			return erp.OperationResult{Status: erp.StatusRejected, Error: &erp.ERPError{
				Code:    "ERP_VALIDATION_FAILED",
				Message: "mock: 注入业务拒绝",
			}}, nil
		}
	}

	// 应用副作用（版本检查失败 → 明确失败，不产生副作用）。
	if err := a.applyCommand(req); err != nil {
		return erp.OperationResult{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	ref := fmt.Sprintf("rmt-%06d", a.seq)
	res := erp.OperationResult{
		OperationID:   erp.OperationID(ref),
		Status:        erp.StatusSucceeded,
		RemoteRef:     ref,
		ReceiptID:     "rcpt-" + ref,
		PayloadDigest: req.PayloadDigest,
		ResultVersion: "2",
	}
	if fault != nil {
		switch fault.Kind {
		case FaultTimeout:
			// 已提交但响应丢失：结果对客户端未知。
			res.Status = erp.StatusUnknown
		case FaultUnknown:
			res.Status = erp.StatusUnknown
		}
	}
	a.ops[ref] = res
	a.byOpKey[ref] = req.IdempotencyKey
	if req.IdempotencyKey != "" {
		a.byKey[req.IdempotencyKey] = ref
	}
	if res.Status == erp.StatusUnknown {
		return res, errors.E(errors.CodeUnavailable, "mock: 注入超时（已提交但响应丢失）")
	}
	return res, nil
}

func (a *InProcessAdapter) applyCommand(req erp.ExecuteRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	targetID := string(req.TargetObjectID)
	for _, bucket := range a.objects {
		obj, ok := bucket[targetID]
		if !ok {
			continue
		}
		if req.ExpectedVersion != "" && obj.SourceVersion != "" && obj.SourceVersion != req.ExpectedVersion {
			return errors.Ef(errors.CodeConflict, "mock: 版本冲突（期望 %s，实际 %s）", req.ExpectedVersion, obj.SourceVersion)
		}
		obj.SourceVersion = bumpVersion(obj.SourceVersion)
		bucket[targetID] = obj
		return nil
	}
	// 无目标对象（如组织同步）：不校验版本。
	return nil
}

// GetOperation 查询操作结果：优先按远端操作 ID，其次按幂等键。
func (a *InProcessAdapter) GetOperation(_ context.Context, req erp.GetOperationRequest) (erp.OperationResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id := string(req.OperationID); id != "" {
		if res, ok := a.ops[id]; ok {
			return res, nil
		}
	}
	if req.IdempotencyKey != "" {
		if ref, ok := a.byKey[req.IdempotencyKey]; ok {
			return a.ops[ref], nil
		}
	}
	return erp.OperationResult{Status: erp.StatusUnknown}, nil
}

// Health 恒为健康。
func (a *InProcessAdapter) Health(_ context.Context, connectionID erp.ConnectionID) (erp.HealthResult, error) {
	return erp.HealthResult{
		ConnectionID: string(connectionID),
		Status:       erp.HealthHealthy,
		Healthy:      true,
		CheckedAt:    time.Now().UTC(),
	}, nil
}

func bumpVersion(v string) string {
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return "1"
	}
	return fmt.Sprintf("%d", n+1)
}

// ── HTTPAdapter ────────────────────────────────────────────────────────────

// HTTPAdapter 通过 REST 调用独立运行的 Mock ERP 服务。
type HTTPAdapter struct {
	BaseURL    string
	HTTPClient *http.Client
	// Caps 是 Adapter 自行声明并经审核的能力清单；为空时按 Mock ERP 默认能力填充。
	Caps erp.Capabilities
}

// NewHTTPAdapter 创建 HTTP Mock Adapter。
func NewHTTPAdapter(baseURL string, client *http.Client) *HTTPAdapter {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HTTPAdapter{BaseURL: baseURL, HTTPClient: client}
}

// Capabilities 返回已审核的能力声明（Adapter 负责声明，而非向 Agent 暴露）。
func (a *HTTPAdapter) Capabilities(_ context.Context, connectionID erp.ConnectionID) (erp.Capabilities, error) {
	caps := a.Caps
	if caps.IdempotencyLevel == "" {
		caps = erp.Capabilities{
			AdapterID:                   "com.odap.adapter.mock-erp",
			AdapterVersion:              "1.0.0",
			Objects:                     []string{"Employee", "Department", "ExpenseClaim"},
			Operations:                  []string{"expense.create_draft", "expense.submit", "expense.get_status"},
			IdempotencyLevel:            erp.IdempotencyLevelL1,
			SupportsVersionCheck:        true,
			SupportsQuery:               "by_idempotency_key",
			SupportsOperationLookup:     true,
			SupportsReceiptVerification: true,
		}
	}
	caps.ConnectionID = string(connectionID)
	return caps, nil
}

func (a *HTTPAdapter) do(ctx context.Context, method, path string, body any, headers map[string]string) (map[string]any, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, errors.Wrap(err, errors.CodeValidationFailed, "mock: 序列化请求体失败")
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, reader)
	if err != nil {
		return nil, 0, errors.Wrap(err, errors.CodeInternal, "mock: 构造请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		// 传输层失败：结果未知，交由 Gateway 落 UNKNOWN 并转入对账。
		return nil, 0, errors.Wrap(err, errors.CodeERPAdapterError, "mock: 调用 Mock ERP 失败")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if resp.StatusCode >= 500 {
		return out, resp.StatusCode, errors.Ef(errors.CodeUnavailable, "mock: Mock ERP 返回 %d", resp.StatusCode)
	}
	return out, resp.StatusCode, nil
}

// GetObject 调用 GET /mock/objects/{type}/{id}。
func (a *HTTPAdapter) GetObject(ctx context.Context, req erp.GetObjectRequest) (erp.ObjectResult, error) {
	path := "/mock/objects/" + url.PathEscape(req.ObjectType) + "/" + url.PathEscape(string(req.ObjectID))
	out, status, err := a.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return erp.ObjectResult{}, err
	}
	if status == http.StatusNotFound {
		return erp.ObjectResult{}, errors.Ef(errors.CodeNotFound, "mock: 对象 %s 不存在", req.ObjectID)
	}
	obj, err := decodeObject(out)
	if err != nil {
		return erp.ObjectResult{}, err
	}
	return erp.ObjectResult{Object: obj, Version: obj.SourceVersion, ObservedAt: time.Now().UTC()}, nil
}

// QueryObjects 调用 POST /mock/objects/query。
func (a *HTTPAdapter) QueryObjects(ctx context.Context, req erp.QueryObjectsRequest) (erp.QueryResult, error) {
	if err := req.Validate(); err != nil {
		return erp.QueryResult{}, err
	}
	out, _, err := a.do(ctx, http.MethodPost, "/mock/objects/query", map[string]any{
		"object_type": req.ObjectType,
		"filter":      req.Filter,
		"page_size":   req.PageSize,
		"cursor":      req.Cursor,
	}, nil)
	if err != nil {
		return erp.QueryResult{}, err
	}
	result := erp.QueryResult{}
	if items, ok := out["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				if obj, err := decodeObject(m); err == nil {
					result.Items = append(result.Items, obj)
				}
			}
		}
	}
	if more, ok := out["has_more"].(bool); ok {
		result.HasMore = more
	}
	if cursor, ok := out["next_cursor"].(string); ok {
		result.NextCursor = cursor
	}
	return result, nil
}

// Execute 调用 POST /mock/operations，携带幂等键与预期版本。
func (a *HTTPAdapter) Execute(ctx context.Context, req erp.ExecuteRequest) (erp.OperationResult, error) {
	headers := map[string]string{
		"Idempotency-Key": req.IdempotencyKey,
		// §8.1 规则 5：请求头附带的本地引用 ID 用于审计关联，不参与远端去重。
		"X-ODAP-Operation-Ref": string(req.OperationID),
	}
	if req.ExpectedVersion != "" {
		headers["If-Match"] = fmt.Sprintf("%q", req.ExpectedVersion)
	}
	body := map[string]any{
		"operation":        req.OperationType,
		"object_id":        string(req.TargetObjectID),
		"payload":          req.Payload,
		"trace_id":         req.TraceID,
		"expected_version": req.ExpectedVersion,
	}
	out, status, err := a.do(ctx, http.MethodPost, "/mock/operations", body, headers)
	if err != nil {
		// 传输失败 / 超时 / 5xx：远端可能已提交，一律按结果未知返回，
		// 由 Gateway 落 UNKNOWN 并转入对账（第七篇 §8.3 / §17.4）。
		return erp.OperationResult{
			OperationID:   erp.OperationID(str(out, "operation_id")),
			Status:        erp.StatusUnknown,
			ReceiptID:     str(out, "receipt_id"),
			PayloadDigest: req.PayloadDigest,
			Retryable:     false,
			Error:         &erp.ERPError{Code: errors.CodeUnavailable, Message: "mock: 远端未确认结果"},
		}, nil
	}
	res := erp.OperationResult{
		OperationID:   erp.OperationID(str(out, "operation_id")),
		Status:        erp.OperationStatus(str(out, "status")),
		RemoteRef:     str(out, "operation_id"),
		ReceiptID:     str(out, "receipt_id"),
		PayloadDigest: req.PayloadDigest,
		ResultVersion: str(out, "result_version"),
	}
	switch {
	case status == http.StatusConflict:
		// 幂等键冲突或版本冲突：明确拒绝，不产生副作用。
		res.Status = erp.StatusRejected
		res.Error = &erp.ERPError{Code: errors.CodeIdempotencyConflict, Message: str(out, "message")}
	case status == http.StatusUnprocessableEntity:
		res.Status = erp.StatusRejected
		res.Error = &erp.ERPError{Code: "ERP_VALIDATION_FAILED", Message: str(out, "message")}
	case status >= 400:
		// 其余 4xx：明确未执行的请求错误，可判定为 FAILED。
		res.Status = erp.StatusFailed
		res.Error = &erp.ERPError{Code: "ERP_VALIDATION_FAILED", Message: str(out, "message")}
	}
	if res.Status == "" {
		res.Status = erp.StatusUnknown
	}
	return res, nil
}

// GetOperation 调用 GET /mock/operations/{id}；远端 ID 未知时退化为按幂等键查询。
func (a *HTTPAdapter) GetOperation(ctx context.Context, req erp.GetOperationRequest) (erp.OperationResult, error) {
	var path string
	if req.OperationID != "" {
		path = "/mock/operations/" + url.PathEscape(string(req.OperationID))
	} else if req.IdempotencyKey != "" {
		path = "/mock/operations/by-idempotency-key/" + url.PathEscape(req.IdempotencyKey)
	} else {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}
	out, status, err := a.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}
	if status == http.StatusNotFound {
		// 远端明确无此操作 → 可判定为"确认未执行"。
		return erp.OperationResult{Status: erp.StatusFailed, Retryable: true}, nil
	}
	res := erp.OperationResult{
		OperationID:   erp.OperationID(str(out, "operation_id")),
		Status:        erp.OperationStatus(str(out, "status")),
		RemoteRef:     str(out, "operation_id"),
		ReceiptID:     str(out, "receipt_id"),
		ResultVersion: str(out, "result_version"),
	}
	if res.Status == "" {
		res.Status = erp.StatusUnknown
	}
	return res, nil
}

// Health 调用 GET /mock/health。
func (a *HTTPAdapter) Health(ctx context.Context, connectionID erp.ConnectionID) (erp.HealthResult, error) {
	out, status, err := a.do(ctx, http.MethodGet, "/mock/health", nil, nil)
	if err != nil || status != http.StatusOK {
		return erp.HealthResult{
			ConnectionID: string(connectionID),
			Status:       erp.HealthUnavailable,
			Healthy:      false,
			CheckedAt:    time.Now().UTC(),
		}, nil
	}
	return erp.HealthResult{
		ConnectionID: string(connectionID),
		Status:       str(out, "status"),
		Healthy:      true,
		CheckedAt:    time.Now().UTC(),
	}, nil
}

func decodeObject(m map[string]any) (erp.CanonicalObject, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return erp.CanonicalObject{}, errors.Wrap(err, errors.CodeERPAdapterError, "mock: 解析对象失败")
	}
	var obj erp.CanonicalObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		return erp.CanonicalObject{}, errors.Wrap(err, errors.CodeERPAdapterError, "mock: 解析对象失败")
	}
	return obj, nil
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
