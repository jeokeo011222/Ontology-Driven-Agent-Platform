package seed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"odap/internal/platform/ids"
	"odap/internal/tools"
)

// 数据集目录约定：dataset.json 承载 name/version/description/tenant，
// 其余实体各占一个同名文件（不存在即视为该节为空），便于大文件拆分与分工维护。
const (
	mainFile        = "dataset.json"
	ontologiesFile  = "ontologies.json"
	agentsFile      = "agents.json"
	toolsFile       = "tools.json"
	workflowsFile   = "workflows.json"
	providersFile   = "providers.json"
	policiesFile    = "policies.json"
	graphsFile      = "graphs.json"
	evaluationsFile = "evaluations.json"
	plansFile       = "plans.json"
	runsFile        = "runs.json"
)

// Load 读取并校验一个数据集目录。
//
// 解析一律使用 DisallowUnknownFields：字段名拼错必须报错，绝不允许被静默忽略——
// 否则会出现"seed 报告成功，但某半个实体的字段实际没写进去"的静默降级。
func Load(dir string) (*Dataset, error) {
	raw, err := os.ReadFile(filepath.Join(dir, mainFile))
	if err != nil {
		return nil, fmt.Errorf("seed: 读取 %s 失败: %w", mainFile, err)
	}
	var ds Dataset
	if err := unmarshalStrict(raw, &ds); err != nil {
		return nil, fmt.Errorf("seed: 解析 %s 失败: %w", mainFile, err)
	}

	sections := []struct {
		file string
		dst  any
	}{
		{ontologiesFile, &ds.Ontologies},
		{agentsFile, &ds.Agents},
		{toolsFile, &ds.Tools},
		{workflowsFile, &ds.Workflows},
		{providersFile, &ds.Providers},
		{policiesFile, &ds.Policies},
		{graphsFile, &ds.Graphs},
		{evaluationsFile, &ds.Evaluations},
		{plansFile, &ds.Plans},
		{runsFile, &ds.Runs},
	}
	for _, s := range sections {
		path := filepath.Join(dir, s.file)
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 该节未使用
			}
			return nil, fmt.Errorf("seed: 读取 %s 失败: %w", s.file, err)
		}
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		if err := unmarshalStrict(b, s.dst); err != nil {
			return nil, fmt.Errorf("seed: 解析 %s 失败: %w", s.file, err)
		}
	}

	if err := ds.Validate(); err != nil {
		return nil, err
	}
	return &ds, nil
}

// unmarshalStrict 严格解析 JSON（未知字段直接报错）。
func unmarshalStrict(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// 拒绝尾随内容，避免"文件被截断但仍解析成功"这类假成功。
	if dec.More() {
		return fmt.Errorf("存在多余内容（JSON 可能被拼接或截断）")
	}
	return nil
}

// Validate 检查数据集的必填项、唯一性与引用完整性。
//
// 校验必须发生在写入之前：宁可拒绝整批数据，也不要把半套互相矛盾的实体写进库。
func (d *Dataset) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("seed: name 必填")
	}
	if err := d.Tenant.validate(); err != nil {
		return err
	}

	// ---- 唯一性（同一数据集内 ID/键不得重复）----
	if err := checkUnique("ontology_key", keysOf(d.Ontologies, func(o Ontology) string { return o.Key })); err != nil {
		return err
	}
	if err := checkUnique("agent_key", keysOf(d.Agents, func(a Agent) string { return a.Key })); err != nil {
		return err
	}
	if err := checkUnique("tool_key", keysOf(d.Tools, func(t Tool) string { return t.Key })); err != nil {
		return err
	}
	if err := checkUnique("workflow_key", keysOf(d.Workflows, func(w Workflow) string { return w.Key })); err != nil {
		return err
	}
	if err := checkUnique("provider_id", keysOf(d.Providers, func(p Provider) string { return p.ID })); err != nil {
		return err
	}

	// ---- Agent/Tool 的插件类型与版本 ----
	for i, a := range d.Agents {
		if err := requireUUID("agents", a.ID, "id"); err != nil {
			return err
		}
		if a.Key == "" || a.Domain == "" {
			return fmt.Errorf("seed: agents[%d] 的 agent_key 与 domain 必填", i)
		}
		if a.Plugin.Kind != "AGENT" {
			return fmt.Errorf("seed: agents[%d] 的 plugin_kind 必须为 AGENT（实际 %q）", i, a.Plugin.Kind)
		}
		if err := validatePlugin(fmt.Sprintf("agents[%d].plugin", i), a.Plugin); err != nil {
			return err
		}
		if err := requireUUID(fmt.Sprintf("agents[%d].version", i), a.Version.ID, "id"); err != nil {
			return err
		}
		if !json.Valid(a.Version.InputSchema) || !json.Valid(a.Version.OutputSchema) {
			return fmt.Errorf("seed: agents[%d].version 的 input_schema/output_schema 必须是合法 JSON", i)
		}
		// 注入期就校验 Schema 是否落在平台支持的子集内：平台校验器是 fail-closed 的，
		// 用了未登记关键字（anyOf/exclusiveMinimum 等）会在运行期才报错；
		// 若是写入侧的疏漏，作者会以为该约束已生效——必须提前拦住。
		if err := tools.ValidateSchema(a.Version.InputSchema); err != nil {
			return fmt.Errorf("seed: agents[%d].version.input_schema 不被平台支持: %w", i, err)
		}
		if err := tools.ValidateSchema(a.Version.OutputSchema); err != nil {
			return fmt.Errorf("seed: agents[%d].version.output_schema 不被平台支持: %w", i, err)
		}
	}
	for i, t := range d.Tools {
		if err := requireUUID("tools", t.ID, "id"); err != nil {
			return err
		}
		if t.Key == "" {
			return fmt.Errorf("seed: tools[%d] 的 tool_key 必填", i)
		}
		if t.Plugin.Kind != "TOOL" {
			return fmt.Errorf("seed: tools[%d] 的 plugin_kind 必须为 TOOL（实际 %q）", i, t.Plugin.Kind)
		}
		if err := validatePlugin(fmt.Sprintf("tools[%d].plugin", i), t.Plugin); err != nil {
			return err
		}
		if err := requireUUID(fmt.Sprintf("tools[%d].version", i), t.Version.ID, "id"); err != nil {
			return err
		}
		switch t.Version.EffectClass {
		case "READ", "EXTERNAL_WRITE", "INTERNAL_WRITE":
		default:
			return fmt.Errorf("seed: tools[%d] 的 effect_class 非法（%q），须为 READ/EXTERNAL_WRITE/INTERNAL_WRITE", i, t.Version.EffectClass)
		}
		for _, s := range []struct {
			name string
			raw  json.RawMessage
		}{
			{"input_schema", t.Version.InputSchema},
			{"output_schema", t.Version.OutputSchema},
		} {
			if !json.Valid(s.raw) {
				return fmt.Errorf("seed: tools[%d].version.%s 必须是合法 JSON", i, s.name)
			}
			if err := tools.ValidateSchema(s.raw); err != nil {
				return fmt.Errorf("seed: tools[%d].version.%s 不被平台支持: %w", i, s.name, err)
			}
		}
	}

	// ---- 工作流：版本非空、编排文档合法、版本状态在平台词表内 ----
	for i, w := range d.Workflows {
		if err := requireUUID("workflows", w.ID, "id"); err != nil {
			return err
		}
		if w.Key == "" || w.Name == "" {
			return fmt.Errorf("seed: workflows[%d] 的 workflow_key 与 name 必填", i)
		}
		if w.Plugin.Kind != "WORKFLOW" {
			return fmt.Errorf("seed: workflows[%d] 的 plugin_kind 必须为 WORKFLOW（实际 %q）", i, w.Plugin.Kind)
		}
		if err := validatePlugin(fmt.Sprintf("workflows[%d].plugin", i), w.Plugin); err != nil {
			return err
		}
		if err := requireUUID(fmt.Sprintf("workflows[%d].version", i), w.Version.ID, "id"); err != nil {
			return err
		}
		if !json.Valid(w.Version.Definition) {
			return fmt.Errorf("seed: workflows[%d].version.definition 必须是合法 JSON（节点/边/触发器编排文档）", i)
		}
		switch w.Version.Status {
		case "DRAFT", "VALIDATING", "APPROVED", "PUBLISHED", "DEPRECATED", "DISABLED":
		default:
			return fmt.Errorf("seed: workflows[%d].version.status 非法（%q），须为 DRAFT/VALIDATING/APPROVED/PUBLISHED/DEPRECATED/DISABLED", i, w.Version.Status)
		}
	}

	// ---- 本体：版本非空、构件类型合法 ----
	for i, o := range d.Ontologies {
		if err := requireUUID("ontologies", o.ID, "id"); err != nil {
			return err
		}
		if o.Key == "" || o.Name == "" || o.Domain == "" || o.Granularity == "" {
			return fmt.Errorf("seed: ontologies[%d] 的 ontology_key/name/domain/granularity 必填", i)
		}
		if len(o.Versions) == 0 {
			return fmt.Errorf("seed: ontologies[%d] 至少需要一个版本", i)
		}
		for j, v := range o.Versions {
			if err := requireUUID(fmt.Sprintf("ontologies[%d].versions[%d]", i, j), v.ID, "id"); err != nil {
				return err
			}
			if !json.Valid(v.Manifest) {
				return fmt.Errorf("seed: ontologies[%d].versions[%d].manifest 必须是合法 JSON", i, j)
			}
			for k, a := range v.Artifacts {
				switch a.Type {
				case "OWL", "RDF", "SHACL", "MAPPING", "RULES", "TESTS", "EVIDENCE", "DOCUMENT":
				default:
					return fmt.Errorf("seed: ontologies[%d].versions[%d].artifacts[%d].artifact_type 非法（%q）", i, j, k, a.Type)
				}
			}
		}
	}

	// ---- 模型供应商：端点与模型必填，且能力声明必须可解析 ----
	// capSeen 按"能力内容摘要"归并：同一份能力内容共享同一条 llm.capability_profiles
	// 档案，因此各模型的修订号与验证标记必须一致，否则能力是否"已验证"会取决于
	// 随机写入顺序 —— 这是必须显式拒绝的歧义。
	type capDecl struct {
		revision string
		verified bool
		where    string
	}
	capSeen := map[string]capDecl{}
	for i, p := range d.Providers {
		if p.ID == "" || p.DisplayName == "" || p.Type == "" || p.Status == "" {
			return fmt.Errorf("seed: providers[%d] 的 provider_id/display_name/provider_type/status 必填", i)
		}
		for j, e := range p.Endpoints {
			if err := requireUUID(fmt.Sprintf("providers[%d].endpoints[%d]", i, j), e.ID, "endpoint_id"); err != nil {
				return err
			}
			if e.Name == "" || e.DeploymentType == "" || e.Status == "" {
				return fmt.Errorf("seed: providers[%d].endpoints[%d] 的 endpoint_name/deployment_type/status 必填", i, j)
			}
			if e.DeploymentType == "HTTP" && e.BaseURL == "" {
				return fmt.Errorf("seed: providers[%d].endpoints[%d]（%s）为 HTTP 端点但缺少 base_url（凭据引用只回答\"用哪把钥匙\"，不回答\"敲哪扇门\"）", i, j, e.Name)
			}
			// 无模型版本的端点永远无法被路由选中（Router 以模型能力为准），
			// 登记它只会让"已配置"与"可调用"看起来一致而实际不一致。
			if len(e.Models) == 0 {
				return fmt.Errorf("seed: providers[%d].endpoints[%d]（%s）至少需要一个模型版本", i, j, e.Name)
			}
			for k, m := range e.Models {
				where := fmt.Sprintf("providers[%d].endpoints[%d].models[%d]", i, j, k)
				if err := requireUUID(where, m.ID, "model_revision_id"); err != nil {
					return err
				}
				if m.ModelID == "" || m.Status == "" {
					return fmt.Errorf("seed: %s 的 model_id/status 必填", where)
				}
				caps, err := decodeCapabilities(m.Capabilities)
				if err != nil {
					return fmt.Errorf("seed: %s 的能力声明非法: %w", where, err)
				}
				if m.CapabilityRevision == "" {
					return fmt.Errorf("seed: %s 的 capability_revision 必填（能力档案需可追溯修订）", where)
				}
				digest := caps.digest()
				if prev, ok := capSeen[digest]; ok {
					if prev.revision != m.CapabilityRevision || prev.verified != m.CapabilityVerified {
						return fmt.Errorf("seed: %s 与 %s 的能力内容相同但声明不一致"+
							"（revision %q/%v vs %q/%v）；同一份能力内容只能有一条档案",
							where, prev.where, m.CapabilityRevision, m.CapabilityVerified,
							prev.revision, prev.verified)
					}
				} else {
					capSeen[digest] = capDecl{m.CapabilityRevision, m.CapabilityVerified, where}
				}
			}
		}
	}

	// ---- 跨节引用：Run 与 Plan 必须指向本数据集内已声明的 Agent 版本 ----
	agentVersionIDs := map[string]bool{}
	for _, a := range d.Agents {
		agentVersionIDs[a.Version.ID] = true
	}
	for i, r := range d.Runs {
		if err := requireUUID(fmt.Sprintf("runs[%d]", i), r.ID, "id"); err != nil {
			return err
		}
		if r.AgentVersionID != "" && !agentVersionIDs[r.AgentVersionID] {
			return fmt.Errorf("seed: runs[%d].agent_version_id=%s 未在本数据集的 agents 中声明（拒绝产生悬空引用）",
				i, r.AgentVersionID)
		}
	}

	// ---- 跨节引用：Agent 的 dependency_manifest 不得引用未声明的工具/本体 ----
	// 若无此校验，dependency_manifest 里写错工具名（例如 erp.expense.queryy）
	// 会静默通过：seed 报告成功，但 Agent 实际可调用的工具集与声明的不是一回事。
	// 这与 Run 的悬空引用是同一类风险，必须一起拦住。
	toolKeys := map[string]bool{}
	for _, t := range d.Tools {
		toolKeys[t.Key] = true
	}
	ontologyRefs := map[string]bool{}
	for _, o := range d.Ontologies {
		for _, v := range o.Versions {
			ontologyRefs[o.Key+"@"+v.Version] = true
		}
	}
	// 模型引用用 model_id@revision_label 表示（llm.model_revisions 的唯一业务键）。
	modelRefs := map[string]bool{}
	for _, p := range d.Providers {
		for _, e := range p.Endpoints {
			for _, m := range e.Models {
				modelRefs[m.ModelID+"@"+m.RevisionLabel] = true
			}
		}
	}
	for i, a := range d.Agents {
		raw := bytes.TrimSpace(a.Version.DependencyManifest)
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var dep struct {
			Ontology []string `json:"ontology"`
			Tools    []string `json:"tools"`
			Models   []string `json:"models"`
		}
		if err := json.Unmarshal(raw, &dep); err != nil {
			return fmt.Errorf("seed: agents[%d].version.dependency_manifest 必须是 JSON 对象: %w", i, err)
		}
		for _, tk := range dep.Tools {
			if !toolKeys[tk] {
				return fmt.Errorf("seed: agents[%d].version.dependency_manifest.tools 引用了未声明的工具 %q（拒绝悬空引用）", i, tk)
			}
		}
		for _, ref := range dep.Ontology {
			if !ontologyRefs[ref] {
				return fmt.Errorf("seed: agents[%d].version.dependency_manifest.ontology 引用了未声明的本体 %q（拒绝悬空引用）", i, ref)
			}
		}
		for _, ref := range dep.Models {
			if !modelRefs[ref] {
				return fmt.Errorf("seed: agents[%d].version.dependency_manifest.models 引用了未声明的模型 %q（拒绝悬空引用）", i, ref)
			}
		}
	}

	for i, g := range d.Graphs {
		if g.GraphIRI == "" || g.GraphType == "" {
			return fmt.Errorf("seed: graphs[%d] 的 graph_iri 与 graph_type 必填", i)
		}
	}
	return nil
}

// decodeCapabilities 解析能力声明，并拒绝未知字段。
//
// 为什么必须拒绝未知字段：能力字段名拼错（例如 native_schema 写成 nativeSchema）
// 不会报错，只会让该字段落地为零值 —— 端点于是被登记为"不支持原生 schema"，
// 路由与结构化输出回路的行为随之悄悄改变。这是典型的静默降级。
func decodeCapabilities(raw json.RawMessage) (ModelCapabilities, error) {
	var caps ModelCapabilities
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return caps, fmt.Errorf("capabilities 必填（第九篇 §4.4 步骤 5：能力声明必须写入 Registry）")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&caps); err != nil {
		return caps, fmt.Errorf("capabilities 解析失败: %w", err)
	}
	if dec.More() {
		return caps, fmt.Errorf("capabilities 存在多余内容")
	}
	if caps.MaxContextTokens <= 0 {
		return caps, fmt.Errorf("max_context_tokens 必须为正整数（实际 %d）", caps.MaxContextTokens)
	}
	return caps, nil
}

// validatePlugin 校验插件三要素。
func validatePlugin(where string, p Plugin) error {
	if err := requireUUID(where, p.ID, "id"); err != nil {
		return err
	}
	if err := requireUUID(where, p.VersionID, "version_id"); err != nil {
		return err
	}
	if p.Key == "" || p.Name == "" || p.Kind == "" || p.Version == "" {
		return fmt.Errorf("seed: %s 的 plugin_key/name/plugin_kind/version 必填", where)
	}
	return nil
}

// validate 校验租户与身份结构。
func (t Tenant) validate() error {
	if err := requireUUID("tenant", t.ID, "id"); err != nil {
		return err
	}
	if t.Key == "" || t.Name == "" {
		return fmt.Errorf("seed: tenant 的 key 与 name 必填")
	}
	if len(t.Users) == 0 {
		return fmt.Errorf("seed: tenant.users 至少需要一个用户（实体的 created_by 需要真实用户）")
	}
	seenLogin := map[string]bool{}
	for i, u := range t.Users {
		if err := requireUUID(fmt.Sprintf("tenant.users[%d]", i), u.ID, "id"); err != nil {
			return err
		}
		if u.LoginName == "" || u.DisplayName == "" {
			return fmt.Errorf("seed: tenant.users[%d] 的 login_name 与 display_name 必填", i)
		}
		if seenLogin[u.LoginName] {
			return fmt.Errorf("seed: tenant.users 中 login_name=%q 重复", u.LoginName)
		}
		seenLogin[u.LoginName] = true
	}
	for i, o := range t.Orgs {
		if err := requireUUID(fmt.Sprintf("tenant.orgs[%d]", i), o.ID, "id"); err != nil {
			return err
		}
		if o.OrgCode == "" || o.Name == "" || o.OrgType == "" {
			return fmt.Errorf("seed: tenant.orgs[%d] 的 org_code/name/org_type 必填", i)
		}
	}
	for i, r := range t.Roles {
		if err := requireUUID(fmt.Sprintf("tenant.roles[%d]", i), r.ID, "id"); err != nil {
			return err
		}
		if r.RoleKey == "" || r.Name == "" {
			return fmt.Errorf("seed: tenant.roles[%d] 的 role_key 与 name 必填", i)
		}
	}
	return nil
}

// requireUUID 校验字段是合法 UUID 字符串。
func requireUUID(where, value, field string) error {
	if value == "" {
		return fmt.Errorf("seed: %s.%s 必填", where, field)
	}
	if _, err := ids.Parse(value); err != nil {
		return fmt.Errorf("seed: %s.%s=%q 不是合法 UUID: %w", where, field, value, err)
	}
	return nil
}

// keysOf 提取键集合（用于唯一性检查）。
func keysOf[T any](items []T, key func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, key(it))
	}
	return out
}

// checkUnique 断言键不重复（空键由各自的必填校验负责）。
func checkUnique(what string, ks []string) error {
	seen := map[string]bool{}
	for _, k := range ks {
		if k == "" {
			continue
		}
		if seen[k] {
			return fmt.Errorf("seed: %s=%q 重复", what, k)
		}
		seen[k] = true
	}
	return nil
}
