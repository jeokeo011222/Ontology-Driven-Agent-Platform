package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// 公共：生成请求的语料装配
// ---------------------------------------------------------------------------

// genContext 汇总一次资产生成的输入语料。
type genContext struct {
	Instructions string
	SourcePath   string
	Corpus       *docCorpus
}

// buildUserPrompt 组装生成提示词：自然语言指令 + 可选制度文档正文。
func (g genContext) buildUserPrompt() string {
	var b strings.Builder
	if g.Instructions != "" {
		b.WriteString("需求说明：\n")
		b.WriteString(g.Instructions)
		b.WriteString("\n\n")
	}
	if g.Corpus != nil {
		b.WriteString("参考制度文档：\n")
		b.WriteString(g.Corpus.Text)
	}
	return b.String()
}

// prepareGenContext 解析 instructions/source_path 并读取语料。
func (h *Handler) prepareGenContext(instructions, sourcePath string, maxChars int) (genContext, error) {
	g := genContext{Instructions: strings.TrimSpace(instructions), SourcePath: strings.TrimSpace(sourcePath)}
	if g.Instructions == "" && g.SourcePath == "" {
		return g, errors.E(errors.CodeInvalidArgument, "需提供 instructions（自然语言需求）或 source_path（制度文档路径）")
	}
	if g.SourcePath != "" {
		corpus, err := h.readCorpus(g.SourcePath, maxChars)
		if err != nil {
			return g, err
		}
		g.Corpus = corpus
	}
	return g, nil
}

// generationEnvelope 构造生成结果的公共元数据（来源与模型用量）。
func generationEnvelope(g genContext, comp *llm.Completion) map[string]any {
	env := map[string]any{"model": completionMeta(comp)}
	if g.SourcePath != "" {
		env["source_path"] = g.SourcePath
	}
	if g.Corpus != nil {
		env["source_files"] = g.Corpus.Files
		env["source_chars"] = g.Corpus.Chars
		env["truncated"] = g.Corpus.Truncated
		env["warnings"] = g.Corpus.Warnings
	}
	return env
}

// requireString 读取并校验非空字符串字段。
func requireString(obj map[string]any, key string) (string, error) {
	v, _ := obj[key].(string)
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.Ef(errors.CodeValidationFailed, "模型输出缺少必填字段 %s", key).
			WithDetails(map[string]any{"field": key})
	}
	return v, nil
}

// requireObject 读取并校验对象字段，返回其 JSON 编码。
func requireObject(obj map[string]any, key string) ([]byte, error) {
	v, ok := obj[key].(map[string]any)
	if !ok || len(v) == 0 {
		return nil, errors.Ef(errors.CodeValidationFailed, "模型输出缺少对象字段 %s 或为空", key).
			WithDetails(map[string]any{"field": key})
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "序列化字段失败: "+key)
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Agent 自动生成
// ---------------------------------------------------------------------------

const agentGenSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["agent_key", "domain", "description", "input_schema", "output_schema", "prompt"],
  "properties": {
    "agent_key": {"type": "string"},
    "domain": {"type": "string"},
    "description": {"type": "string"},
    "input_schema": {"type": "object", "additionalProperties": true},
    "output_schema": {"type": "object", "additionalProperties": true},
    "prompt": {
      "type": "object", "additionalProperties": false,
      "required": ["system", "user"],
      "properties": {"system": {"type": "string"}, "user": {"type": "string"}}
    },
    "tools": {"type": "array", "items": {"type": "string"}},
    "temperature": {"type": "number"},
    "max_output_tokens": {"type": "integer"}
  }
}`

const agentSystemPrompt = `你是企业级 Agent 设计专家。请根据用户需求（与可选制度文档）设计一个可执行的 Agent 定义。
要求：
1. agent_key 用英文小写短横线命名；domain 是业务域（如 finance/contract/hr）；
2. input_schema / output_schema 用 JSON Schema 描述，字段名用英文，description 用中文；
3. prompt.system 给出 Agent 的角色、边界与必须遵守的规则；prompt.user 用 {{占位符}} 引用输入字段；
4. tools 只列出真正需要的工具 key（可为空数组）；
5. 严格输出符合给定 JSON Schema 的单个 JSON 对象，不要输出任何解释性文字。`

// GenerateAgentRequest 是 POST /api/v1/agents/generate 的请求体。
type GenerateAgentRequest struct {
	Instructions   string `json:"instructions,omitempty"`
	SourcePath     string `json:"source_path,omitempty"`
	AgentKey       string `json:"agent_key,omitempty"`
	Domain         string `json:"domain,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	MaxSourceChars int    `json:"max_source_chars,omitempty"`
}

// GenerateAgent 处理 POST /api/v1/agents/generate。
func (h *Handler) GenerateAgent(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	if _, err := h.llmGateway(); err != nil {
		h.fail(w, r, err)
		return
	}
	var req GenerateAgentRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	g, err := h.prepareGenContext(req.Instructions, req.SourcePath, req.MaxSourceChars)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	user := g.buildUserPrompt()
	if req.AgentKey != "" {
		user += "\n\n请使用 agent_key = " + strings.TrimSpace(req.AgentKey)
	}
	if req.Domain != "" {
		user += "，domain = " + strings.TrimSpace(req.Domain)
	}
	obj, comp, err := h.llmJSON(r.Context(), id.TenantID, req.ModelID,
		agentSystemPrompt, user, []byte(agentGenSchema), idempotencyKey(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	agentKey, err := requireString(obj, "agent_key")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	domain, err := requireString(obj, "domain")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	description, _ := obj["description"].(string)
	inSchema, err := requireObject(obj, "input_schema")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	outSchema, err := requireObject(obj, "output_schema")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	prompt, err := requireObject(obj, "prompt")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var promptObj map[string]any
	_ = json.Unmarshal(prompt, &promptObj)
	if s, _ := promptObj["system"].(string); strings.TrimSpace(s) == "" {
		h.fail(w, r, errors.E(errors.CodeValidationFailed, "模型输出的 prompt.system 为空"))
		return
	}

	// 运行配置：温度与最大输出令牌（可选）。
	cfg := map[string]any{}
	if t, ok := obj["temperature"].(float64); ok {
		cfg["temperature"] = t
	}
	if m, ok := obj["max_output_tokens"].(float64); ok && m > 0 {
		cfg["max_output_tokens"] = int(m)
	}
	cfgJSON, _ := json.Marshal(cfg)

	// 依赖清单：模型给出的工具 key 列表。
	var depJSON []byte
	if tools, ok := obj["tools"].([]any); ok {
		depJSON, _ = json.Marshal(map[string]any{"tools": tools})
	}

	out, err := h.deps.Store.CreateAgent(r.Context(), store.CreateAgentInput{
		AgentKey:           agentKey,
		Domain:             domain,
		Description:        description,
		InputSchema:        inSchema,
		OutputSchema:       outSchema,
		PromptTemplate:     prompt,
		ConfigJSON:         cfgJSON,
		DependencyManifest: depJSON,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "AgentGenerated", "agent.generate", "SUCCESS", "agent",
		stringify(out["id"]), ids.ID{}, map[string]any{
			"agent_key": agentKey, "source_path": req.SourcePath,
			"model": modelRevisionOf(comp), "idempotency_key": idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "AgentGenerated", sse.Important,
		map[string]any{"agent_id": stringify(out["id"]), "agent_key": agentKey})
	out["generation"] = generationEnvelope(g, comp)
	h.writeJSON(w, http.StatusCreated, out)
}

// ---------------------------------------------------------------------------
// 工具自动生成
// ---------------------------------------------------------------------------

const toolGenSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["tool_key", "description", "input_schema", "output_schema", "capabilities", "effect_class"],
  "properties": {
    "tool_key": {"type": "string"},
    "description": {"type": "string"},
    "input_schema": {"type": "object", "additionalProperties": true},
    "output_schema": {"type": "object", "additionalProperties": true},
    "capabilities": {"type": "array", "items": {"type": "string"}},
    "effect_class": {"type": "string", "enum": ["READ", "EXTERNAL_WRITE", "INTERNAL_WRITE"]}
  }
}`

const toolSystemPrompt = `你是企业工具（Tool）设计专家。请根据用户需求（与可选制度文档）设计一个平台可注册的工具。
要求：
1. tool_key 用英文小写短横线命名；description 用中文说明用途；
2. input_schema / output_schema 用 JSON Schema 描述，字段名用英文；
3. effect_class 取值：READ（只读）、EXTERNAL_WRITE（写外部系统，如 ERP）、INTERNAL_WRITE（写平台内部）；
   只读查询一律用 READ，涉及外部系统下发才用 EXTERNAL_WRITE；
4. capabilities 是能力标签数组（如 ["erp","query"]）；
5. 严格输出符合给定 JSON Schema 的单个 JSON 对象，不要输出任何解释性文字。`

// GenerateToolRequest 是 POST /api/v1/tools/generate 的请求体。
type GenerateToolRequest struct {
	Instructions   string `json:"instructions,omitempty"`
	SourcePath     string `json:"source_path,omitempty"`
	ToolKey        string `json:"tool_key,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	MaxSourceChars int    `json:"max_source_chars,omitempty"`
}

// GenerateTool 处理 POST /api/v1/tools/generate。
func (h *Handler) GenerateTool(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	if _, err := h.llmGateway(); err != nil {
		h.fail(w, r, err)
		return
	}
	var req GenerateToolRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	g, err := h.prepareGenContext(req.Instructions, req.SourcePath, req.MaxSourceChars)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	user := g.buildUserPrompt()
	if req.ToolKey != "" {
		user += "\n\n请使用 tool_key = " + strings.TrimSpace(req.ToolKey)
	}

	obj, comp, err := h.llmJSON(r.Context(), id.TenantID, req.ModelID,
		toolSystemPrompt, user, []byte(toolGenSchema), idempotencyKey(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	toolKey, err := requireString(obj, "tool_key")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	description, _ := obj["description"].(string)
	inSchema, err := requireObject(obj, "input_schema")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	outSchema, err := requireObject(obj, "output_schema")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	effectClass, err := requireString(obj, "effect_class")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var caps []string
	if arr, ok := obj["capabilities"].([]any); ok {
		for _, c := range arr {
			if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
				caps = append(caps, s)
			}
		}
	}

	out, err := h.deps.Store.CreateTool(r.Context(), store.CreateToolInput{
		ToolKey:      toolKey,
		Description:  description,
		InputSchema:  inSchema,
		OutputSchema: outSchema,
		Capabilities: caps,
		EffectClass:  effectClass,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "ToolGenerated", "tool.generate", "SUCCESS", "tool",
		stringify(out["id"]), ids.ID{}, map[string]any{
			"tool_key": toolKey, "source_path": req.SourcePath,
			"model": modelRevisionOf(comp), "idempotency_key": idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "ToolGenerated", sse.Important,
		map[string]any{"tool_id": stringify(out["id"]), "tool_key": toolKey})
	out["generation"] = generationEnvelope(g, comp)
	h.writeJSON(w, http.StatusCreated, out)
}

// ---------------------------------------------------------------------------
// 工作流自动生成
// ---------------------------------------------------------------------------

const workflowGenSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["workflow_key", "name", "description", "definition"],
  "properties": {
    "workflow_key": {"type": "string"},
    "name": {"type": "string"},
    "description": {"type": "string"},
    "definition": {
      "type": "object", "additionalProperties": true,
      "required": ["nodes", "edges"],
      "properties": {
        "nodes": {"type": "array", "items": {"type": "object", "additionalProperties": true}},
        "edges": {"type": "array", "items": {"type": "object", "additionalProperties": true}}
      }
    },
    "input_schema": {"type": "object", "additionalProperties": true},
    "output_schema": {"type": "object", "additionalProperties": true}
  }
}`

const workflowSystemPrompt = `你是企业工作流设计专家。请根据用户需求（与可选制度文档）设计一个可编排的工作流。
要求：
1. workflow_key 用英文小写短横线命名；name 用中文；
2. definition.nodes 每个节点含 id、type（start/end/task/agent/tool/approval/condition）、name，可按需含 agent_key/tool_key；
3. definition.edges 每条边含 from、to，条件分支可含 condition；
4. 节点必须从 start 可达并最终抵达 end；
5. input_schema / output_schema 用 JSON Schema 描述工作流对外契约；
6. 严格输出符合给定 JSON Schema 的单个 JSON 对象，不要输出任何解释性文字。`

// GenerateWorkflowRequest 是 POST /api/v1/workflows/generate 的请求体。
type GenerateWorkflowRequest struct {
	Instructions   string `json:"instructions,omitempty"`
	SourcePath     string `json:"source_path,omitempty"`
	WorkflowKey    string `json:"workflow_key,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	MaxSourceChars int    `json:"max_source_chars,omitempty"`
}

// GenerateWorkflow 处理 POST /api/v1/workflows/generate。
func (h *Handler) GenerateWorkflow(w http.ResponseWriter, r *http.Request) {
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	if _, err := h.llmGateway(); err != nil {
		h.fail(w, r, err)
		return
	}
	var req GenerateWorkflowRequest
	if err := decodeJSON(r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	echoIdempotency(w, r)

	g, err := h.prepareGenContext(req.Instructions, req.SourcePath, req.MaxSourceChars)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	user := g.buildUserPrompt()
	if req.WorkflowKey != "" {
		user += "\n\n请使用 workflow_key = " + strings.TrimSpace(req.WorkflowKey)
	}

	obj, comp, err := h.llmJSON(r.Context(), id.TenantID, req.ModelID,
		workflowSystemPrompt, user, []byte(workflowGenSchema), idempotencyKey(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	workflowKey, err := requireString(obj, "workflow_key")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	name, err := requireString(obj, "name")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	description, _ := obj["description"].(string)
	definition, err := requireObject(obj, "definition")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var defObj map[string]any
	_ = json.Unmarshal(definition, &defObj)
	if nodes, ok := defObj["nodes"].([]any); !ok || len(nodes) == 0 {
		h.fail(w, r, errors.E(errors.CodeValidationFailed, "definition.nodes 缺失或为空"))
		return
	}

	var inSchema, outSchema []byte
	if b, err := requireObject(obj, "input_schema"); err == nil {
		inSchema = b
	}
	if b, err := requireObject(obj, "output_schema"); err == nil {
		outSchema = b
	}

	out, err := h.deps.Store.CreateWorkflow(r.Context(), store.CreateWorkflowInput{
		WorkflowKey:  workflowKey,
		Name:         name,
		Description:  description,
		Definition:   definition,
		InputSchema:  inSchema,
		OutputSchema: outSchema,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "WorkflowGenerated", "workflow.generate", "SUCCESS", "workflow",
		stringify(out["id"]), ids.ID{}, map[string]any{
			"workflow_key": workflowKey, "source_path": req.SourcePath,
			"model": modelRevisionOf(comp), "idempotency_key": idempotencyKey(r),
		})
	h.publish(id.TenantID.String(), "WorkflowGenerated", sse.Important,
		map[string]any{"workflow_id": stringify(out["id"]), "workflow_key": workflowKey})
	out["generation"] = generationEnvelope(g, comp)
	h.writeJSON(w, http.StatusCreated, out)
}
