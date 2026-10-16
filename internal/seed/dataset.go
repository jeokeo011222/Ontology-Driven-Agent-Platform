// Package seed 实现"数据文件驱动"的演示数据注入。
//
// 设计原则（第十六篇 §8.4 `make seed` / §14.1「一套 Finance Ontology 与报销演示数据」）：
//
//   - 数据与代码分离：实体定义放在 datasets/<name>/*.json，本包只负责
//     加载、校验与写入。这样数据集可被独立审阅、替换与版本化，避免把
//     业务定义硬编码进 Go 代码。
//   - 幂等：所有写入使用固定 UUID + ON CONFLICT DO NOTHING，重复执行安全。
//   - 身份一致：数据集声明的 tenant/user 必须与 .env 的 ODAP_DEV_* 一致，
//     否则数据会落在开发身份读不到的租户下（见 cmd/api 的同名约束说明）。
//
// 权威来源：第二篇（DDL）、第五篇（本体）、第八篇（Agent/Tool）、第九篇（模型供应商）。
package seed

import (
	"encoding/json"

	"odap/internal/llm"
)

// Dataset 是一个数据集（datasets/<name>/dataset.json）的根结构。
type Dataset struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`

	Tenant      Tenant        `json:"tenant"`
	Ontologies  []Ontology    `json:"ontologies,omitempty"`
	Agents      []Agent       `json:"agents,omitempty"`
	Tools       []Tool        `json:"tools,omitempty"`
	Skills      []Skill       `json:"skills,omitempty"`
	Workflows   []Workflow    `json:"workflows,omitempty"`
	Providers   []Provider    `json:"providers,omitempty"`
	Policies    []Policy      `json:"policies,omitempty"`
	Graphs      []NamedGraph  `json:"graphs,omitempty"`
	Evaluations []Evaluation  `json:"evaluations,omitempty"`
	Plans       []Plan        `json:"plans,omitempty"`
	Runs        []Run         `json:"runs,omitempty"`
	Releases    []ReleaseSeed `json:"releases,omitempty"`
}

// ---------------------------------------------------------------------------
// 身份与治理基础（iam / governance）
// ---------------------------------------------------------------------------

// Tenant 是数据集的目标租户及其身份结构。
type Tenant struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Name  string `json:"name"`
	Users []User `json:"users"`
	Orgs  []Org  `json:"orgs"`

	Roles       []Role       `json:"roles,omitempty"`
	Memberships []Membership `json:"memberships,omitempty"`
	Bindings    []Binding    `json:"bindings,omitempty"`
}

// User 是平台用户（iam.users）。
type User struct {
	ID          string `json:"id"`
	LoginName   string `json:"login_name"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
}

// Org 是组织单元（iam.organizations）。
type Org struct {
	ID      string `json:"id"`
	OrgCode string `json:"org_code"`
	Name    string `json:"name"`
	OrgType string `json:"org_type"`
}

// Role 是租户内角色（governance.roles）及其权限授权。
type Role struct {
	ID          string   `json:"id"`
	RoleKey     string   `json:"role_key"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	SystemRole  bool     `json:"system_role"`
	Permissions []string `json:"permissions,omitempty"`
}

// Membership 是用户与组织的成员关系（iam.memberships）。
type Membership struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	OrgID  string `json:"org_id"`
}

// Binding 是角色绑定（governance.role_bindings）。
type Binding struct {
	ID           string `json:"id"`
	MembershipID string `json:"membership_id"`
	RoleID       string `json:"role_id"`
	ScopeType    string `json:"scope_type"`
	GrantedBy    string `json:"granted_by,omitempty"`
}

// ---------------------------------------------------------------------------
// 本体（ontology）
// ---------------------------------------------------------------------------

// Ontology 是本体的逻辑单元（ontology.ontologies）及其版本序列。
type Ontology struct {
	ID          string            `json:"id"`
	Key         string            `json:"ontology_key"`
	Name        string            `json:"name"`
	Domain      string            `json:"domain"`
	Granularity string            `json:"granularity"`
	Description string            `json:"description,omitempty"`
	OwnerOrgID  string            `json:"owner_org_id,omitempty"`
	Versions    []OntologyVersion `json:"versions"`
}

// OntologyVersion 是本体的一个版本（ontology.ontology_versions）。
type OntologyVersion struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Status         string          `json:"status"`
	Manifest       json.RawMessage `json:"manifest"`
	Artifacts      []Artifact      `json:"artifacts,omitempty"`
	Publication    *Publication    `json:"publication,omitempty"`
	ValidationRuns []ValidationRun `json:"validation_runs,omitempty"`
}

// Artifact 是本体构件（ontology.ontology_artifacts）。
// Content 非空时由 seed 就地计算 digest 与 size（避免手写摘要漂移）。
type Artifact struct {
	ID          string `json:"id"`
	Type        string `json:"artifact_type"`
	LogicalPath string `json:"logical_path"`
	MediaType   string `json:"media_type"`
	StorageURI  string `json:"storage_uri"`
	Content     string `json:"content,omitempty"`
}

// Publication 是发布记录（ontology.publications）。
type Publication struct {
	ID          string `json:"id"`
	Environment string `json:"environment"`
}

// ValidationRun 是本体校验记录（ontology.validation_runs）。
type ValidationRun struct {
	ID                 string `json:"id"`
	ValidatorVersion   string `json:"validator_version"`
	Status             string `json:"status"`
	SyntaxValid        bool   `json:"syntax_valid"`
	OWLConsistent      bool   `json:"owl_consistent"`
	SHACLConformant    bool   `json:"shacl_conformant"`
	BusinessRulesValid bool   `json:"business_rules_valid"`
}

// ---------------------------------------------------------------------------
// Registry（plugins / agents / tools）
// ---------------------------------------------------------------------------

// Plugin 是 Agent/Tool 的载体（registry.plugins + registry.plugin_versions）。
// 说明：MVP DDL 无 agent↔tool 绑定表，"某 Agent 可用哪些工具"由同一插件
// 或 agent_versions.dependency_manifest 表达（见 dependency_manifest）。
type Plugin struct {
	ID          string          `json:"id"`
	Key         string          `json:"plugin_key"`
	Kind        string          `json:"plugin_kind"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	VersionID   string          `json:"version_id"`
	Version     string          `json:"version"`
	Manifest    json.RawMessage `json:"manifest,omitempty"`
}

// Agent 是 Agent 定义（registry.agents）及其首个版本。
type Agent struct {
	ID          string `json:"id"`
	Key         string `json:"agent_key"`
	Domain      string `json:"domain"`
	Description string `json:"description,omitempty"`
	// Builtin 标记平台内置智能体：登记在 Agent 管理页但不可修改
	// （PATCH/DELETE/publish 一律 409）。用户自建 Agent 该字段缺省为 false。
	Builtin bool         `json:"is_builtin,omitempty"`
	Plugin  Plugin       `json:"plugin"`
	Version AgentVersion `json:"version"`
}

// AgentVersion 是 Agent 版本（registry.agent_versions）。
type AgentVersion struct {
	ID                 string          `json:"id"`
	Version            string          `json:"version"`
	Status             string          `json:"status"`
	InputSchema        json.RawMessage `json:"input_schema"`
	OutputSchema       json.RawMessage `json:"output_schema"`
	PromptTemplate     json.RawMessage `json:"prompt_template,omitempty"`
	Config             json.RawMessage `json:"config,omitempty"`
	DependencyManifest json.RawMessage `json:"dependency_manifest,omitempty"`
}

// Tool 是工具定义（registry.tools）及其首个版本。
type Tool struct {
	ID          string `json:"id"`
	Key         string `json:"tool_key"`
	Description string `json:"description,omitempty"`
	// Builtin 标记平台内置工具：登记在工具管理页但不可编辑/删除
	// （PATCH/DELETE 一律 409）。用户自建工具该字段缺省为 false。
	Builtin bool        `json:"is_builtin,omitempty"`
	Plugin  Plugin      `json:"plugin"`
	Version ToolVersion `json:"version"`
}

// ToolVersion 是工具版本（registry.tool_versions）。
type ToolVersion struct {
	ID              string          `json:"id"`
	Version         string          `json:"version"`
	Status          string          `json:"status"`
	InputSchema     json.RawMessage `json:"input_schema"`
	OutputSchema    json.RawMessage `json:"output_schema"`
	Capabilities    []string        `json:"capabilities,omitempty"`
	EffectClass     string          `json:"effect_class"`
	ExecutionConfig json.RawMessage `json:"execution_config,omitempty"`
}

// Skill 是技能定义（registry.skills）及其首个版本。
// 技能是"提示词片段 + 工具绑定"的可复用能力包，运行期经
// dependency_manifest.skills → agent.CompileSkills 展开进系统提示词。
type Skill struct {
	ID          string `json:"id"`
	SkillKey    string `json:"skill_key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Domain      string `json:"domain,omitempty"`
	// Builtin 标记平台内置技能：不可编辑/删除（PATCH/DELETE 一律 409）。
	Builtin bool         `json:"is_builtin,omitempty"`
	Version SkillVersion `json:"version"`
}

// SkillVersion 是技能版本（registry.skill_versions）。
type SkillVersion struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Status         string          `json:"status"`
	PromptFragment string          `json:"prompt_fragment"`
	ToolBindings   []string        `json:"tool_bindings,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
}

// Workflow 是工作流定义（registry.workflows）及其首个版本。
type Workflow struct {
	ID          string `json:"id"`
	Key         string `json:"workflow_key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Builtin 标记平台内置工作流：登记在工作流与 Harness 页但不可编辑、
	// 发布或删除（PATCH/DELETE/publish 一律 409）。用户自建该字段缺省为 false。
	Builtin bool            `json:"is_builtin,omitempty"`
	Plugin  Plugin          `json:"plugin"`
	Version WorkflowVersion `json:"version"`
}

// WorkflowVersion 是工作流版本（registry.workflow_versions）。
// definition 是编排文档（节点/边/触发器），结构由 Harness 解释。
type WorkflowVersion struct {
	ID           string          `json:"id"`
	Version      string          `json:"version"`
	Status       string          `json:"status"`
	Definition   json.RawMessage `json:"definition"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// ---------------------------------------------------------------------------
// 模型供应商（llm，平台级：无 tenant_id、无 RLS）
// ---------------------------------------------------------------------------

// Provider 是模型供应商（llm.providers）。
type Provider struct {
	ID          string     `json:"provider_id"`
	DisplayName string     `json:"display_name"`
	Type        string     `json:"provider_type"`
	Status      string     `json:"status"`
	Endpoints   []Endpoint `json:"endpoints,omitempty"`
}

// Endpoint 是供应商端点（llm.endpoints）。
type Endpoint struct {
	ID             string `json:"endpoint_id"`
	Name           string `json:"endpoint_name"`
	DeploymentType string `json:"deployment_type"`
	RegionCode     string `json:"region_code,omitempty"`
	CredentialRef  string `json:"credential_ref,omitempty"`
	// BaseURL 仅 HTTP 类端点使用（deployment_type=MOCK 的进程内端点不使用）。
	BaseURL string          `json:"base_url,omitempty"`
	Status  string          `json:"status"`
	Models  []ModelRevision `json:"models,omitempty"`
}

// ModelRevision 是模型版本（llm.model_revisions）。
//
// 注意：这里**故意没有** capability_digest 字段。原因（第九篇 §4.4 步骤 5）：
// 能力声明必须写入 Registry，而哈希只能回答"能力是否变了"、不能回答"能力是什么"。
// 摘要由 seed 从 Capabilities 规范化计算，手工填写的哈希无法校验、只会与内容漂移，
// 因此数据文件里写 capability_digest 会被 DisallowUnknownFields 直接拒绝。
type ModelRevision struct {
	ID               string          `json:"model_revision_id"`
	ModelID          string          `json:"model_id"`
	RevisionLabel    string          `json:"revision_label,omitempty"`
	PricingProfileID string          `json:"pricing_profile_id,omitempty"`
	Status           string          `json:"status"`
	Capabilities     json.RawMessage `json:"capabilities"`

	// CapabilityRevision / CapabilityVerified 描述这条能力声明的修订与验证状态，
	// 落到 llm.capability_profiles（按能力内容寻址，见该表注释）。
	// 同一份能力内容在多个模型版本间共享一条档案，因此两处声明必须一致，
	// 否则 Validate 会报错（而不是让"先写者胜"决定能力是否被视为已验证）。
	CapabilityRevision string `json:"capability_revision"`
	CapabilityVerified bool   `json:"capability_verified"`
}

// ModelCapabilities 是能力声明的规范化结构，字段与 llm.ProviderCapabilities 一一对应。
//
// 用结构体而不是 map：结构体的 JSON 序列化字段顺序固定，
// 因此 SHA-256 摘要稳定可比 —— map 序列化顺序虽也被 Go 规范化，
// 但结构体额外提供了"字段缺失即为零值"的强类型保证，避免拼错字段名后被静默忽略。
type ModelCapabilities struct {
	NativeSchema     bool `json:"native_schema"`
	SupportsSeed     bool `json:"supports_seed"`
	SupportsTools    bool `json:"supports_tools"`
	MaxContextTokens int  `json:"max_context_tokens"`
}

// digest 返回能力声明的规范化 SHA-256（capability_digest 的唯一计算来源）。
//
// 委托给 llm.CapabilityDigest：界面「模型版本登记」路径也用同一个函数，
// 因此同一份能力在 seed 与界面两条写入路径下必然落到同一条档案，
// 不会因两份 Marshal 逻辑漂移而分裂。
func (c ModelCapabilities) digest() string {
	return llm.CapabilityDigest(llm.ProviderCapabilities{
		NativeSchema:     c.NativeSchema,
		SupportsSeed:     c.SupportsSeed,
		SupportsTools:    c.SupportsTools,
		MaxContextTokens: c.MaxContextTokens,
	})
}

// ---------------------------------------------------------------------------
// 策略 / 图 / 评估 / 计划 / 运行
// ---------------------------------------------------------------------------

// Policy 是授权策略（governance.policies）及其版本。
type Policy struct {
	ID          string          `json:"id"`
	Key         string          `json:"policy_key"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Versions    []PolicyVersion `json:"versions,omitempty"`
}

// PolicyVersion 是策略版本（governance.policy_versions）。
type PolicyVersion struct {
	ID       string          `json:"id"`
	Version  string          `json:"version"`
	Language string          `json:"policy_language"`
	Status   string          `json:"status"`
	Content  json.RawMessage `json:"content"`
}

// NamedGraph 是命名图（graph.named_graphs）及其三元组。
type NamedGraph struct {
	GraphIRI        string   `json:"graph_iri"`
	GraphType       string   `json:"graph_type"`
	OntologyVersion string   `json:"ontology_version,omitempty"`
	Triples         []Triple `json:"triples,omitempty"`
}

// Triple 是一条 RDF 三元组。Kind: 1=IRI 2=BlankNode 3=Literal。
type Triple struct {
	Subject        string `json:"subject"`
	SubjectKind    int    `json:"subject_kind,omitempty"`
	Predicate      string `json:"predicate"`
	Object         string `json:"object"`
	ObjectKind     int    `json:"object_kind,omitempty"`
	ObjectDatatype string `json:"object_datatype,omitempty"`
	ObjectLanguage string `json:"object_language,omitempty"`
	FactKind       string `json:"fact_kind"`
}

// Evaluation 是评估数据集（evaluation.datasets）及其用例。
type Evaluation struct {
	ID          string     `json:"id"`
	Key         string     `json:"dataset_key"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	RevisionID  string     `json:"revision_id"`
	Cases       []EvalCase `json:"cases,omitempty"`
}

// EvalCase 是评估用例（evaluation.cases）。
type EvalCase struct {
	ID       string          `json:"id"`
	CaseKey  string          `json:"case_key"`
	Input    json.RawMessage `json:"input_payload,omitempty"`
	Expected json.RawMessage `json:"expected_payload,omitempty"`
}

// Plan 是一个计划（planning.plans）及其修订。
type Plan struct {
	ID       string       `json:"plan_id"`
	IntentID string       `json:"intent_id"`
	Domain   string       `json:"domain"`
	Status   string       `json:"status"`
	Version  int          `json:"version"`
	Revision PlanRevision `json:"revision"`
}

// PlanRevision 是计划修订（planning.plan_revisions）。
type PlanRevision struct {
	ID               string          `json:"plan_revision_id"`
	RevisionNumber   int             `json:"revision_number"`
	PlanIR           json.RawMessage `json:"plan_ir"`
	ValidationStatus string          `json:"validation_status"`
	PolicyVersion    string          `json:"policy_version,omitempty"`
}

// Run 是一条演示运行（runtime.agent_runs）。
type Run struct {
	ID             string          `json:"id"`
	RequestedBy    string          `json:"requested_by"`
	AgentVersionID string          `json:"agent_version_id"`
	Status         string          `json:"status"`
	InputPayload   json.RawMessage `json:"input_payload"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	Priority       int             `json:"priority,omitempty"`
	MaxAttempts    int             `json:"max_attempts,omitempty"`
}

// ReleaseSeed 是发布实体（release_mgmt.releases，迁移 000040）。
// artifact_id 引用本数据集内已声明的插件/本体 id（拒绝悬空引用，由 Validate 校验）。
type ReleaseSeed struct {
	ID           string `json:"id"`
	ArtifactType string `json:"artifact_type"`
	ArtifactID   string `json:"artifact_id"`
	Version      string `json:"version"`
	State        string `json:"state"`
	GateState    string `json:"gate_state"`
	Environment  string `json:"environment,omitempty"`
}
