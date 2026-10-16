package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/secretbox"
)

// 本文件把 Model Registry（llm.*）与 LLM Gateway 连接起来。
//
// 在此之前，Gateway.Providers 只能由调用方在内存里手工注册 —— 也就是说
// "已登记在 Registry 里的端点"与"运行时真正可被路由的端点"是两套独立的真相，
// 二者只能靠人工保持一致。本文件把 Registry 变成唯一真源：
// 运行时装配完全由 llm.providers / llm.endpoints / llm.model_revisions /
// llm.capability_profiles 决定（第九篇 §5 Model Registry）。
//
// 装配原则（失败必须显式，不允许静默降级）：
//   - 未启用（provider/endpoint/model 状态非 ACTIVE）或能力档案被撤销 → 排除并记录原因；
//   - 凭据缺失/地址缺失等配置缺陷 → 排除并记录原因；
//   - 被排除的 ModelID 若仍存在于其他端点，则仅失去"固定路由"，不影响其他模型；
//   - 请求点名了固定路由模型但端点不可用 → Router 返回 UNAVAILABLE（见 Router.Resolve）。

// EndpointBinding 是一条"供应商 → 端点 → 模型版本 → 能力档案"的绑定。
type EndpointBinding struct {
	ProviderID     string
	ProviderType   string
	CustomerRegion string

	EndpointID     string
	EndpointName   string
	DeploymentType string
	RegionCode     string
	CredentialRef  string
	BaseURL        string

	// CredentialCiphertext 是界面录入路径下的 AES-256-GCM 信封密文凭据（可为 nil）。
	// 与 CredentialRef 二选一（数据库 CHECK ck_endpoints_single_credential_source 保证）。
	// 装配时密文优先，解密失败绝不回落到 CredentialRef（见 newProvider）。
	CredentialCiphertext []byte

	ModelRevisionID  string
	ModelID          string
	RevisionLabel    string
	Capabilities     ProviderCapabilities
	CapabilityStatus string
	CapabilityRev    string
}

// ProviderKey 是绑定在 Gateway.Providers 中的唯一键。
//
// 用"供应商/端点/模型@版本"三元组而不是 model_id：同一个 model_id 可能部署在
// 多个端点（例如公有云与私有部署），它们的数据边界、区域与能力并不相同，
// 必须能被分别寻址；否则多端点登记会互相覆盖。
func (b EndpointBinding) ProviderKey() string {
	return b.ProviderID + "/" + b.EndpointName + "/" + b.ModelID + "@" + b.RevisionLabel
}

// Exclusion 记录一条未能装配或被排除的绑定及其原因。
//
// 为什么必须记录而不是直接丢弃：被静默跳过的端点会让"已登记"与"可调用"
// 看起来一致而实际不一致 —— 排查时既看不到行，也看不到原因（第九篇 §6.2）。
type Exclusion struct {
	ProviderID   string
	EndpointID   string
	EndpointName string
	ModelID      string
	Reason       string
}

// RegistryLoader 从 Model Registry 装载端点绑定。
type RegistryLoader struct {
	Pool *pgxpool.Pool
}

// registryLoadSQL 读取全部"供应商 → 端点"及其模型版本与能力档案。
//
// 为什么是 LEFT JOIN 而不是 INNER JOIN：新建供应商与其首个端点的写入口
//
//	（POST /api/v1/admin/model-providers）允许登记一个**尚无模型版本**的端点
//	—— 这是合法的中间状态。INNER JOIN 会让这种端点**一行都不产生**，于是它
//	既不在 bindings、也不在 exclusions 里：UI 显示"已创建"，而装配诊断
//	对此一言不发。这正是本文件开头禁止的"静默跳过"（第九篇 §6.2）。
//	改为 LEFT JOIN 后，这类端点会带着明确原因进入 exclusions。
const registryLoadSQL = `
SELECT p.provider_id, p.provider_type, p.status AS provider_status,
       e.endpoint_id::text, e.endpoint_name, e.deployment_type,
       coalesce(e.region_code, ''), coalesce(e.credential_ref, ''), coalesce(e.base_url, ''),
       e.credential_ciphertext,
       e.status AS endpoint_status,
       coalesce(m.model_revision_id::text, ''), coalesce(m.model_id, ''), coalesce(m.revision_label, ''),
       coalesce(m.status, '') AS model_status,
       coalesce(cp.capabilities, '{}'::jsonb), coalesce(cp.status, '') AS capability_status,
       coalesce(cp.revision, '') AS capability_revision
  FROM llm.providers p
  JOIN llm.endpoints e ON e.provider_id = p.provider_id
  LEFT JOIN llm.model_revisions m ON m.endpoint_id = e.endpoint_id
  LEFT JOIN llm.capability_profiles cp ON cp.capability_digest = m.capability_digest
 ORDER BY p.provider_id, e.endpoint_name, m.model_id, m.revision_label`

// Load 读取全部绑定；未启用的绑定以 Exclusion 形式一并返回（不静默丢弃）。
func (l *RegistryLoader) Load(ctx context.Context) ([]EndpointBinding, []Exclusion, error) {
	if l.Pool == nil {
		return nil, nil, errors.E(errors.CodeInternal, "llm: 未提供数据库连接池")
	}
	rows, err := l.Pool.Query(ctx, registryLoadSQL)
	if err != nil {
		return nil, nil, errors.Wrap(err, errors.CodeInternal, "llm: 读取 Model Registry 失败")
	}
	defer rows.Close()

	var (
		bindings []EndpointBinding
		excluded []Exclusion
	)
	for rows.Next() {
		var (
			b                                 EndpointBinding
			provStatus, epStatus, modelStatus string
			capsRaw                           []byte
			capStatus, capRev                 string
			providerType                      string
		)
		if err := rows.Scan(
			&b.ProviderID, &providerType, &provStatus,
			&b.EndpointID, &b.EndpointName, &b.DeploymentType,
			&b.RegionCode, &b.CredentialRef, &b.BaseURL, &b.CredentialCiphertext, &epStatus,
			&b.ModelRevisionID, &b.ModelID, &b.RevisionLabel, &modelStatus,
			&capsRaw, &capStatus, &capRev,
		); err != nil {
			return nil, nil, errors.Wrap(err, errors.CodeInternal, "llm: 解析 Model Registry 行失败")
		}
		b.ProviderType = providerType
		b.CapabilityStatus = capStatus
		b.CapabilityRev = capRev

		ex := Exclusion{ProviderID: b.ProviderID, EndpointID: b.EndpointID, EndpointName: b.EndpointName, ModelID: b.ModelID}
		switch {
		case provStatus != "ACTIVE":
			ex.Reason = fmt.Sprintf("供应商状态为 %s（非 ACTIVE）", provStatus)
		case epStatus != "ACTIVE":
			ex.Reason = fmt.Sprintf("端点状态为 %s（非 ACTIVE）", epStatus)
		case b.ModelRevisionID == "":
			// 有供应商+端点，但尚无模型版本：合法中间状态，必须留痕。
			// 否则该端点既不在 bindings 也不在 exclusions —— 界面说"已创建"，
			// 装配诊断却只字不提，排查时看不到行也看不到原因。
			ex.Reason = "该端点尚未登记任何模型版本（llm.model_revisions 无对应行），不参与路由；" +
				"请为其登记模型版本与能力档案"
		case modelStatus != "ACTIVE":
			ex.Reason = fmt.Sprintf("模型版本状态为 %s（非 ACTIVE）", modelStatus)
		case b.CapabilityStatus == "":
			// 能力档案缺失比"不可解析"更早、也更明确：连引用都指不到行。
			ex.Reason = "模型版本引用的能力档案在 llm.capability_profiles 中不存在" +
				"（capability_digest 无匹配行）"
		case capStatus == "REVOKED":
			ex.Reason = "能力档案已被撤销（REVOKED）"
		}
		if ex.Reason != "" {
			excluded = append(excluded, ex)
			continue
		}

		caps, err := decodeCapabilitiesJSON(capsRaw)
		if err != nil {
			ex.Reason = "能力档案不可解析: " + err.Error()
			excluded = append(excluded, ex)
			continue
		}
		b.Capabilities = caps

		if capStatus == "UNVERIFIED" {
			// 未验证能力不阻断装配（否则演示环境永远起不来），但必须留痕：
			// 第九篇 §4.4 要求能力声明经过验证，未验证项在排查与审计时必须可见。
			excluded = append(excluded, Exclusion{
				ProviderID: b.ProviderID, EndpointID: b.EndpointID, EndpointName: b.EndpointName,
				ModelID: b.ModelID, Reason: "能力档案状态为 UNVERIFIED（仍参与路由，但能力未经探测验证）",
			})
		}
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, errors.Wrap(err, errors.CodeInternal, "llm: 遍历 Model Registry 失败")
	}
	return bindings, excluded, nil
}

// capabilitiesJSON 是 llm.capability_profiles.capabilities 的结构化视图。
type capabilitiesJSON struct {
	NativeSchema     bool `json:"native_schema"`
	SupportsSeed     bool `json:"supports_seed"`
	SupportsTools    bool `json:"supports_tools"`
	Embeddings       bool `json:"embeddings"`
	MaxContextTokens int  `json:"max_context_tokens"`
}

// decodeCapabilitiesJSON 解析能力档案；未知字段一律报错。
//
// 与 seed 侧同样的理由：能力字段名拼错会让该字段静默取零值，
// 于是端点被登记为"不支持原生 schema"，路由与结构化输出行为随之改变。
func decodeCapabilitiesJSON(raw []byte) (ProviderCapabilities, error) {
	var c capabilitiesJSON
	if len(raw) == 0 {
		return ProviderCapabilities{}, fmt.Errorf("能力档案为空")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return ProviderCapabilities{}, err
	}
	if c.MaxContextTokens <= 0 {
		return ProviderCapabilities{}, fmt.Errorf("max_context_tokens 必须为正整数（实际 %d）", c.MaxContextTokens)
	}
	return ProviderCapabilities{
		NativeSchema:     c.NativeSchema,
		SupportsSeed:     c.SupportsSeed,
		SupportsTools:    c.SupportsTools,
		Embeddings:       c.Embeddings,
		MaxContextTokens: c.MaxContextTokens,
	}, nil
}

// EndpointIdentity 由"已绑定 Registry 身份"的 Provider 实现。
//
// 为什么用包装而不是给每个 Provider 加字段：身份是**装配期**概念，与协议实现无关。
// 包装能让 MockProvider / HTTPProvider 保持"只关心协议"的职责，
// 同时保证任何从 Registry 装配出的 Provider 都必然带身份。
type EndpointIdentity interface {
	Endpoint() (endpointID, modelRevisionID ids.ID)
}

// boundProvider 把 Provider 与它在 Model Registry 中的身份绑定在一起。
type boundProvider struct {
	Provider
	endpointID      ids.ID
	modelRevisionID ids.ID
}

func (b boundProvider) Endpoint() (ids.ID, ids.ID) { return b.endpointID, b.modelRevisionID }

// EndpointOf 取出 Provider 携带的端点身份；未绑定身份时返回零值。
//
// 返回零值而不是报错：手工在内存里注册 Provider（测试、离线评估）是合法用法。
// 但零值会导致账本落不进去，因此 Gateway 在路由后会显式告警（见 Gateway.Complete）。
func EndpointOf(p Provider) (ids.ID, ids.ID) {
	if p == nil {
		return ids.ID{}, ids.ID{}
	}
	if e, ok := p.(EndpointIdentity); ok {
		return e.Endpoint()
	}
	return ids.ID{}, ids.ID{}
}

// GatewayOptions 是 Gateway 的装配参数。治理组件为可选：任一为空表示对应环节不启用。
type GatewayOptions struct {
	Logger *slog.Logger
	// Getenv 解析端点凭据（默认 os.Getenv）。注入它是为了让测试可替换，
	// 也为了明确 credential_ref 路径"凭据只从进程环境读取"。
	Getenv func(string) string

	// Secrets 是凭据信封解密器（默认 nil = 该能力未启用）。
	// 为 nil 时，携带密文凭据的端点在装配期被显式排除（绝不静默回落到别的来源）；
	// 携带 credential_ref 的端点不受影响。装配参数由 cmd/api 从
	// ODAP_SECRET_MASTER_KEY 构造。
	Secrets *secretbox.Cipher

	Limiter *Limiter
	Ledger  *UsageLedger
	Budget  *BudgetGuard
	Cache   *SemanticCache
	Kind    string
}

// Build 从 Registry 装载并装配 Gateway。
func (l *RegistryLoader) Build(ctx context.Context, opt GatewayOptions) (*Gateway, error) {
	bindings, excluded, err := l.Load(ctx)
	if err != nil {
		return nil, err
	}
	return BuildGateway(bindings, excluded, opt), nil
}

// BuildGateway 由端点绑定装配 Gateway。
//
// 不返回 error：单条端点装配失败不应让整个 Gateway 不可用（否则一个配置错误的
// 端点会拖垮全部推理）。失败项进入 gateway.Unavailable，供诊断与 UI 展示；
// 请求点名这些模型时会以 UNAVAILABLE 明确失败，而不是回落到别的模型。
func BuildGateway(bindings []EndpointBinding, excluded []Exclusion, opt GatewayOptions) *Gateway {
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}
	getenv := opt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	providers := map[string]Provider{}
	routing := map[string]string{}
	// known：在 Model Registry 中登记过的全部 ModelID。
	// failures：装配失败的 ModelID → 原因（同一模型多端点时，只要有一处装上就不算失败）。
	known := map[string]bool{}
	failures := map[string]string{}
	unavailable := append([]Exclusion(nil), excluded...)

	for _, b := range bindings {
		known[b.ModelID] = true
	}
	for _, x := range unavailable {
		if x.ModelID != "" {
			known[x.ModelID] = true
			if _, seen := failures[x.ModelID]; !seen {
				failures[x.ModelID] = x.Reason
			}
		}
	}

	// bindings 已由 SQL ORDER BY 定序，装配结果因此是确定性的。
	for _, b := range bindings {
		key := b.ProviderKey()
		fail := func(reason string) {
			if _, seen := failures[b.ModelID]; !seen {
				failures[b.ModelID] = reason
			}
			unavailable = append(unavailable, Exclusion{
				ProviderID: b.ProviderID, EndpointID: b.EndpointID, EndpointName: b.EndpointName,
				ModelID: b.ModelID, Reason: reason,
			})
		}

		p, err := newProvider(b, getenv, opt.Secrets)
		if err != nil {
			fail(err.Error())
			logger.Warn("端点未装配",
				"provider", b.ProviderID, "endpoint", b.EndpointName,
				"model", b.ModelID, "reason", err.Error())
			continue
		}
		// 身份解析失败必须排除该端点：没有身份的端点会让账本无法落账，
		// 而账本写失败按 §16.2 只告警不阻断，缺失会一路静默到对账环节。
		endpointID, err := ids.Parse(b.EndpointID)
		if err != nil {
			fail("端点 ID 非法: " + err.Error())
			continue
		}
		modelRevID, err := ids.Parse(b.ModelRevisionID)
		if err != nil {
			fail("模型版本 ID 非法: " + err.Error())
			continue
		}
		providers[key] = boundProvider{
			Provider:        p,
			endpointID:      endpointID,
			modelRevisionID: modelRevID,
		}

		if existing, dup := routing[b.ModelID]; dup {
			// 同一 model_id 出现在多个端点：保留首个作为固定路由（确定性），
			// 其余仍注册为能力候选，但必须留痕 —— 否则"为什么请求落到了这个端点"
			// 将无法从配置解释。
			unavailable = append(unavailable, Exclusion{
				ProviderID: b.ProviderID, EndpointID: b.EndpointID, EndpointName: b.EndpointName,
				ModelID: b.ModelID,
				Reason:  fmt.Sprintf("同一 model_id 已固定路由到 %s；本条仅作为能力候选注册", existing),
			})
			continue
		}
		routing[b.ModelID] = key
	}

	// Registered 的最终语义：登记过的模型 → ""（可路由）或不可用原因。
	// 必须在装配循环之后统一结算：若在循环中边装边写，同一模型在"先成功后失败"
	// 的顺序下会被误标为不可用，路由因此随 ORDER BY 变化。
	registered := make(map[string]string, len(known))
	for m := range known {
		if _, ok := routing[m]; ok {
			registered[m] = ""
			continue
		}
		if r, ok := failures[m]; ok {
			registered[m] = r
			continue
		}
		registered[m] = "所有端点均未能装配"
	}

	sort.Slice(unavailable, func(i, j int) bool {
		if unavailable[i].ProviderID != unavailable[j].ProviderID {
			return unavailable[i].ProviderID < unavailable[j].ProviderID
		}
		return unavailable[i].ModelID < unavailable[j].ModelID
	})

	return &Gateway{
		Providers:   providers,
		Router:      &Router{Providers: providers, Routing: routing, Registered: registered},
		Cache:       opt.Cache,
		Limiter:     opt.Limiter,
		Ledger:      opt.Ledger,
		Budget:      opt.Budget,
		Logger:      logger,
		Kind:        opt.Kind,
		Unavailable: unavailable,
	}
}

// newProvider 依据部署类型实例化 Provider。
//
// 凭据/地址缺失时返回错误而不是"用一个空配置硬上"：后者的失败会推迟到
// 真正调用时才以供应商错误的形式出现，掩盖"配置本身不完整"这一事实。
func newProvider(b EndpointBinding, getenv func(string) string, cipher *secretbox.Cipher) (Provider, error) {
	switch b.DeploymentType {
	case "MOCK":
		// MOCK 部署形态已下线：它会在进程内返回确定性内容，是伪造数据来源。
		// 这里不装配模拟 Provider，只给出明确错误（遗留行会被排除，见 BuildGateway）。
		return nil, fmt.Errorf("deployment_type=MOCK 已下线：系统不再装配确定性模拟端点，请改用真实 HTTP 端点")
	case "HTTP":
		if b.BaseURL == "" {
			return nil, fmt.Errorf("HTTP 端点缺少 base_url（无法寻址）")
		}
		secret, err := ResolveEndpointSecret(b, getenv, cipher)
		if err != nil {
			return nil, err
		}
		return NewHTTPProvider(b.ProviderKey(), b.BaseURL, secret, b.ModelID, b.Capabilities)
	default:
		return nil, fmt.Errorf("未知 deployment_type %q（仅支持 HTTP）", b.DeploymentType)
	}
}

// ResolveEndpointSecret 按"密文优先 → env 引用回落"解析端点凭据。
//
// 顺序与唯一的失败优先级是有意的：
//  1. 有密文 → 必须能解密；解密失败（密文被篡改 / 主密钥已更换 / 未配置主密钥）
//     一律报错，**绝不回落到 credential_ref**。回落会把"密钥换过、密文已死"
//     这类严重配置错误，掩盖成一个看起来还能用的端点。
//  2. 无密文 → 回落 credential_ref + getenv（存量路径，向后兼容）。
//  3. 两者皆无 → 报错（HTTP 端点无凭据无法调用）。
//
// 导出原因：除装配期外，录入期的「模型测试」探测也要用**同一套**凭据解析语义，
// 不能各写一份——两份实现迟早会漂移，让"探测通过但装配失败"变成难查的怪象。
func ResolveEndpointSecret(b EndpointBinding, getenv func(string) string, cipher *secretbox.Cipher) (string, error) {
	if len(b.CredentialCiphertext) > 0 {
		if cipher == nil {
			return "", fmt.Errorf("端点凭据以密文存储，但服务端未配置主密钥（ODAP_SECRET_MASTER_KEY），无法解密")
		}
		plain, err := cipher.Decrypt(b.CredentialCiphertext, secretbox.AADFor(b.EndpointID))
		if err != nil {
			return "", fmt.Errorf("端点凭据解密失败（密文被篡改，或主密钥已更换）：%w", err)
		}
		if len(plain) == 0 {
			return "", fmt.Errorf("端点密文凭据解密结果为空（异常密文）")
		}
		return string(plain), nil
	}
	if b.CredentialRef == "" {
		return "", fmt.Errorf("HTTP 端点缺少凭据（既无密文，亦无 credential_ref）")
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	secret := getenv(b.CredentialRef)
	if secret == "" {
		return "", fmt.Errorf("凭据环境变量 %s 未配置或为空", b.CredentialRef)
	}
	return secret, nil
}
