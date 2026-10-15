package handlers

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"odap/internal/api/middleware"
	"odap/internal/api/store"
	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// createModelProviderRequest 是 POST /api/v1/admin/model-providers 的请求体。
//
// 凭据安全（§8.2 / §13.2）——本结构提供**两条互斥的凭据来源**，HTTP 端点二选一：
//   - credential_ref：秘密存储中的环境变量引用名（如 ODAP_LLM_API_KEY），凭据本身仍在进程环境里；
//   - api_key：界面录入的明文密钥，**只入不出**——可随请求体传入，但响应与审计绝不回显；
//     服务端在 store 层用 AES-256-GCM 信封加密后落库（credential_ciphertext 列），
//     对外只投影 credential_masked（如 sk-****6e22）与 has_credential。
//
// 响应永不回显任何明文密钥；数据库亦不存明文（见迁移 000029 的列注释）。
type createModelProviderRequest struct {
	ProviderID   string                    `json:"provider_id,omitempty"`
	DisplayName  string                    `json:"display_name"`
	ProviderType string                    `json:"provider_type"`
	Status       string                    `json:"status,omitempty"`
	Endpoint     *createModelEndpointInput `json:"endpoint"`
}

type createModelEndpointInput struct {
	EndpointName   string `json:"endpoint_name"`
	DeploymentType string `json:"deployment_type"`
	RegionCode     string `json:"region_code,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	CredentialRef  string `json:"credential_ref,omitempty"`
	// APIKey 是明文密钥（只入不出）：与 credential_ref 二选一。
	APIKey string `json:"api_key,omitempty"`
	Status string `json:"status,omitempty"`
}

// CreateModelProvider 处理 POST /api/v1/admin/model-providers。
//
// 一次调用同时创建供应商与其**首个端点**。不拆成两个端点：只有端点带
// base_url 与凭据（credential_ref 或 api_key），供应商才可能被 Model Registry
// 装配并真正被调用（internal/llm/registry.go newProvider），只建供应商会留下
// 无法调用的空壳。
func (h *Handler) CreateModelProvider(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	var req createModelProviderRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	if req.Endpoint == nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument,
			"endpoint 必填：仅创建供应商会得到一个无法被调用的空壳（地址与凭据都在端点上）"))
		return
	}
	echoIdempotency(w, r)

	out, err := h.deps.Store.CreateModelProvider(r.Context(), store.CreateModelProviderInput{
		ProviderID:     req.ProviderID,
		DisplayName:    req.DisplayName,
		ProviderType:   req.ProviderType,
		Status:         req.Status,
		EndpointName:   req.Endpoint.EndpointName,
		DeploymentType: req.Endpoint.DeploymentType,
		RegionCode:     req.Endpoint.RegionCode,
		BaseURL:        req.Endpoint.BaseURL,
		CredentialRef:  req.Endpoint.CredentialRef,
		APIKey:         req.Endpoint.APIKey,
		EndpointStatus: req.Endpoint.Status,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	providerID, _ := out["provider_id"].(string)
	// 审计只记录**地址、来源枚举与打码值**，绝不含密钥明文、也不回显引用名。
	h.recordAudit(r, id, "ModelProviderCreated", "model_provider.create", "SUCCESS",
		"model_provider", providerID, ids.ID{},
		map[string]any{
			"provider_id":       providerID,
			"endpoint_id":       out["endpoint_id"],
			"provider_type":     req.ProviderType,
			"deployment_type":   req.Endpoint.DeploymentType,
			"base_url":          req.Endpoint.BaseURL,
			"credential_source": out["credential_source"],
			"credential_masked": out["credential_masked"],
			"has_credential":    out["has_credential"],
			"idempotency_key":   idempotencyKey(r),
		})
	h.writeJSON(w, http.StatusCreated, out)
}

// ---- 端点 CRUD ----

type addModelEndpointRequest struct {
	EndpointName   string `json:"endpoint_name"`
	DeploymentType string `json:"deployment_type"`
	RegionCode     string `json:"region_code,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	CredentialRef  string `json:"credential_ref,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	Status         string `json:"status,omitempty"`
}

// updateModelEndpointRequest 是端点部分更新的请求体（指针表达"是否提供"）。
//
// api_key 的三态语义（见 store.UpdateModelEndpointInput）：
//
//	省略字段   —— 不修改现有密钥
//	非空字符串 —— 替换为新的密文凭据
//	显式空串   —— 清空密文凭据（此后 HTTP 端点须保留 credential_ref）
type updateModelEndpointRequest struct {
	EndpointName   *string `json:"endpoint_name,omitempty"`
	DeploymentType *string `json:"deployment_type,omitempty"`
	RegionCode     *string `json:"region_code,omitempty"`
	BaseURL        *string `json:"base_url,omitempty"`
	CredentialRef  *string `json:"credential_ref,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	Status         *string `json:"status,omitempty"`
}

// AddModelEndpoint 处理 POST /api/v1/admin/model-providers/{id}/endpoints。
func (h *Handler) AddModelEndpoint(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	providerID := r.PathValue("id")
	var req addModelEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	out, err := h.deps.Store.AddModelEndpoint(r.Context(), providerID,
		req.EndpointName, req.DeploymentType, req.RegionCode,
		req.BaseURL, req.CredentialRef, req.APIKey, req.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	endpointID, _ := out["endpoint_id"].(string)
	h.recordAudit(r, id, "ModelEndpointAdded", "model_endpoint.create", "SUCCESS",
		"model_provider", providerID, ids.ID{},
		map[string]any{
			"endpoint_id":       endpointID,
			"endpoint_name":     req.EndpointName,
			"deployment_type":   req.DeploymentType,
			"base_url":          req.BaseURL,
			"credential_source": out["credential_source"],
			"credential_masked": out["credential_masked"],
			"has_credential":    out["has_credential"],
			"idempotency_key":   idempotencyKey(r),
		})
	h.writeJSON(w, http.StatusCreated, out)
}

// UpdateModelEndpoint 处理 PATCH /api/v1/admin/model-endpoints/{endpoint_id}。
func (h *Handler) UpdateModelEndpoint(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	endpointID := r.PathValue("endpoint_id")
	var req updateModelEndpointRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	out, err := h.deps.Store.UpdateModelEndpoint(r.Context(), endpointID, store.UpdateModelEndpointInput{
		EndpointName:   req.EndpointName,
		DeploymentType: req.DeploymentType,
		RegionCode:     req.RegionCode,
		BaseURL:        req.BaseURL,
		CredentialRef:  req.CredentialRef,
		APIKey:         req.APIKey,
		Status:         req.Status,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ModelEndpointUpdated", "model_endpoint.update", "SUCCESS",
		"model_endpoint", endpointID, ids.ID{},
		map[string]any{
			"endpoint_name":     req.EndpointName,
			"deployment_type":   req.DeploymentType,
			"status":            req.Status,
			"credential_source": out["credential_source"],
			"credential_masked": out["credential_masked"],
			"has_credential":    out["has_credential"],
			"idempotency_key":   idempotencyKey(r),
		})
	h.writeJSON(w, http.StatusOK, out)
}

// DeleteModelEndpoint 处理 DELETE /api/v1/admin/model-endpoints/{endpoint_id}。
func (h *Handler) DeleteModelEndpoint(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	endpointID := r.PathValue("endpoint_id")
	echoIdempotency(w, r)
	if err := h.deps.Store.DeleteModelEndpoint(r.Context(), endpointID); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ModelEndpointDeleted", "model_endpoint.delete", "SUCCESS",
		"model_endpoint", endpointID, ids.ID{},
		map[string]any{"idempotency_key": idempotencyKey(r)})
	w.WriteHeader(http.StatusNoContent)
}

// ---- 录入前连通性探测 ----

// probeModelProviderRequest 是 POST /api/v1/admin/model-providers/probe 的请求体。
//
// 探测用**表单里刚填的参数**真实试调一次，因此允许携带明文 api_key（只入不出）。
// 探测的副作用边界：不落库、不写用量账本（账本是业务推理的对账真值，探测会污染它）、
// 审计只记录 deployment_type / 目标主机 / 结果，绝不含密钥。
type probeModelProviderRequest struct {
	DeploymentType string `json:"deployment_type"`
	BaseURL        string `json:"base_url,omitempty"`
	CredentialRef  string `json:"credential_ref,omitempty"`
	APIKey         string `json:"api_key,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	// EndpointID 指名一个**已落库**的端点：表单未提供凭据时，服务端改用该端点
	// 已存凭据（密文或环境变量引用）解析后探测。供「编辑接入」弹窗的「模型测试」
	// 使用——此时密钥已加密存储，不该要求用户为测试重新粘贴明文。
	EndpointID string `json:"endpoint_id,omitempty"`
}

// ProbeModelProvider 处理 POST /api/v1/admin/model-providers/probe。
//
// 该端点不读 Idempotency-Key：它不产生任何写副作用，重复探测是幂等的，
// 且用户会在录入窗口里反复点"测试连接"调整参数。
func (h *Handler) ProbeModelProvider(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req probeModelProviderRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}

	// 地址/凭据解析：优先用表单里刚填的值；表单未填凭据但指名了已存端点时，
	// 改用该端点**已落库**的凭据（密文优先，解密失败不回落）与地址。
	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)
	credentialRef := strings.TrimSpace(req.CredentialRef)
	if apiKey == "" && credentialRef == "" && strings.TrimSpace(req.EndpointID) != "" {
		if h.deps.Store == nil {
			h.fail(w, r, unavailable("持久化适配器未装配"))
			return
		}
		target, terr := h.deps.Store.GetModelEndpointProbeTarget(r.Context(), strings.TrimSpace(req.EndpointID), os.Getenv)
		if terr != nil {
			h.fail(w, r, terr)
			return
		}
		baseURL = target.BaseURL
		apiKey = target.Secret
	}

	res, err := llm.Probe(r.Context(), llm.ProbeRequest{
		DeploymentType: req.DeploymentType,
		BaseURL:        baseURL,
		CredentialRef:  credentialRef,
		APIKey:         apiKey,
		ModelID:        req.ModelID,
	}, os.Getenv)
	if err != nil {
		h.fail(w, r, errors.Wrap(err, errors.CodeInternal, "探测模型端点失败"))
		return
	}

	// 审计结论：成功 → SUCCESS；被端点拒绝（密钥/地址/模型名错）→ REJECTED。
	// 探测失败是**预期内的诊断结果**，不是系统故障，故不记 ERROR。
	outcome := "SUCCESS"
	if !res.OK {
		outcome = "REJECTED"
	}
	h.recordAudit(r, id, "ModelProviderProbed", "model_provider.probe", outcome,
		"model_provider", "", ids.ID{},
		map[string]any{
			"deployment_type": req.DeploymentType,
			"base_url_host":   urlHost(baseURL),
			"endpoint_id":     strings.TrimSpace(req.EndpointID),
			"ok":              res.OK,
			"status_code":     res.StatusCode,
			"request_id":      middleware.RequestIDFromContext(r.Context()),
		})
	h.writeJSON(w, http.StatusOK, map[string]any{"probe": res})
}

// urlHost 只取 URL 的主机名用于审计留痕（去掉可能携带凭据的 userinfo 与查询串）。
func urlHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	host := u.Host
	if i := strings.IndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	return host
}
