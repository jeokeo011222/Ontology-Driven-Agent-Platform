package nc65

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"odap/internal/platform/errors"
)

// 默认端点（用友 NC Cloud 1909+ 的 REST 约定）。运营商可按本企业 NC65 版本
// 通过环境变量覆盖，Adapter 不硬编码任何"猜测可用"的地址。
const (
	defaultTokenPath   = "/nccloud/api/uapbd/pubbusi/getToken"
	defaultTokenHeader = "access_token"
	defaultLangCode    = "zh-CN"
)

// ObjectMapping 把一个 ODAP 规范对象类型映射到 NC65 原生对象。
//
// 字段映射（Fields）是必需的：Adapter 只把 NC65 真实返回的原生字段按此表
// 转录为规范字段，绝不凭空补值。若本体需要某规范字段而原生响应里没有，
// 该对象会被标注为 PARTIAL（数据质量），而不是用零值冒充完整数据。
type ObjectMapping struct {
	// NativeObjectType 是 NC65 原生对象名（如 bd_psndoc / org_dept）。
	NativeObjectType string `json:"native_object_type"`
	// GetMethod/GetPath 是单对象读取的 HTTP 方法与路径；路径可含 {id} 占位符。
	GetMethod string `json:"get_method"`
	GetPath   string `json:"get_path"`
	// QueryMethod/QueryPath 是受控查询的方法与路径。
	QueryMethod string `json:"query_method"`
	QueryPath   string `json:"query_path"`
	// IDField 是原生主键字段名；VersionField 是承载版本（乐观锁/ts）的字段名。
	IDField      string `json:"id_field"`
	VersionField string `json:"version_field"`
	// Fields 是规范字段名 -> 原生字段名。
	Fields map[string]string `json:"fields"`
}

// OperationMapping 把一个 ODAP 规范业务命令映射到 NC65 原生写接口。
type OperationMapping struct {
	// NativeAction 是 NC65 侧动作标识（仅用于留痕与错误映射）。
	NativeAction string `json:"native_action"`
	// Path 是写操作端点路径；Method 默认 POST。
	Method string `json:"method"`
	Path   string `json:"path"`
	// PayloadMap 是规范 payload 字段名 -> 原生字段名。
	PayloadMap map[string]string `json:"payload_map"`
}

// mappingFile 是外置映射文件的顶层结构（ODAP_NC65_MAPPING_FILE 指向）。
//
// 把映射数据放在外部文件而非代码里，是为了让运维按各企业 NC65 的实际对象名
// 与端点配置连接，而不必重新编译服务。
type mappingFile struct {
	Objects               map[string]ObjectMapping    `json:"objects"`
	Operations            map[string]OperationMapping `json:"operations"`
	OperationLookupPath   string                      `json:"operation_lookup_path"`
	OperationLookupMethod string                      `json:"operation_lookup_method"`
	HealthPath            string                      `json:"health_path"`
	MappingVersion        string                      `json:"mapping_version"`
}

// Config 是 NC65 Adapter 的装配配置。
type Config struct {
	// BaseURL 是 NC65 服务地址（必填，如 http://nc65.example.com:8080）。
	BaseURL string
	// Usercode/Password 是 NC65 登录凭据；Password 仅从环境变量注入，绝不落盘或写日志。
	Usercode  string
	Password  string `json:"-"`
	GroupCode string
	LangCode  string

	// TokenPath 是取令牌端点；TokenHeader 是业务请求携带令牌的请求头名。
	TokenPath   string
	TokenHeader string

	// Objects/Operations 是连接的实际能力映射；为空表示该连接不可用。
	Objects    map[string]ObjectMapping
	Operations map[string]OperationMapping

	// OperationLookupPath 是操作结果查询端点；为空表示不支持结果查询
	// （幂等能力据此降级为 L3，结果未知时禁止自动重发）。
	OperationLookupPath   string
	OperationLookupMethod string

	// HealthPath 是健康检查端点；为空时以"取令牌（真实登录）"作为存活探针。
	HealthPath string

	// IdempotencyLevel 是经管理员审核的远端幂等能力（L1|L2|L3）。
	// 留空按 L3 处理（fail-closed）。
	IdempotencyLevel string
	AdapterVersion   string
	MappingVersion   string

	Timeout time.Duration
	// HTTPClient 允许注入自定义客户端（测试用）；为空时按 Timeout 新建。
	HTTPClient *http.Client `json:"-"`
}

// ConfigFromEnv 从环境变量装配 NC65 配置。
//
// 返回 enabled=false 表示未配置 NC65（ODAP_NC65_BASE_URL 为空）：
// 调用方据此保持 ERP 未装配，相关端点 fail-closed 返回 UNAVAILABLE，
// 而不是装配一个假的 Adapter。
func ConfigFromEnv(getenv func(string) string) (cfg Config, enabled bool, err error) {
	base := strings.TrimSpace(getenv("ODAP_NC65_BASE_URL"))
	if base == "" {
		return Config{}, false, nil
	}
	cfg = Config{
		BaseURL:               base,
		Usercode:              strings.TrimSpace(getenv("ODAP_NC65_USERCODE")),
		Password:              getenv("ODAP_NC65_PASSWORD"),
		GroupCode:             strings.TrimSpace(getenv("ODAP_NC65_GROUPCODE")),
		LangCode:              orDefault(strings.TrimSpace(getenv("ODAP_NC65_LANGCODE")), defaultLangCode),
		TokenPath:             orDefault(strings.TrimSpace(getenv("ODAP_NC65_TOKEN_PATH")), defaultTokenPath),
		TokenHeader:           orDefault(strings.TrimSpace(getenv("ODAP_NC65_TOKEN_HEADER")), defaultTokenHeader),
		OperationLookupPath:   strings.TrimSpace(getenv("ODAP_NC65_OPERATION_LOOKUP_PATH")),
		OperationLookupMethod: strings.TrimSpace(getenv("ODAP_NC65_OPERATION_LOOKUP_METHOD")),
		HealthPath:            strings.TrimSpace(getenv("ODAP_NC65_HEALTH_PATH")),
		IdempotencyLevel:      strings.TrimSpace(getenv("ODAP_NC65_IDEMPOTENCY_LEVEL")),
		AdapterVersion:        strings.TrimSpace(getenv("ODAP_NC65_ADAPTER_VERSION")),
	}

	if v := strings.TrimSpace(getenv("ODAP_NC65_TIMEOUT_SECONDS")); v != "" {
		secs, perr := strconv.Atoi(v)
		if perr != nil || secs <= 0 {
			return Config{}, true, errors.Ef(errors.CodeValidationFailed,
				"nc65: ODAP_NC65_TIMEOUT_SECONDS 必须是正整数秒，实际为 %q", v)
		}
		cfg.Timeout = time.Duration(secs) * time.Second
	}

	if v := strings.TrimSpace(getenv("ODAP_NC65_MAPPING_FILE")); v != "" {
		if lerr := loadMappingFile(v, &cfg); lerr != nil {
			return Config{}, true, lerr
		}
	}
	return cfg, true, nil
}

// loadMappingFile 读取外置映射文件并合并进配置。
func loadMappingFile(path string, cfg *Config) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.Wrap(err, errors.CodeValidationFailed, "nc65: 读取映射文件失败")
	}
	var mf mappingFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return errors.Wrap(err, errors.CodeValidationFailed, "nc65: 解析映射文件失败")
	}
	if len(mf.Objects) == 0 {
		return errors.Ef(errors.CodeValidationFailed,
			"nc65: 映射文件 %s 未定义任何 objects（连接将无对象可读写）", path)
	}
	cfg.Objects = mf.Objects
	cfg.Operations = mf.Operations
	if mf.OperationLookupPath != "" {
		cfg.OperationLookupPath = mf.OperationLookupPath
	}
	if mf.OperationLookupMethod != "" {
		cfg.OperationLookupMethod = mf.OperationLookupMethod
	}
	if mf.HealthPath != "" {
		cfg.HealthPath = mf.HealthPath
	}
	if mf.MappingVersion != "" {
		cfg.MappingVersion = mf.MappingVersion
	}
	return nil
}

// Validate 校验装配所需的最小条件；不满足一律 fail-closed。
func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.E(errors.CodeUnavailable, "nc65: 未配置 NC65 服务地址（ODAP_NC65_BASE_URL）")
	}
	if strings.TrimSpace(c.Usercode) == "" || c.Password == "" {
		return errors.E(errors.CodeUnavailable,
			"nc65: 未注入 NC65 登录凭据（ODAP_NC65_USERCODE / ODAP_NC65_PASSWORD）；凭据只从环境变量读取")
	}
	if len(c.Objects) == 0 {
		return errors.E(errors.CodeUnavailable,
			"nc65: 未配置对象映射（ODAP_NC65_MAPPING_FILE），无法把规范对象映射到 NC65 原生对象")
	}
	for name, m := range c.Objects {
		if strings.TrimSpace(m.NativeObjectType) == "" || strings.TrimSpace(m.GetPath) == "" {
			return errors.Ef(errors.CodeValidationFailed,
				"nc65: 对象映射 %q 缺少 native_object_type 或 get_path", name)
		}
	}
	return nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func orDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
