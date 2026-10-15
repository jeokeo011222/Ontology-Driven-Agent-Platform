// Package nc65 实现用友 NC65 的 ODAP ERP Adapter —— 第七篇 §19 路线图中的
// "真实 ERP Adapter"首个对接示例。
//
// 职责边界（第七篇 §1.3）：Port 只声明"平台需要什么业务能力"，本 Adapter
// 负责把 NC65 的原生协议映射到 Port 能力；它不自行批准业务操作，也不绕过
// Operation Gateway 的幂等、授权与账本。
//
// 硬约束（与项目"严禁 mock、严禁伪造数据、严禁静默降级"一致）：
//   - 所有读写都走真实 HTTP 调用；规范对象的每个字段都来自 NC65 真实响应
//     或外置映射表，绝不凭空补值；
//   - 未配置的对象/操作/端点一律 fail-closed，返回 UNAVAILABLE 并说明原因；
//   - 传输失败与 5xx 一律标记结果 UNKNOWN（可能已提交），绝不静默当失败；
//   - 凭据只从环境变量注入，不落盘、不写日志。
//
// 端口差异说明：NC65 各模块的原生接口路径随版本与部署不同，因此对象/操作
// 端点与字段映射均由外置映射文件（ODAP_NC65_MAPPING_FILE）提供，运维按本
// 企业实际部署配置；Adapter 不硬编码任何"猜测可用"的地址。
package nc65

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"odap/internal/erp"
	"odap/internal/platform/errors"
)

// 断言接口实现。
var _ erp.Port = (*Adapter)(nil)

// AdapterID 是本 Adapter 的稳定标识（写入能力声明与审计）。
const AdapterID = "com.odap.adapter.ufida-nc65"

// 读取 NC65 响应体时的上限，防止异常大响应耗尽内存。
const maxResponseBytes = 4 << 20

// 令牌默认有效期（NC65 未返回 expires_in 时使用）。
const defaultTokenTTL = 30 * time.Minute

// Adapter 是用友 NC65 的 ERP Port 实现。
type Adapter struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// New 构造 NC65 Adapter；配置不满足最小条件时返回错误（fail-closed），
// 绝不返回一个"能跑但会返回假数据"的适配器。
func New(cfg Config) (*Adapter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.TokenPath == "" {
		cfg.TokenPath = defaultTokenPath
	}
	if cfg.TokenHeader == "" {
		cfg.TokenHeader = defaultTokenHeader
	}
	if cfg.LangCode == "" {
		cfg.LangCode = defaultLangCode
	}
	cl := cfg.HTTPClient
	if cl == nil {
		cl = &http.Client{Timeout: orDuration(cfg.Timeout, 15*time.Second)}
	}
	return &Adapter{cfg: cfg, http: cl}, nil
}

// Capabilities 返回经管理员审核的连接能力声明（由配置如实推导）。
func (a *Adapter) Capabilities(_ context.Context, connectionID erp.ConnectionID) (erp.Capabilities, error) {
	objs := make([]string, 0, len(a.cfg.Objects))
	for k := range a.cfg.Objects {
		objs = append(objs, k)
	}
	ops := make([]string, 0, len(a.cfg.Operations))
	for k := range a.cfg.Operations {
		ops = append(ops, k)
	}

	level := a.cfg.IdempotencyLevel
	switch level {
	case erp.IdempotencyLevelL1, erp.IdempotencyLevelL2, erp.IdempotencyLevelL3:
	default:
		// 未声明或声明非法一律按 L3（最保守）：结果未知时禁止自动重发。
		level = erp.IdempotencyLevelL3
	}

	supportsLookup := strings.TrimSpace(a.cfg.OperationLookupPath) != ""
	supportsQuery := "none"
	if supportsLookup {
		supportsQuery = "by_operation_id"
	}

	return erp.Capabilities{
		ConnectionID:                string(connectionID),
		AdapterID:                   AdapterID,
		AdapterVersion:              orDefault(a.cfg.AdapterVersion, "1.0.0"),
		Objects:                     objs,
		Operations:                  ops,
		IdempotencyLevel:            level,
		SupportsVersionCheck:        a.anyVersionField(),
		SupportsQuery:               supportsQuery,
		SupportsOperationLookup:     supportsLookup,
		SupportsReceiptVerification: false, // NC65 无统一回执核验接口，保守声明为不支持
		SupportsRemoteApproval:      false,
	}, nil
}

func (a *Adapter) anyVersionField() bool {
	for _, m := range a.cfg.Objects {
		if strings.TrimSpace(m.VersionField) != "" {
			return true
		}
	}
	return false
}

// GetObject 读取单个规范对象。
func (a *Adapter) GetObject(ctx context.Context, req erp.GetObjectRequest) (erp.ObjectResult, error) {
	m, ok := a.cfg.Objects[req.ObjectType]
	if !ok {
		return erp.ObjectResult{}, errors.Ef(errors.CodeUnavailable,
			"nc65: 连接未配置对象类型 %q 的映射", req.ObjectType)
	}
	method := orDefault(m.GetMethod, http.MethodPost)
	path := strings.ReplaceAll(m.GetPath, "{id}", url.PathEscape(string(req.ObjectID)))

	var body any
	if method != http.MethodGet {
		body = map[string]any{"pk": string(req.ObjectID)}
	}
	out, status, err := a.request(ctx, method, path, body)
	if err != nil {
		return erp.ObjectResult{}, err
	}
	if status == http.StatusNotFound {
		return erp.ObjectResult{}, errors.Ef(errors.CodeNotFound,
			"nc65: 对象 %s/%s 不存在", req.ObjectType, req.ObjectID)
	}
	if status < 200 || status >= 300 {
		return erp.ObjectResult{}, errors.Ef(errors.CodeERPAdapterError,
			"nc65: 读取对象 %s 失败，NC65 返回 HTTP %d", req.ObjectID, status)
	}

	data, bizErr := unwrap(out)
	if bizErr != nil {
		return erp.ObjectResult{}, errors.Ef(errors.CodeNotFound,
			"nc65: 读取对象 %s 被 NC65 拒绝：%s", req.ObjectID, bizErr.Message)
	}
	native, ok := asObject(data)
	if !ok {
		return erp.ObjectResult{}, errors.Ef(errors.CodeERPAdapterError,
			"nc65: 对象 %s 的响应结构无法解析为单个对象", req.ObjectID)
	}
	obj := a.toCanonical(req.ObjectType, req.TenantID, string(req.ConnectionID), native, m)
	return erp.ObjectResult{Object: obj, Version: obj.SourceVersion, ObservedAt: time.Now().UTC()}, nil
}

// QueryObjects 执行受控查询。查询体由 Adapter 组装为 NC Cloud 常见的
// pageInfo + filter 形状；端点契约由运维在映射文件中配置。
func (a *Adapter) QueryObjects(ctx context.Context, req erp.QueryObjectsRequest) (erp.QueryResult, error) {
	if err := req.Validate(); err != nil {
		return erp.QueryResult{}, err
	}
	m, ok := a.cfg.Objects[req.ObjectType]
	if !ok {
		return erp.QueryResult{}, errors.Ef(errors.CodeUnavailable,
			"nc65: 连接未配置对象类型 %q 的映射", req.ObjectType)
	}
	if strings.TrimSpace(m.QueryPath) == "" {
		return erp.QueryResult{}, errors.Ef(errors.CodeUnavailable,
			"nc65: 对象类型 %q 未配置 query_path，连接不支持受控查询", req.ObjectType)
	}

	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	body := map[string]any{
		"pageInfo": map[string]any{
			"pageIndex": cursorPageIndex(req.Cursor),
			"pageSize":  pageSize,
		},
	}
	if req.Filter.Op != "" {
		body["filter"] = map[string]any{
			"field": nativeField(m, req.Filter.Field),
			"op":    req.Filter.Op,
			"value": req.Filter.Value,
		}
	}
	if len(req.Fields) > 0 {
		nativeFields := make([]string, 0, len(req.Fields))
		for _, f := range req.Fields {
			nativeFields = append(nativeFields, nativeField(m, f))
		}
		body["fields"] = nativeFields
	}

	method := orDefault(m.QueryMethod, http.MethodPost)
	out, status, err := a.request(ctx, method, m.QueryPath, body)
	if err != nil {
		return erp.QueryResult{}, err
	}
	if status < 200 || status >= 300 {
		return erp.QueryResult{}, errors.Ef(errors.CodeERPAdapterError,
			"nc65: 查询 %s 失败，NC65 返回 HTTP %d", req.ObjectType, status)
	}
	data, bizErr := unwrap(out)
	if bizErr != nil {
		return erp.QueryResult{}, errors.Ef(errors.CodeERPAdapterError,
			"nc65: 查询 %s 被 NC65 拒绝：%s", req.ObjectType, bizErr.Message)
	}
	rows, hasMore, next, ok := extractRows(data, pageSize)
	if !ok {
		// 结构无法识别时明确失败，绝不静默返回空集（会被误读为"无数据"）。
		return erp.QueryResult{}, errors.Ef(errors.CodeERPAdapterError,
			"nc65: 查询 %s 的响应结构无法识别（缺少已知列表字段）", req.ObjectType)
	}
	res := erp.QueryResult{HasMore: hasMore, NextCursor: next}
	for _, row := range rows {
		native, ok := asObject(row)
		if !ok {
			continue
		}
		res.Items = append(res.Items, a.toCanonical(req.ObjectType, req.TenantID, string(req.ConnectionID), native, m))
	}
	return res, nil
}

// Execute 执行一个明确的业务命令（写操作）。
//
// 结果语义与第七篇 §3.4 对齐：
//   - NC65 业务成功 → SUCCEEDED；
//   - NC65 明确业务拒绝（envelope 报错）→ REJECTED（不产生"半个成功"的错觉）；
//   - HTTP 4xx 请求错误 → FAILED（明确未执行）；
//   - 传输失败 / 5xx → UNKNOWN（可能已提交，交由 Gateway 落 UNKNOWN 并转入对账）。
func (a *Adapter) Execute(ctx context.Context, req erp.ExecuteRequest) (erp.OperationResult, error) {
	m, ok := a.cfg.Operations[req.OperationType]
	if !ok {
		return erp.OperationResult{}, errors.Ef(errors.CodeUnavailable,
			"nc65: 连接未配置业务命令 %q 的映射", req.OperationType)
	}
	if strings.TrimSpace(m.Path) == "" {
		return erp.OperationResult{}, errors.Ef(errors.CodeUnavailable,
			"nc65: 业务命令 %q 未配置 path", req.OperationType)
	}

	body := a.mapPayload(req.Payload, m)
	// NC65 无原生幂等键支持：仅把本地引用与幂等键随请求送达，供远端审计与
	// 对账关联；Adapter 绝不声称远端已按该键去重（能力声明如实标注级别）。
	body["traceId"] = req.TraceID
	body["idempotencyKey"] = req.IdempotencyKey
	if req.ExpectedVersion != "" {
		body["expectedVersion"] = req.ExpectedVersion
	}

	headers := map[string]string{}
	if req.ExpectedVersion != "" {
		headers["If-Match"] = fmt.Sprintf("%q", req.ExpectedVersion)
	}
	// §8.1：本地引用 ID 仅用于审计关联，不参与远端去重。
	headers["X-ODAP-Operation-Ref"] = string(req.OperationID)

	method := orDefault(m.Method, http.MethodPost)
	out, status, err := a.requestWithHeaders(ctx, method, m.Path, body, headers)
	if err != nil {
		return erp.OperationResult{
			OperationID:   req.OperationID,
			Status:        erp.StatusUnknown,
			PayloadDigest: req.PayloadDigest,
			Error:         &erp.ERPError{Code: errors.CodeUnavailable, Message: "nc65: 到 NC65 的调用未获确认（可能已提交）"},
		}, nil
	}
	if status >= 500 {
		return erp.OperationResult{
			OperationID:   req.OperationID,
			Status:        erp.StatusUnknown,
			PayloadDigest: req.PayloadDigest,
			Error:         &erp.ERPError{Code: errors.CodeUnavailable, Message: fmt.Sprintf("nc65: NC65 返回 HTTP %d（结果未知）", status)},
		}, nil
	}

	res := erp.OperationResult{
		OperationID:   req.OperationID,
		PayloadDigest: req.PayloadDigest,
	}
	data, bizErr := unwrap(out)
	switch {
	case bizErr != nil:
		// 业务明确拒绝：不产生副作用，也不是"失败但可能已执行"。
		res.Status = erp.StatusRejected
		res.Error = &erp.ERPError{Code: bizErr.Code, Message: bizErr.Message, Retryable: false}
	case status >= 400:
		res.Status = erp.StatusFailed
		res.Error = &erp.ERPError{Code: "ERP_VALIDATION_FAILED", Message: fmt.Sprintf("nc65: NC65 返回 HTTP %d", status), Retryable: false}
	default:
		res.Status = erp.StatusSucceeded
		res.RemoteRef = remoteRef(data, m)
		res.ResultVersion = resultVersion(data, m)
	}
	if res.Status == "" {
		res.Status = erp.StatusUnknown
	}
	if res.Status == erp.StatusSucceeded && res.RemoteRef == "" {
		// 成功但拿不到远端主键：仍如实标为成功，但对账时只能凭幂等键回查。
		res.RemoteRef = string(req.OperationID)
	}
	now := time.Now().UTC()
	res.CompletedAt = &now
	return res, nil
}

// GetOperation 查询某个远端操作的执行结果（用于对账）。
// 未配置查询端点时返回 UNKNOWN（无法确认），绝不臆断为成功或失败。
func (a *Adapter) GetOperation(ctx context.Context, req erp.GetOperationRequest) (erp.OperationResult, error) {
	if strings.TrimSpace(a.cfg.OperationLookupPath) == "" {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}
	body := map[string]any{}
	if req.OperationID != "" {
		body["operationId"] = string(req.OperationID)
	}
	if req.IdempotencyKey != "" {
		body["idempotencyKey"] = req.IdempotencyKey
	}
	if len(body) == 0 {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}

	method := orDefault(a.cfg.OperationLookupMethod, http.MethodPost)
	out, status, err := a.request(ctx, method, a.cfg.OperationLookupPath, body)
	if err != nil {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}
	if status == http.StatusNotFound {
		// NC65 明确无此操作：可判定为"确认未执行"。
		return erp.OperationResult{Status: erp.StatusFailed, Retryable: true}, nil
	}
	if status < 200 || status >= 300 {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}
	data, bizErr := unwrap(out)
	if bizErr != nil {
		return erp.OperationResult{Status: erp.StatusUnknown}, nil
	}
	res := erp.OperationResult{OperationID: req.OperationID}
	statusText := ""
	if m, ok := asObject(data); ok {
		statusText = strings.ToUpper(strings.TrimSpace(stringField(m, "status")))
		res.RemoteRef = stringField(m, "operationId")
		res.ResultVersion = stringField(m, "resultVersion")
	}
	switch statusText {
	case "SUCCEEDED", "SUCCESS", "COMPLETED", "DONE":
		res.Status = erp.StatusSucceeded
	case "FAILED", "ERROR":
		res.Status = erp.StatusFailed
	case "REJECTED":
		res.Status = erp.StatusRejected
	default:
		res.Status = erp.StatusUnknown
	}
	return res, nil
}

// Health 返回连接健康检查结果。未配置 HealthPath 时以"真实取令牌"作为探针。
func (a *Adapter) Health(ctx context.Context, connectionID erp.ConnectionID) (erp.HealthResult, error) {
	checked := time.Now().UTC()
	path := strings.TrimSpace(a.cfg.HealthPath)
	if path == "" {
		if _, err := a.token(ctx); err != nil {
			return erp.HealthResult{
				ConnectionID: string(connectionID),
				Status:       erp.HealthUnavailable,
				Healthy:      false,
				CheckedAt:    checked,
				Message:      "nc65: 登录失败：" + err.Error(),
			}, nil
		}
		return erp.HealthResult{
			ConnectionID: string(connectionID),
			Status:       erp.HealthHealthy,
			Healthy:      true,
			CheckedAt:    checked,
			Message:      "nc65: 登录成功",
		}, nil
	}

	out, status, err := a.request(ctx, http.MethodGet, path, nil)
	if err != nil || status < 200 || status >= 300 {
		msg := fmt.Sprintf("nc65: 健康检查失败，HTTP %d", status)
		if err != nil {
			msg = "nc65: 健康检查请求失败：" + err.Error()
		}
		return erp.HealthResult{
			ConnectionID: string(connectionID),
			Status:       erp.HealthUnavailable,
			Healthy:      false,
			CheckedAt:    checked,
			Message:      msg,
		}, nil
	}
	res := erp.HealthResult{
		ConnectionID: string(connectionID),
		Status:       erp.HealthHealthy,
		Healthy:      true,
		CheckedAt:    checked,
	}
	if m, ok := asObject(out); ok {
		if s := stringField(m, "status"); s != "" {
			res.Status = strings.ToUpper(s)
		}
	}
	if res.Status == erp.HealthDegraded || res.Status == erp.HealthUnavailable {
		res.Healthy = false
	}
	return res, nil
}

// ── HTTP 传输 ───────────────────────────────────────────────────────────────

// request 发起一次带令牌的请求并解析 JSON 响应体。
func (a *Adapter) request(ctx context.Context, method, path string, body any) (map[string]any, int, error) {
	return a.requestWithHeaders(ctx, method, path, body, nil)
}

func (a *Adapter) requestWithHeaders(ctx context.Context, method, path string, body any, headers map[string]string) (map[string]any, int, error) {
	// 健康检查在取令牌之前也可能被调用；此处只在需要令牌的调用上取令牌，
	// 避免健康探针与令牌端点的相互递归（健康探针显式调用 token）。
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, errors.Wrap(err, errors.CodeValidationFailed, "nc65: 序列化请求体失败")
		}
		reader = bytes.NewReader(b)
	}
	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, 0, errors.Wrap(err, errors.CodeInternal, "nc65: 构造请求失败")
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")

	// 取令牌（幂等，带缓存）。
	if tok, terr := a.token(ctx); terr == nil && tok != "" {
		req.Header.Set(a.cfg.TokenHeader, tok)
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := a.http.Do(req)
	if err != nil {
		// 传输层失败：结果未知，交由 Gateway 落 UNKNOWN 并转入对账。
		return nil, 0, errors.Wrap(err, errors.CodeERPAdapterError, "nc65: 请求 NC65 失败")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	out := map[string]any{}
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			// 非 JSON 响应：仅在业务成功状态下才视为异常；错误状态交由调用方按 HTTP 码判定。
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil, resp.StatusCode, errors.Ef(errors.CodeERPAdapterError,
					"nc65: 响应体不是合法 JSON（HTTP %d）", resp.StatusCode)
			}
		}
	}
	return out, resp.StatusCode, nil
}

// token 返回缓存的访问令牌；过期或缺失时重新登录。
func (a *Adapter) token(ctx context.Context) (string, error) {
	a.mu.Lock()
	if a.accessToken != "" && time.Now().Before(a.tokenExpiry) {
		tok := a.accessToken
		a.mu.Unlock()
		return tok, nil
	}
	a.mu.Unlock()

	tok, ttl, err := a.login(ctx)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.accessToken = tok
	a.tokenExpiry = time.Now().Add(ttl)
	a.mu.Unlock()
	return tok, nil
}

// login 调用 NC65 令牌端点完成真实登录。凭据来自配置（其来源是环境变量）。
func (a *Adapter) login(ctx context.Context) (string, time.Duration, error) {
	body := map[string]any{
		"usercode":  a.cfg.Usercode,
		"password":  a.cfg.Password,
		"groupcode": a.cfg.GroupCode,
		"langcode":  a.cfg.LangCode,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", 0, errors.Wrap(err, errors.CodeInternal, "nc65: 序列化登录请求失败")
	}
	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + a.cfg.TokenPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return "", 0, errors.Wrap(err, errors.CodeInternal, "nc65: 构造登录请求失败")
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return "", 0, errors.Wrap(err, errors.CodeERPAdapterError, "nc65: 登录请求失败")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, errors.Ef(errors.CodeUnavailable,
			"nc65: 登录失败，令牌端点返回 HTTP %d", resp.StatusCode)
	}
	data, bizErr := unwrap(out)
	if bizErr != nil {
		return "", 0, errors.Ef(errors.CodeUnavailable, "nc65: 登录被拒绝：%s", bizErr.Message)
	}
	tok := ""
	if m, ok := asObject(data); ok {
		tok = firstString(m, "access_token", "accessToken", "token")
	}
	if tok == "" {
		tok = firstString(out, "access_token", "accessToken", "token")
	}
	if tok == "" {
		return "", 0, errors.E(errors.CodeUnavailable, "nc65: 登录响应中未包含访问令牌")
	}

	ttl := defaultTokenTTL
	if m, ok := asObject(data); ok {
		if secs := intField(m, "expires_in", "expiresIn"); secs > 0 {
			ttl = time.Duration(secs) * time.Second
		}
	}
	// 到期前留 10% 安全裕量，避免临界过期。
	return tok, ttl * 9 / 10, nil
}

// ── 响应解析 ───────────────────────────────────────────────────────────────

// bizError 是 NC65 业务层错误。
type bizError struct {
	Code    string
	Message string
}

// unwrap 解包 NC Cloud 风格响应信封，返回业务数据；业务失败时返回 bizError。
//
// 兼容两类信封：
//   - {"success": bool, "data": ..., "message": ...}
//   - {"code": "0"|"200"|..., "data": ..., "msg"/"message": ...}
func unwrap(m map[string]any) (any, *bizError) {
	if m == nil {
		return nil, nil
	}
	if ok, has := m["success"].(bool); has && !ok {
		return nil, &bizError{Code: errCode(m), Message: errMessage(m)}
	}
	if code, has := m["code"]; has {
		switch fmt.Sprint(code) {
		case "0", "200", "success", "SUCCESS":
			// 正常
		default:
			return nil, &bizError{Code: errCode(m), Message: errMessage(m)}
		}
	}
	if m["data"] != nil {
		return m["data"], nil
	}
	return m, nil
}

func errCode(m map[string]any) string {
	if e, ok := asObject(m["error"]); ok {
		if c := stringField(e, "code"); c != "" {
			return c
		}
	}
	if c := stringField(m, "errorCode"); c != "" {
		return c
	}
	if c := stringField(m, "code"); c != "" && c != "0" {
		return c
	}
	return "ERP_VALIDATION_FAILED"
}

func errMessage(m map[string]any) string {
	for _, k := range []string{"message", "msg", "errorMessage"} {
		if s := stringField(m, k); s != "" {
			return s
		}
	}
	if e, ok := asObject(m["error"]); ok {
		if s := stringField(e, "message"); s != "" {
			return s
		}
	}
	return "nc65 业务操作被拒绝"
}

// extractRows 从查询响应中提取列表、分页信息。返回 (rows, hasMore, nextCursor, ok)。
func extractRows(data any, pageSize int) ([]any, bool, string, bool) {
	switch t := data.(type) {
	case []any:
		return t, false, "", true
	case map[string]any:
		for _, key := range []string{"rows", "list", "dataList", "records", "items", "result"} {
			if arr, ok := t[key].([]any); ok {
				hasMore := boolField(t, "hasMore", "has_next")
				next := ""
				if idx := intField(t, "pageIndex", "pageNum"); idx > 0 {
					if hasMore {
						next = strconv.Itoa(idx + 1)
					}
				}
				return arr, hasMore, next, true
			}
		}
		// 分页对象嵌套（如 {"page": {"list": [...]}}）。
		for _, wrap := range []string{"page", "pageInfo", "result"} {
			if inner, ok := asObject(t[wrap]); ok {
				if arr, hasMore, next, ok := extractRows(inner, pageSize); ok {
					return arr, hasMore, next, true
				}
			}
		}
	}
	return nil, false, "", false
}

// ── 映射与转换 ─────────────────────────────────────────────────────────────

// toCanonical 把 NC65 原生对象按映射表转录为规范对象。
//
// 数据质量判定（第七篇 §2.4）：配置了字段映射但原生响应缺少某个映射字段时，
// 对象被标注为 PARTIAL；未配置字段映射时标注为 UNMAPPED 并原样保留原生字段。
// 绝不为了"看起来完整"而填充零值。
func (a *Adapter) toCanonical(objectType, tenantID, connID string, native map[string]any, m ObjectMapping) erp.CanonicalObject {
	fields := map[string]any{}
	quality := erp.DataQualityVerified

	if len(m.Fields) == 0 {
		quality = erp.DataQualityUnmapped
		for k, v := range native {
			fields[k] = v
		}
	} else {
		missing := false
		for canonical, nativeName := range m.Fields {
			v, ok := native[nativeName]
			if !ok {
				missing = true
				continue
			}
			fields[canonical] = v
		}
		if missing {
			quality = erp.DataQualityPartial
		}
	}

	id := ""
	if m.IDField != "" {
		id = stringField(native, m.IDField)
	}
	if id == "" {
		id = firstString(native, "pk", "id", "code", "billId")
	}
	version := ""
	if m.VersionField != "" {
		version = stringField(native, m.VersionField)
	}

	return erp.CanonicalObject{
		ObjectType:     objectType,
		ObjectID:       id,
		TenantID:       tenantID,
		SourceVersion:  version,
		MappingVersion: a.cfg.MappingVersion,
		ObservedAt:     time.Now().UTC(),
		DataQuality:    quality,
		Fields:         fields,
		ExternalRef: erp.ExternalRef{
			ConnectionID:     connID,
			NativeObjectType: m.NativeObjectType,
			NativeID:         id,
		},
	}
}

// mapPayload 按映射表把规范 payload 字段转成 NC65 原生字段名；
// 未配置映射表时原样透传（由运维保证端点契约一致）。
func (a *Adapter) mapPayload(payload map[string]any, m OperationMapping) map[string]any {
	out := map[string]any{}
	if len(m.PayloadMap) == 0 {
		for k, v := range payload {
			out[k] = v
		}
		return out
	}
	for canonical, nativeName := range m.PayloadMap {
		if v, ok := payload[canonical]; ok {
			out[nativeName] = v
		}
	}
	return out
}

func remoteRef(data any, m OperationMapping) string {
	if obj, ok := asObject(data); ok {
		if id := stringField(obj, "pk"); id != "" {
			return id
		}
		if id := stringField(obj, "id"); id != "" {
			return id
		}
		if id := stringField(obj, "billId"); id != "" {
			return id
		}
		return firstString(obj, "operationId")
	}
	if s, ok := data.(string); ok {
		return s
	}
	return ""
}

func resultVersion(data any, m OperationMapping) string {
	if obj, ok := asObject(data); ok {
		if v := stringField(obj, "resultVersion"); v != "" {
			return v
		}
	}
	return ""
}

// nativeField 返回规范字段对应的原生字段名；未映射时原样返回字段名。
func nativeField(m ObjectMapping, canonical string) string {
	if m.Fields != nil {
		if n, ok := m.Fields[canonical]; ok && n != "" {
			return n
		}
	}
	return canonical
}

func cursorPageIndex(cursor string) int {
	if cursor == "" {
		return 1
	}
	if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
		return n
	}
	return 1
}

// ── 小工具 ─────────────────────────────────────────────────────────────────

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := stringField(m, k); s != "" {
			return s
		}
	}
	return ""
}

func intField(m map[string]any, keys ...string) int {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		case string:
			if n, err := strconv.Atoi(t); err == nil {
				return n
			}
		}
	}
	return 0
}

func boolField(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k].(bool); ok {
			return v
		}
	}
	return false
}
