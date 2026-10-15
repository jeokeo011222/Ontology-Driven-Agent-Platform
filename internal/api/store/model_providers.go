package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/secretbox"
)

// CreateModelProviderInput 是新建模型供应商（含**首个端点**）的输入。
//
// 为什么必须一次建好端点：llm.providers 本身不含地址与凭据，只有端点带
// base_url / credential_ref 才可能被 Model Registry 装配（见
// internal/llm/registry.go 的 newProvider：HTTP 端点缺 base_url 或缺
// credential_ref 一律报错，绝不"用空配置硬上"）。只建供应商会产出一个
// **永远无法被调用**的空壳——故此处要求同时提供首个端点，fail-closed。
type CreateModelProviderInput struct {
	ProviderID   string
	DisplayName  string
	ProviderType string
	Status       string

	EndpointName   string
	DeploymentType string
	RegionCode     string
	BaseURL        string
	// CredentialRef 与 APIKey 二选一（HTTP 端点至少一个）：
	//   - CredentialRef：秘密存储中的环境变量引用名（存量路径）
	//   - APIKey：明文密钥，仅在本次请求内存中存活；落库前由 store 层加密为密文
	CredentialRef  string
	APIKey         string
	EndpointStatus string
}

// 取值集合与装配侧保持同源：deployment_type 由 internal/llm/registry.go
// 的 newProvider switch 决定；MOCK 部署形态已下线（它会在进程内返回确定性
// 内容，属伪造数据来源），故仅保留 HTTP；provider_type 目前只作展示与诊断。
var (
	modelProviderTypes   = map[string]bool{"OPENAI_COMPATIBLE": true}
	modelDeploymentTypes = map[string]bool{"HTTP": true}
	modelStatuses        = map[string]bool{"ACTIVE": true, "DISABLED": true}
)

// DeriveProviderID 由显示名派生规范 provider_id，并报告派生是否**可信**。
//
// 只有在显示名能无损映射为规范标识时才返回 ok=true：字母数字保留（大写转
// 小写），常见分隔符（空格、-、_、.、/ 等）折叠为单个 '-'；一旦出现任何
// 无法映射的字符（中文、重音字母、emoji……）立即返回 ok=false。
//
// 为什么必须 fail-closed 而不是"跳过该字符继续拼"：provider_id 是
// llm.providers 的主键，会出现在装配日志与"排除原因"里，必须稳定、可读、
// 可复现。此前的实现对不可映射字符一律写成 '-'，于是
// `"E2E 探针供应商"` 被**静默**派生成 `"e2e"`——中文语义整段丢失，且任何
// 以 "E2E" 开头的显示名都会撞上同一个主键；而纯中文名（如"阿斯麦推理服务"）
// 又因为结果为空而报错。同一个函数对两种中文名给出截然不同的行为，
// 属于典型的"静默降级"。现在两种情况统一 fail-closed：要求调用方显式提供
// provider_id，绝不产出一个丢掉语义的短 id。
func DeriveProviderID(displayName string) (string, bool) {
	var b strings.Builder
	lastDash := true // 前导 '-' 不输出
	for _, r := range strings.ToLower(displayName) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case isIDSafeSeparator(r):
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// 不可无损映射的字符：派生结果不可信。
			return "", false
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return "", false
	}
	return id, true
}

// isIDSafeSeparator 判定 r 是否只是分词/可读性分隔符——折叠为 '-' 不丢失身份语义。
func isIDSafeSeparator(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '/', '+', ':', ',', '(', ')', '[', ']', '@', '&', '\'':
		return true
	default:
		return false
	}
}

// provider_id 的规范形状：小写字母/数字，单个连字符分词。
// 该形状同时会出现在契约中 /api/v1/admin/model-providers/{id}/verify 这类
// **路径参数**里，因此必须 URL 安全——显式提供时同样强制，避免
// "派生出来的 id 合规、手填的 id 却可以含 '/' 或 '..'"的不对称。
const (
	providerIDMinLen = 2
	providerIDMaxLen = 63
)

var providerIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateProviderID 校验 provider_id 是否满足规范形状（派生与显式提供一视同仁）。
func ValidateProviderID(id string) error {
	if len(id) < providerIDMinLen || len(id) > providerIDMaxLen {
		return errors.E(errors.CodeInvalidArgument,
			fmt.Sprintf("provider_id 长度须在 %d–%d 之间", providerIDMinLen, providerIDMaxLen))
	}
	if !providerIDRe.MatchString(id) {
		return errors.E(errors.CodeInvalidArgument,
			"provider_id 只允许小写字母、数字与单个连字符（如 acme-llm），且不能以连字符开头或结尾")
	}
	return nil
}

// CreateModelProvider 在单事务内创建模型供应商与其首个端点。
//
// llm schema 是**平台级共享定义**（无 tenant_id、无 RLS，见 000010 与 000016 的
// 逐表结论），因此**不走 TenantTx**；但身份仍由 handler 强制（未认证请求到不了这里），
// 且写入落审计（handler 侧）。
func (s *Store) CreateModelProvider(ctx context.Context, in CreateModelProviderInput) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}

	// ---- 供应商 ----
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	if in.DisplayName == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "display_name 必填")
	}
	if in.ProviderID == "" {
		derived, ok := DeriveProviderID(in.DisplayName)
		if !ok {
			return nil, errors.E(errors.CodeInvalidArgument,
				"display_name 含无法映射为 ASCII 标识的字符（如中文），无法隐式派生 provider_id；"+
					"请显式提供 provider_id（小写字母/数字/连字符，如 acme-llm）")
		}
		in.ProviderID = derived
	}
	if err := ValidateProviderID(in.ProviderID); err != nil {
		return nil, err
	}
	if !modelProviderTypes[in.ProviderType] {
		return nil, errors.E(errors.CodeInvalidArgument, "provider_type 仅支持 OPENAI_COMPATIBLE（MOCK 已下线）")
	}
	if in.Status == "" {
		in.Status = "ACTIVE"
	}
	if !modelStatuses[in.Status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 仅支持 ACTIVE / DISABLED")
	}

	// ---- 首个端点 ----
	in.EndpointName = strings.TrimSpace(in.EndpointName)
	in.DeploymentType = strings.TrimSpace(in.DeploymentType)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	in.CredentialRef = strings.TrimSpace(in.CredentialRef)
	if in.EndpointName == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "endpoint.endpoint_name 必填")
	}
	if !modelDeploymentTypes[in.DeploymentType] {
		return nil, errors.E(errors.CodeInvalidArgument, "endpoint.deployment_type 仅支持 HTTP（MOCK 已下线）")
	}
	if in.EndpointStatus == "" {
		in.EndpointStatus = "ACTIVE"
	}
	if !modelStatuses[in.EndpointStatus] {
		return nil, errors.E(errors.CodeInvalidArgument, "endpoint.status 仅支持 ACTIVE / DISABLED")
	}
	switch in.DeploymentType {
	case "HTTP":
		if !strings.HasPrefix(in.BaseURL, "http://") && !strings.HasPrefix(in.BaseURL, "https://") {
			return nil, errors.E(errors.CodeInvalidArgument,
				"deployment_type=HTTP 时 base_url 必填且须以 http:// 或 https:// 开头")
		}
		// 凭据来源必须唯一：同时给出 api_key 与 credential_ref 会让"以谁为准"产生歧义，
		// 与数据库 CHECK ck_endpoints_single_credential_source 对齐。
		if in.APIKey != "" && in.CredentialRef != "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"api_key 与 credential_ref 二选一：不能同时提供（否则凭据来源不唯一）")
		}
		if in.APIKey == "" && in.CredentialRef == "" {
			return nil, errors.E(errors.CodeInvalidArgument,
				"deployment_type=HTTP 时须提供 api_key（界面录入，加密存储）或 credential_ref（环境变量引用名）之一")
		}
	}

	endpointID := ids.New()

	// 凭据落库形态：api_key → 信封密文 + 打码值（credential_ref 置 NULL）；
	// credential_ref → 直接存引用名（不加密）。明文密钥只在此处短暂存在，
	// 进入 SQL 参数的只有密文。
	var (
		credCiphertext []byte
		credMasked     string
		credRef        = in.CredentialRef
		credUpdated    *time.Time
	)
	if in.APIKey != "" {
		if s.cipher == nil {
			return nil, errors.E(errors.CodeUnavailable,
				"服务端未配置凭据主密钥（ODAP_SECRET_MASTER_KEY），无法加密存储密钥；"+
					"请配置后重试，或改用 credential_ref 环境变量引用路径")
		}
		ct, cerr := s.cipher.Encrypt([]byte(in.APIKey), secretbox.AADFor(endpointID.String()))
		if cerr != nil {
			return nil, errors.Wrap(cerr, errors.CodeInternal, "加密端点凭据失败")
		}
		credCiphertext = ct
		credMasked = secretbox.Mask(in.APIKey)
		credRef = ""
		now := time.Now().UTC()
		credUpdated = &now
	}

	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            INSERT INTO llm.providers (provider_id, display_name, provider_type, status)
            VALUES ($1, $2, $3, $4)
            ON CONFLICT (provider_id) DO NOTHING`,
			in.ProviderID, in.DisplayName, in.ProviderType, in.Status)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "创建模型供应商失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeConflict, "provider_id 已存在："+in.ProviderID)
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO llm.endpoints
                (endpoint_id, provider_id, endpoint_name, deployment_type,
                 region_code, credential_ref, base_url, status,
                 credential_ciphertext, credential_masked, credential_updated_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			endpointID.String(), in.ProviderID, in.EndpointName, in.DeploymentType,
			nullableStr(in.RegionCode), nullableStr(credRef), nullableStr(in.BaseURL),
			in.EndpointStatus, nullableBytes(credCiphertext), nullableStr(credMasked), credUpdated); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "创建模型端点失败")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider_id":       in.ProviderID,
		"endpoint_id":       endpointID.String(),
		"endpoint_name":     in.EndpointName,
		"status":            in.EndpointStatus,
		"credential_masked": credMasked,
		"credential_source": credentialSource(credRef, credCiphertext),
		"has_credential":    credRef != "" || len(credCiphertext) > 0,
	}, nil
}
