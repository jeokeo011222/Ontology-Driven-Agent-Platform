package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/secretbox"
)

// AddModelEndpoint 给已有供应商新增一个端点。
//
// 为什么"必须先有供应商"：llm.endpoints.provider_id 是 FK 到 llm.providers.provider_id。
// 供应商存在性由数据库 FK + SELECT 双重校验——避免静默接受一个不存在的 provider_id，
// 让用户以为建好了端点但实际 FK 拒绝。
//
// 校验与 CreateModelProvider 的端点部分同源（失败条件一致，避免"建供应商能过、
// 加端点却挂"的不对称）。
func (s *Store) AddModelEndpoint(ctx context.Context, providerID, endpointName, deploymentType,
	regionCode, baseURL, credentialRef, apiKey, status string) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	if err := ValidateProviderID(providerID); err != nil {
		return nil, err
	}

	endpointName = strings.TrimSpace(endpointName)
	deploymentType = strings.TrimSpace(deploymentType)
	baseURL = strings.TrimSpace(baseURL)
	credentialRef = strings.TrimSpace(credentialRef)
	if endpointName == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "endpoint_name 必填")
	}
	if !modelDeploymentTypes[deploymentType] {
		return nil, errors.E(errors.CodeInvalidArgument, "deployment_type 仅支持 HTTP（MOCK 已下线）")
	}
	if status == "" {
		status = "ACTIVE"
	}
	if !modelStatuses[status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 仅支持 ACTIVE / DISABLED")
	}

	switch deploymentType {
	case "HTTP":
		if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
			return nil, errors.E(errors.CodeInvalidArgument,
				"deployment_type=HTTP 时 base_url 必填且须以 http:// 或 https:// 开头")
		}
		if apiKey != "" && credentialRef != "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"api_key 与 credential_ref 二选一：不能同时提供（否则凭据来源不唯一）")
		}
		if apiKey == "" && credentialRef == "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"deployment_type=HTTP 时须提供 api_key（加密存储）或 credential_ref（环境变量引用名）之一")
		}
	}

	// 供应商必须存在（数据库 FK 会兜底，但先查一遍返回更友好的错误）。
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM llm.providers WHERE provider_id=$1)`,
		providerID).Scan(&exists); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "检查供应商存在性失败")
	}
	if !exists {
		return nil, errors.E(errors.CodeNotFound, "模型供应商不存在："+providerID)
	}

	endpointID := ids.New()

	// 凭据落库形态：api_key → 密文 + 打码值；credential_ref → 引用名。二者互斥。
	var (
		credCiphertext []byte
		credMasked     string
		credRef        = credentialRef
		credUpdated    *time.Time
	)
	if apiKey != "" {
		if s.cipher == nil {
			return nil, errors.E(errors.CodeUnavailable,
				"服务端未配置凭据主密钥（ODAP_SECRET_MASTER_KEY），无法加密存储密钥")
		}
		ct, cerr := s.cipher.Encrypt([]byte(apiKey), secretbox.AADFor(endpointID.String()))
		if cerr != nil {
			return nil, errors.Wrap(cerr, errors.CodeInternal, "加密端点凭据失败")
		}
		credCiphertext = ct
		credMasked = secretbox.Mask(apiKey)
		credRef = ""
		now := time.Now().UTC()
		credUpdated = &now
	}

	tag, err := s.Pool.Exec(ctx, `
        INSERT INTO llm.endpoints
            (endpoint_id, provider_id, endpoint_name, deployment_type,
             region_code, credential_ref, base_url, status,
             credential_ciphertext, credential_masked, credential_updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		endpointID.String(), providerID, endpointName, deploymentType,
		nullableStr(regionCode), nullableStr(credRef), nullableStr(baseURL), status,
		nullableBytes(credCiphertext), nullableStr(credMasked), credUpdated)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "创建模型端点失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, errors.E(errors.CodeConflict, "端点创建未生效")
	}

	return map[string]any{
		"endpoint_id":       endpointID.String(),
		"provider_id":       providerID,
		"endpoint_name":     endpointName,
		"status":            status,
		"credential_masked": credMasked,
		"credential_source": credentialSource(credRef, credCiphertext),
		"has_credential":    credRef != "" || len(credCiphertext) > 0,
	}, nil
}

// credentialSource 由两个来源的填充情况推导凭据来源枚举（不回显任何原文）。
func credentialSource(ref string, ciphertext []byte) string {
	switch {
	case len(ciphertext) > 0:
		return "CIPHERTEXT"
	case ref != "":
		return "ENV_REF"
	default:
		return "NONE"
	}
}

// UpdateModelEndpointInput 是端点部分更新的输入（nil 指针 = 该字段不更新）。
//
// 用指针而非空串表达"未提供"，是为了让 api_key 具备**三态**语义：
//
//	nil        —— 不修改现有密钥
//	非空串     —— 替换为新的密文凭据
//	显式空串   —— 清空密文凭据（此后 HTTP 端点必须回落到 credential_ref）
//
// 若用空串兼表"不提供"，就无法区分"我不想动密钥"与"我要把密钥删掉"。
type UpdateModelEndpointInput struct {
	EndpointName   *string
	DeploymentType *string
	RegionCode     *string
	BaseURL        *string
	CredentialRef  *string
	APIKey         *string
	Status         *string
}

func derefTrim(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// UpdateModelEndpoint 白名单更新端点。
//
// 白名单字段：endpoint_name / deployment_type / region_code / base_url /
// credential_ref / api_key（三态）/ status。
// MOCK 部署形态已下线，deployment_type 只允许 HTTP；改动它会丢弃/重建地址与凭据，
// 因此只允许在**填全了对应字段**时才改，并一次性校验通过。
// 单独改 status 或 endpoint_name 不受此限制。
//
// provider_id 永远不允许改（改了就等于"把端点搬到另一个供应商"，会破坏 audit 追溯链）。
func (s *Store) UpdateModelEndpoint(ctx context.Context, endpointID string,
	in UpdateModelEndpointInput) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}

	endpointName := derefTrim(in.EndpointName)
	deploymentType := derefTrim(in.DeploymentType)
	regionCode := derefTrim(in.RegionCode)
	baseURL := derefTrim(in.BaseURL)
	credentialRef := derefTrim(in.CredentialRef)
	status := derefTrim(in.Status)

	apiKeyProvided := in.APIKey != nil
	apiKey := derefTrim(in.APIKey)

	if endpointName == "" && deploymentType == "" && regionCode == "" && baseURL == "" &&
		credentialRef == "" && status == "" && !apiKeyProvided {
		return nil, errors.E(errors.CodeInvalidArgument, "至少提供一个要更新的字段")
	}
	if status != "" && !modelStatuses[status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 仅支持 ACTIVE / DISABLED")
	}
	if deploymentType != "" && !modelDeploymentTypes[deploymentType] {
		return nil, errors.E(errors.CodeInvalidArgument, "deployment_type 仅支持 HTTP（MOCK 已下线）")
	}

	// 取当前行，做"部分更新"时的上下文校验（比如 deployment_type 不改但 HTTP 字段要校验）。
	current, err := s.getEndpoint(ctx, endpointID)
	if err != nil {
		return nil, err
	}

	// 决定最终生效的 deployment_type 与 base_url：
	//   ① 显式给了 deployment_type → 用新值，并按 HTTP 要求补齐 URL（空串视为"不填"，沿用原值）
	//   ② 没给 deployment_type → 沿用原值
	// MOCK 部署形态已下线：若读到历史遗留的 MOCK 行，一律拒绝更新并提示改为 HTTP，
	// 绝不"顺手"当成 HTTP 处理 —— 那会留下一个地址/凭据残缺的端点。
	newDepType := deploymentType
	if newDepType == "" {
		newDepType = strOrEmpty(current["deployment_type"])
	}
	if newDepType != "HTTP" {
		return nil, errors.E(errors.CodeInvalidArgument,
			"该端点 deployment_type="+newDepType+" 已下线；请改为 HTTP 并补齐 base_url 与凭据后重试")
	}
	newBaseURL := baseURL
	if newBaseURL == "" {
		newBaseURL = strOrEmpty(current["base_url"])
	}
	if !strings.HasPrefix(newBaseURL, "http://") && !strings.HasPrefix(newBaseURL, "https://") {
		return nil, errors.E(errors.CodeInvalidArgument,
			"HTTP 端点的 base_url 须以 http:// 或 https:// 开头")
	}

	// endpoint_name 只做 trim，不做空值拒绝——空值视为"不改"。
	if endpointName != "" {
		endpointName = strings.TrimSpace(endpointName)
	}

	// ---- 凭据解析（唯一来源约束 + api_key 三态）----
	refProvided := in.CredentialRef != nil && credentialRef != ""
	apiKeyNew := apiKeyProvided && apiKey != ""
	apiKeyClear := apiKeyProvided && apiKey == ""

	var (
		credChange     bool
		newCredRef     *string
		newCiphertext  []byte
		newMasked      string
		newCredUpdated *time.Time
	)
	switch {
	case refProvided && apiKeyNew:
		return nil, errors.E(errors.CodeInvalidArgument,
			"api_key 与 credential_ref 二选一：不能同时提供（否则凭据来源不唯一）")

	case refProvided:
		// 切换到环境变量引用路径：清空密文，只留引用名。
		credChange = true
		ref := credentialRef
		newCredRef, newCiphertext, newMasked = &ref, nil, ""

	case apiKeyNew:
		if s.cipher == nil {
			return nil, errors.E(errors.CodeUnavailable,
				"服务端未配置凭据主密钥（ODAP_SECRET_MASTER_KEY），无法加密存储密钥")
		}
		ct, cerr := s.cipher.Encrypt([]byte(apiKey), secretbox.AADFor(endpointID))
		if cerr != nil {
			return nil, errors.Wrap(cerr, errors.CodeInternal, "加密端点凭据失败")
		}
		now := time.Now().UTC()
		credChange = true
		newCredRef, newCiphertext, newMasked, newCredUpdated = nil, ct, secretbox.Mask(apiKey), &now

	case apiKeyClear:
		// 清空密文后必须回落到一个非空 credential_ref，否则 HTTP 端点将失去唯一凭据来源。
		fallback := credentialRef
		if fallback == "" {
			fallback = strOrEmpty(current["credential_ref"])
		}
		if fallback == "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"清空密文密钥后 HTTP 端点必须保留 credential_ref 环境变量引用，否则将失去唯一凭据来源")
		}
		credChange = true
		newCredRef, newCiphertext, newMasked = &fallback, nil, ""

	default:
		// 密钥保持不变：不触碰 credential 列（无需读取现有密文）。
		credChange = false
	}

	// 最终一致性：HTTP 端点必须有且仅有一个凭据来源。
	if newDepType == "HTTP" {
		effectiveRef := strOrEmpty(current["credential_ref"])
		hasCipher := current["has_credential"] == true && effectiveRef == ""
		if credChange {
			effectiveRef = ""
			hasCipher = len(newCiphertext) > 0
			if newCredRef != nil {
				effectiveRef = *newCredRef
			}
		}
		if (effectiveRef != "") == hasCipher {
			return nil, errors.E(errors.CodeInvalidArgument,
				"HTTP 端点必须恰好有一个凭据来源（api_key 或 credential_ref）")
		}
	}

	// UPDATE ... RETURNING 走通用行映射：本包约定用 rows.Values() 取回结果
	// （uuid/bool 等的解码差异由 normalizeValue 统一），不用裸 Scan 拼列序。
	rows, err := s.queryMapsNoTenant(ctx, `
        UPDATE llm.endpoints
           SET endpoint_name   = COALESCE(NULLIF($2, ''), endpoint_name),
               deployment_type = $3,
               region_code    = $4,
               base_url       = $5,
               status         = COALESCE(NULLIF($6, ''), status),
               credential_ref        = CASE WHEN $7 THEN $8  ELSE credential_ref        END,
               credential_ciphertext = CASE WHEN $7 THEN $9::bytea ELSE credential_ciphertext END,
               credential_masked     = CASE WHEN $7 THEN $10 ELSE credential_masked     END,
               credential_updated_at = CASE WHEN $7 THEN $11 ELSE credential_updated_at END
         WHERE endpoint_id = $1::uuid
         RETURNING endpoint_id::text, provider_id, endpoint_name, deployment_type,
                   region_code, base_url, credential_ref, status,
                   coalesce(credential_masked, '') AS credential_masked,
                   (credential_ciphertext IS NOT NULL) AS has_credential`,
		endpointID, nullableStr(endpointName), newDepType,
		nullableStr(regionCode), nullableStr(newBaseURL), nullableStr(status),
		credChange, newCredRef, nullableBytes(newCiphertext), nullableStr(newMasked), newCredUpdated)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.E(errors.CodeNotFound, "模型端点不存在")
	}
	out := rows[0]
	out["credential_source"] = credentialSource(strOrEmpty(out["credential_ref"]), ciphertextMarker(out))
	return out, nil
}

// ciphertextMarker 把 has_credential 布尔投影回"是否有密文"的判定信号。
func ciphertextMarker(out map[string]any) []byte {
	if out["has_credential"] == true {
		return []byte{1}
	}
	return nil
}

// DeleteModelEndpoint 删除端点，带下游保护。
//
// 保护条件（全部满足才允许删除）：
//
//	① 没有 llm.model_revisions 引用该端点（版本档案必须随端点一起消失或先清理）
//	② 没有 llm.usage_ledger 引用该端点（账本是审计真值，不能删；如果有账本，拒绝）
//
// 两条保护任一条不满足 → 返回 409 Conflict + 明确原因，让用户知道该先处理什么。
//
// 已移除的规则：此前还有"供应商必须还剩至少一个其他端点"的第三条保护。它属于
// 产品策略而非数据完整性，且在单端点供应商上会让用户**无法删除任何一个端点**
// （连自己刚建错的端点也删不掉）——与"允许用户从错误配置中恢复"冲突。供应商在
// 零端点时会被 Model Registry 装配期直接忽略（见 cmd/api/llm_gateway.go），不会
// 造成"空壳"错误；需要去掉空壳时删除整个供应商即可（DeleteModelProvider 已支持）。
func (s *Store) DeleteModelEndpoint(ctx context.Context, endpointID string) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}

	// ① model_revisions 引用检查
	var revCount int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM llm.model_revisions WHERE endpoint_id = $1::uuid`,
		endpointID).Scan(&revCount); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查版本档案引用失败")
	}
	if revCount > 0 {
		return errors.E(errors.CodeConflict,
			fmt.Sprintf("该端点已关联 %d 条模型版本档案，请先清理版本档案再删除端点", revCount))
	}

	// ② usage_ledger 引用检查（账本是审计真值，永不硬删；有账本就拒绝删除端点）
	var ledgerCount int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM llm.usage_ledger WHERE endpoint_id = $1::uuid`,
		endpointID).Scan(&ledgerCount); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查用量账本引用失败")
	}
	if ledgerCount > 0 {
		return errors.E(errors.CodeConflict,
			fmt.Sprintf("该端点已产生 %d 条用量账本记录（审计真值不可硬删），无法删除端点", ledgerCount))
	}

	tag, err := s.Pool.Exec(ctx, `DELETE FROM llm.endpoints WHERE endpoint_id = $1::uuid`, endpointID)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "删除模型端点失败")
	}
	if tag.RowsAffected() == 0 {
		return errors.E(errors.CodeNotFound, "模型端点不存在")
	}
	return nil
}

// getEndpoint 取单条端点，端点不存在返回 CodeNotFound。
//
// 注意：**绝不 SELECT credential_ciphertext 本体**，只回打码值与"是否有密文"的布尔，
// 避免密文在进程内被无谓地搬运。
func (s *Store) getEndpoint(ctx context.Context, endpointID string) (map[string]any, error) {
	rows, err := s.queryMapsNoTenant(ctx, `
        SELECT endpoint_id::text, provider_id, endpoint_name, deployment_type,
               region_code, base_url, credential_ref, status,
               coalesce(credential_masked, '') AS credential_masked,
               (credential_ciphertext IS NOT NULL) AS has_credential
          FROM llm.endpoints WHERE endpoint_id = $1::uuid`, endpointID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.E(errors.CodeNotFound, "模型端点不存在")
	}
	return rows[0], nil
}

// ModelEndpointProbeTarget 是「模型测试」探测所需的端点地址与**已解析明文凭据**。
//
// Secret 明文只在服务端进程内短暂存在，用于向供应商发起一次最小探测；绝不写入
// 响应、审计、日志或 SSE（第九篇 §4.4）。
type ModelEndpointProbeTarget struct {
	BaseURL string
	Secret  string
}

// GetModelEndpointProbeTarget 载入某端点的探测目标（地址 + 已解析明文凭据）。
//
// 凭据解析复用装配期的**同一套**语义（llm.ResolveEndpointSecret）：密文优先，
// 解密失败绝不回落到 credential_ref，两者皆无即报错。这样"录入期探测通过"与
// "装配期可调用"才具备一致的前提，不会出现探测亮绿灯、路由却不可用的假象。
//
// 端点不存在返回 CodeNotFound；凭据缺失或无法解密返回 CodeUnavailable。
func (s *Store) GetModelEndpointProbeTarget(
	ctx context.Context, endpointID string, getenv func(string) string,
) (ModelEndpointProbeTarget, error) {
	if s == nil || s.Pool == nil {
		return ModelEndpointProbeTarget{}, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	var baseURL, credentialRef string
	var ciphertext []byte
	err := s.Pool.QueryRow(ctx, `
        SELECT coalesce(base_url, ''), coalesce(credential_ref, ''), credential_ciphertext
          FROM llm.endpoints WHERE endpoint_id = $1::uuid`, endpointID,
	).Scan(&baseURL, &credentialRef, &ciphertext)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ModelEndpointProbeTarget{}, errors.E(errors.CodeNotFound, "模型端点不存在")
		}
		return ModelEndpointProbeTarget{}, errors.Wrap(err, errors.CodeInternal, "读取模型端点失败")
	}
	secret, err := llm.ResolveEndpointSecret(llm.EndpointBinding{
		EndpointID:           endpointID,
		BaseURL:              baseURL,
		CredentialRef:        credentialRef,
		CredentialCiphertext: ciphertext,
	}, getenv, s.cipher)
	if err != nil {
		return ModelEndpointProbeTarget{}, errors.Ef(errors.CodeUnavailable, "%s", err.Error())
	}
	return ModelEndpointProbeTarget{BaseURL: baseURL, Secret: secret}, nil
}

// strOrEmpty 把 any 安全转字符串，nil 返回 ""。
func strOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
