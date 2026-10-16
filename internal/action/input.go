// Package action 实现本体 Action 管理（模块 D「完整版：一等节点 + PROV-O」）。
//
// Action 是平台里"决策/动作"的一等节点：QUERY/UPDATE/INFER/ACTION/TOOL 五类，
// 双存储落位：
//   - 元数据行 → PostgreSQL action.actions（迁移 000044，RLS 租户隔离）；
//   - PROV-O 溯源三元组 → 图存储独立命名图 prov:{tenant}:{action_id}
//     （经 graphstore.GraphStore.InsertTriples，词项转义与命名图校验复用其
//     唯一实现，本包不重复造轮子）。
//
// 相似决策检索依赖 summary 嵌入（vector(1024) + HNSW），走异步补算：嵌入失败
// 显式落 embed_status='FAILED'+embed_error，绝不阻塞动作记录主流程，也绝不静默。
//
// 分层约束：本包是领域包，禁止 import internal/api/**（tests/unit/arch_test.go
// 的"非传输层不得反向依赖 API 层"规则会抓反向依赖）。
package action

import (
	"strings"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// Action 类型枚举（action.actions.type 的 CHECK 白名单，迁移 000044）。
const (
	TypeQuery  = "QUERY"
	TypeUpdate = "UPDATE"
	TypeInfer  = "INFER"
	TypeAction = "ACTION"
	TypeTool   = "TOOL"
)

// Types 是 Action 类型的白名单（供校验与契约文档引用）。
var Types = []string{TypeQuery, TypeUpdate, TypeInfer, TypeAction, TypeTool}

// Outcome 枚举（action.actions.outcome 的 CHECK 白名单）。
const (
	OutcomePending  = "PENDING"
	OutcomeSuccess  = "SUCCESS"
	OutcomeDenied   = "DENIED"
	OutcomeRejected = "REJECTED"
	OutcomeError    = "ERROR"
)

// Outcomes 是 Outcome 的白名单。
var Outcomes = []string{OutcomePending, OutcomeSuccess, OutcomeDenied, OutcomeRejected, OutcomeError}

// ActorKind 枚举（action.actions.actor_kind 的 CHECK 白名单）。
const (
	ActorUser   = "USER"
	ActorAgent  = "AGENT"
	ActorA2A    = "A2A"
	ActorWorker = "WORKER"
)

// ActorKinds 是 ActorKind 的白名单。
var ActorKinds = []string{ActorUser, ActorAgent, ActorA2A, ActorWorker}

// embed_status 枚举（action.actions.embed_status 的 CHECK 白名单）。
//
// 四态全部显式：PENDING=待补算；SUCCEEDED=已落列；FAILED=补算失败（embed_error
// 必须带原因）；SKIPPED=未配置嵌入通道或无可嵌入文本。绝不出现"既没向量也没说法"的行。
const (
	EmbedPending   = "PENDING"
	EmbedSucceeded = "SUCCEEDED"
	EmbedFailed    = "FAILED"
	EmbedSkipped   = "SKIPPED"
)

// ValidType 报告 type 是否在白名单内。
func ValidType(s string) bool { return contains(Types, s) }

// ValidOutcome 报告 outcome 是否在白名单内。
func ValidOutcome(s string) bool { return contains(Outcomes, s) }

// ValidActorKind 报告 actor_kind 是否在白名单内。
func ValidActorKind(s string) bool { return contains(ActorKinds, s) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Input 是一次动作记录的输入（Recorder.Record 的唯一入参）。
//
// 字段语义与 action.actions 列一一对应；InputDigest/OutputDigest 允许调用方
// 传"已算好的 SHA-256 hex"或"原始内容"，Recorder 统一归一为 SHA-256 hex
// （见 recorder.go 的 normalizeDigest）。
type Input struct {
	TenantID ids.ID
	// RunID 是关联的编排 Run（可为零值：不挂在 Run 下的独立动作，如 A2A 调用）。
	RunID ids.ID
	Type  string
	// Title 必填（非空白），是动作列表与详情页的主标题。
	Title string
	// Summary 是动作的自然语言摘要，也是相似检索的嵌入文本（可空；为空则嵌入显式 SKIPPED）。
	Summary string
	// InputDigest / OutputDigest：输入/输出内容的 SHA-256 hex（或原始内容，Recorder 归一）。
	InputDigest  string
	OutputDigest string
	// Outcome 为空时默认 PENDING；非空必须是 Outcomes 成员。
	Outcome string
	// ActorKind 为空时默认 USER；非空必须是 ActorKinds 成员。
	ActorKind string
	// ActorName 是动作主体的可读名（用户名/智能体键/工具键等，可空）。
	ActorName string
	// OccurredAt 为零值时由 Recorder 补当前时间。
	OccurredAt time.Time
}

// Validate 校验 Input 的契约约束（Recorder.Record 的第一步）。
//
// 校验口径与 action.actions 的 CHECK 约束一致：type/outcome/actor_kind 走枚举
// 白名单（outcome/actor_kind 允许空串 → 由 Recorder 落库前补默认值，与列 DEFAULT
// 对齐），title 非空白。tenant_id 必须显式提供——RLS 之下没有租户身份的写入
// 必然失败，早失败优于让数据库报一条难懂的 RLS 错误。
func (in Input) Validate() error {
	if in.TenantID.IsZero() {
		return errors.E(errors.CodeValidationFailed, "action: tenant_id 不能为空")
	}
	if !ValidType(in.Type) {
		return errors.Ef(errors.CodeValidationFailed,
			"action: type 必须是 %s 之一，得到 %q", strings.Join(Types, "/"), in.Type)
	}
	if strings.TrimSpace(in.Title) == "" {
		return errors.E(errors.CodeValidationFailed, "action: title 不能为空")
	}
	if in.Outcome != "" && !ValidOutcome(in.Outcome) {
		return errors.Ef(errors.CodeValidationFailed,
			"action: outcome 必须是 %s 之一，得到 %q", strings.Join(Outcomes, "/"), in.Outcome)
	}
	if in.ActorKind != "" && !ValidActorKind(in.ActorKind) {
		return errors.Ef(errors.CodeValidationFailed,
			"action: actor_kind 必须是 %s 之一，得到 %q", strings.Join(ActorKinds, "/"), in.ActorKind)
	}
	return nil
}
