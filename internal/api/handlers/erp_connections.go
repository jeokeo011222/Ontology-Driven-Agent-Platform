package handlers

// ERP 连接配置（与ERP集成，需求 2）：ERP 系统类型字典 + 租户级连接 CRUD + 测试连接。
//
// 设计要点：
//   - 字典 integration.erp_systems 是全局只读的；`implemented=false` 的类型
//     （金蝶星空云 / SAP）在创建连接时直接 422（fail-closed），绝不产出一个
//     永远无法装配的空壳连接。
//   - 凭据与模型端点同一套硬约束：界面录入的凭据（usercode/password 等）
//     由 store 层以 AES-256-GCM 信封加密落库（AAD 绑定连接 id），明文绝不
//     进 SQL 参数、审计、日志、SSE 或 HTTP 响应，读取只回打码值。
//   - 测试连接用用户录入的参数直连真实探针（nc65 走 Health），不落库、
//     不产生任何业务写入——与模型供应商弹窗的「连接测试」同一交互契约。
//   - 写操作一律要求 Idempotency-Key 并回写响应头；审计 + SSE 同步落。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/erp"
	"odap/internal/erp/adapters/nc65"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// credentialInput 是创建/测试时的凭据录入（按 ERP 类型解释字段）。
//
// nc65：usercode + password（NC65 登录凭据）。store 层把它序列化为 JSON
// 再加密——密文里没有可读字段，读出后按 system_code 反序列化。
type credentialInput struct {
	Usercode string `json:"usercode,omitempty"`
	Password string `json:"password,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Token    string `json:"token,omitempty"`
}

// nonEmpty 报告凭据里是否录入了任何字段。
func (c credentialInput) nonEmpty() bool {
	return c.Usercode != "" || c.Password != "" || c.APIKey != "" || c.Token != ""
}

// marshal 把凭据序列化为待加密字节（无字段时返回 nil）。
func (c credentialInput) marshal() []byte {
	if !c.nonEmpty() {
		return nil
	}
	b, _ := json.Marshal(c)
	return b
}

// parseCredentialJSON 把解密后的凭据 JSON 反序列化回结构化字段。
func parseCredentialJSON(raw string) credentialInput {
	var c credentialInput
	_ = json.Unmarshal([]byte(raw), &c)
	return c
}

type createErpConnectionRequest struct {
	SystemCode string `json:"system_code"`
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	// CredentialRef 与 Credential 二选一（与模型端点同规则）。
	CredentialRef string           `json:"credential_ref,omitempty"`
	Credential    *credentialInput `json:"credential,omitempty"`
	Status        string           `json:"status,omitempty"`
}

// ListErpSystems 处理 GET /api/v1/admin/erp-systems（全局字典）。
func (h *Handler) ListErpSystems(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	systems, err := h.deps.Store.ListErpSystems(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"items": systems})
}

// ListErpConnections 处理 GET /api/v1/admin/erp-connections。
//
// 支持 ?status=&system_code=&q=&cursor=&limit=（cursor 分页，默认 10，最大 200）。
func (h *Handler) ListErpConnections(w http.ResponseWriter, r *http.Request) {
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
	page, err := h.deps.Store.ListErpConnections(r.Context(), limit,
		r.URL.Query().Get("cursor"), r.URL.Query().Get("status"),
		r.URL.Query().Get("system_code"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]store.ErpConnection, 0, len(page.Items))
	for _, row := range page.Items {
		items = append(items, scanErpConnectionFromMap(row))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"next_cursor": page.NextCursor,
	})
}

// mapStr / mapBool 把 queryMaps 归一化值转成读模型标量。
func mapStr(v any) string {
	s, _ := v.(string)
	return s
}

func mapBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// scanErpConnectionFromMap 把 ListResult 行投影为读模型。
func scanErpConnectionFromMap(row map[string]any) store.ErpConnection {
	return store.ErpConnection{
		ID:               mapStr(row["id"]),
		SystemCode:       mapStr(row["system_code"]),
		SystemName:       mapStr(row["system_name"]),
		Name:             mapStr(row["name"]),
		BaseURL:          mapStr(row["base_url"]),
		CredentialRef:    mapStr(row["credential_ref"]),
		CredentialSource: mapStr(row["credential_source"]),
		CredentialMasked: mapStr(row["credential_masked"]),
		HasCredential:    mapBool(row["has_credential"]),
		Status:           mapStr(row["status"]),
		CreatedBy:        mapStr(row["created_by"]),
		CreatedAt:        mapStr(row["created_at"]),
		UpdatedAt:        mapStr(row["updated_at"]),
	}
}

// CreateErpConnection 处理 POST /api/v1/admin/erp-connections。
func (h *Handler) CreateErpConnection(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createErpConnectionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	var credRaw []byte
	if req.Credential != nil {
		credRaw = req.Credential.marshal()
	}
	in := store.CreateErpConnectionInput{
		SystemCode: req.SystemCode,
		Name:       req.Name,
		BaseURL:    req.BaseURL,
		// credential_ref 直接落引用名；界面录入的 credential 走 JSON 序列化 + 加密。
		CredentialRef: req.CredentialRef,
		Status:        req.Status,
		CreatedBy:     id.SubjectID.String(),
	}
	if len(credRaw) > 0 {
		in.Credential = string(credRaw)
	}
	conn, err := h.deps.Store.CreateErpConnection(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ErpConnectionCreated", "erp_connection.create", "SUCCESS",
		"erp_connection", conn.ID, ids.ID{},
		map[string]any{
			"connection_id":     conn.ID,
			"system_code":       conn.SystemCode,
			"name":              conn.Name,
			"base_url":          conn.BaseURL,
			"credential_source": conn.CredentialSource,
			"credential_masked": conn.CredentialMasked,
			"idempotency_key":   idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "ErpConnectionCreated", sse.Important,
		map[string]any{"connection_id": conn.ID, "system_code": conn.SystemCode})
	h.writeJSON(w, http.StatusCreated, conn)
}

type updateErpConnectionRequest struct {
	Name          *string `json:"name,omitempty"`
	BaseURL       *string `json:"base_url,omitempty"`
	CredentialRef *string `json:"credential_ref,omitempty"`
	// Credential 三态：省略=不改；非空=替换；显式空对象 `{}`=清空（须保留 credential_ref）。
	Credential *credentialInput `json:"credential,omitempty"`
	Status     *string          `json:"status,omitempty"`
}

// UpdateErpConnection 处理 PATCH /api/v1/admin/erp-connections/{id}。
func (h *Handler) UpdateErpConnection(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	connID, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "连接 id 不合法"))
		return
	}
	var req updateErpConnectionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	in := store.UpdateErpConnectionInput{
		Name:          req.Name,
		BaseURL:       req.BaseURL,
		CredentialRef: req.CredentialRef,
		Status:        req.Status,
	}
	if req.Credential != nil {
		if req.Credential.nonEmpty() {
			raw := req.Credential.marshal()
			s := string(raw)
			in.Credential = &s
		} else {
			// 显式空对象 = 清空凭据。
			empty := ""
			in.Credential = &empty
		}
	}
	conn, err := h.deps.Store.UpdateErpConnection(r.Context(), connID, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ErpConnectionUpdated", "erp_connection.update", "SUCCESS",
		"erp_connection", conn.ID, ids.ID{},
		map[string]any{
			"connection_id":     conn.ID,
			"credential_source": conn.CredentialSource,
			"credential_masked": conn.CredentialMasked,
			"idempotency_key":   idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "ErpConnectionUpdated", sse.Important,
		map[string]any{"connection_id": conn.ID})
	h.writeJSON(w, http.StatusOK, conn)
}

// DeleteErpConnection 处理 DELETE /api/v1/admin/erp-connections/{id}（软删）。
func (h *Handler) DeleteErpConnection(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	connID, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "连接 id 不合法"))
		return
	}
	echoIdempotency(w, r)
	if err := h.deps.Store.SoftDeleteErpConnection(r.Context(), connID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ErpConnectionDeleted", "erp_connection.delete", "SUCCESS",
		"erp_connection", connID.String(), ids.ID{},
		map[string]any{"connection_id": connID.String(), "idempotency_key": idempotencyKey(r)})
	h.publish(id.TenantID.String(), "ErpConnectionDeleted", sse.Important,
		map[string]any{"connection_id": connID.String()})
	h.writeJSON(w, http.StatusOK, map[string]any{"id": connID.String(), "status": "DELETED"})
}

type testErpConnectionRequest struct {
	SystemCode string           `json:"system_code"`
	BaseURL    string           `json:"base_url"`
	Credential *credentialInput `json:"credential,omitempty"`
}

// TestErpConnection 处理 POST /api/v1/admin/erp-connections/test。
//
// 用用户录入的参数直连真实探针（nc65：Health——未配置健康路径时退化为真实登录）。
// 不落库、不产生任何业务写入；凭据明文只在本请求内存中存活，绝不进日志/审计/SSE。
func (h *Handler) TestErpConnection(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	_ = id
	var req testErpConnectionRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if !strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://") {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "base_url 必填且须以 http:// 或 https:// 开头"))
		return
	}

	switch req.SystemCode {
	case "nc65":
		// 映射目录/端点路径等企业级元数据来自环境（与运行时装配一致）；
		// 地址与登录凭据以本次录入为准——测试的就是用户将要保存的连接参数。
		envCfg, enabled, cerr := nc65.ConfigFromEnv(os.Getenv)
		if cerr != nil {
			h.fail(w, r, errors.E(errors.CodeUnavailable,
				"NC65 映射配置无效（ODAP_NC65_MAPPING_FILE 等），无法构造探针："+cerr.Error()))
			return
		}
		envCfg.BaseURL = req.BaseURL
		if req.Credential != nil {
			envCfg.Usercode = req.Credential.Usercode
			envCfg.Password = req.Credential.Password
		}
		if !enabled && len(envCfg.Objects) == 0 {
			// 未配置 NC65 对象映射时，完整业务能力无法验证；此处退化为对
			// base_url 的真实 HTTP 连通性探针（仍是一次真实网络请求，不是假成功），
			// 并明确标注"仅验证连通性、未验证业务能力"。
			reachable, msg := probeReachable(r.Context(), req.BaseURL)
			h.writeJSON(w, http.StatusOK, map[string]any{
				"reachable": reachable,
				"healthy":   reachable,
				"message":   msg,
			})
			return
		}
		ad, aerr := nc65.New(envCfg)
		if aerr != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "NC65 参数不合法："+aerr.Error()))
			return
		}
		health, herr := ad.Health(r.Context(), erp.ConnectionID("probe"))
		if herr != nil {
			h.writeJSON(w, http.StatusOK, map[string]any{
				"reachable": false,
				"healthy":   false,
				"message":   "连接测试失败：" + herr.Error(),
			})
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{
			"reachable": true,
			"healthy":   health.Healthy,
			"status":    health.Status,
			"message":   health.Message,
		})
	default:
		h.fail(w, r, errors.E(errors.CodeInvalidArgument,
			"该 ERP 类型的适配器尚未实现，无法执行连接测试（当前支持：nc65）"))
	}
}

// probeReachable 对 base_url 发起一次真实 HTTP 探针，仅验证地址连通性。
//
// 用于 NC65 对象映射未配置（无法验证业务能力）时给出真实、可解释的结论，
// 而不是无依据地报"失败"或"成功"。
func probeReachable(ctx context.Context, baseURL string) (bool, string) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL, nil)
	if err != nil {
		return false, "地址格式不合法：" + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "地址不可达：" + err.Error()
	}
	defer resp.Body.Close()
	return true, fmt.Sprintf(
		"地址可达（HTTP %d）；未配置 NC65 对象映射（ODAP_NC65_MAPPING_FILE），本次仅验证地址连通性，未验证业务能力",
		resp.StatusCode)
}

// resolveErpPort 为工具执行器解析 ERP Port（租户连接优先，env 传统装配显式回落）。
//
// 返回 (port, connectionID, usedSource, err)：usedSource 标明端口来源
// （"db:<id>" 或 "env"），供调用方留痕与诊断，绝不静默混合两种来源。
func (h *Handler) resolveErpPort(r *http.Request, systemCode string) (erp.Port, string, string, error) {
	// 1) 租户级 DB 连接优先。
	secret, serr := h.deps.Store.ResolveActiveErpConnection(r.Context(), systemCode)
	if serr != nil {
		return nil, "", "", serr
	}
	if secret != nil {
		if systemCode == "nc65" {
			envCfg, _, cerr := nc65.ConfigFromEnv(os.Getenv) // 映射目录来自 env（企业级元数据）
			if cerr != nil {
				return nil, "", "", errors.E(errors.CodeUnavailable,
					"NC65 映射配置无效（ODAP_NC65_MAPPING_FILE 等）："+cerr.Error())
			}
			envCfg.BaseURL = secret.BaseURL
			cred := parseCredentialJSON(secret.Credential)
			envCfg.Usercode = cred.Usercode
			envCfg.Password = cred.Password
			ad, aerr := nc65.New(envCfg)
			if aerr != nil {
				return nil, "", "", errors.E(errors.CodeUnavailable,
					"按租户连接装配 NC65 适配器失败："+aerr.Error())
			}
			return ad, secret.ID, "db:" + secret.ID, nil
		}
		return nil, "", "", errors.E(errors.CodeUnavailable,
			"ERP 类型「"+systemCode+"」的适配器尚未实现，无法装配连接")
	}
	// 2) env 装配的传统端口（mockerp 演示 ERP / 全局 NC65），显式标注来源。
	if h.deps.ERP != nil {
		return h.deps.ERP, h.deps.ERPConnectionID, "env", nil
	}
	return nil, "", "", errors.E(errors.CodeUnavailable,
		"未配置可用的 ERP 连接：请在「与ERP集成」页创建连接，或为 API 进程配置 ERP 环境变量")
}
