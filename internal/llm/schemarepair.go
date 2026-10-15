package llm

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"

	"odap/internal/platform/errors"
	"odap/internal/tools"
)

// MaxSchemaRepairAttempts 是有界修复回路的默认重生成上限 N（第九篇 §9.1：
// 默认 2 次，按任务契约可配置）。
const MaxSchemaRepairAttempts = 2

// CompleteStructured 实现结构化输出与工具调用治理链路（第九篇 §9.1）：
//
//   - 若 Provider 声明 NativeSchema（端点原生严格 Schema 约束生成），直接请求；
//   - 否则（generate-then-validate）要求 Provider 先自由生成，再本地校验
//     json.Unmarshal + tools.ValidateArgs；校验失败进入受限重生成：把校验诊断
//     作为追加消息回灌，最多 N=MaxSchemaRepairAttempts 次；
//   - N 次后仍不合法，返回 CodeModelOutputInvalid，并附最后一次校验错误。
//
// 注意：本体的语义/业务校验（第九篇 §9.1 后几层）不在此回路内——结构合法但
// 语义失败应由 Harness 决定修正输入或转人工，不能让模型对同一错误反复重试。
func CompleteStructured(ctx context.Context, p Provider, req CompletionRequest) (Completion, error) {
	if p == nil {
		return Completion{}, errors.E(errors.CodeInternal, "llm: provider 为空")
	}
	// 无 Schema 要求：与普通文本调用一致。
	if len(req.ResponseSchema) == 0 {
		return p.Complete(ctx, req)
	}
	// native schema：直接请求（第九篇 §9.1 能力声明区分两种结构化输出能力）。
	if p.Capabilities().NativeSchema {
		return p.Complete(ctx, req)
	}

	var lastErr error
	cur := req
	for attempt := 0; attempt <= MaxSchemaRepairAttempts; attempt++ {
		comp, err := p.Complete(ctx, cur)
		if err != nil {
			// 供应商错误不进入修复回路（第九篇 §10.4：错误分类由 Gateway 决定）。
			return Completion{}, err
		}
		if verr := validateCompletion(cur, comp); verr != nil {
			lastErr = verr
			// 确定性失败（如被输出上限截断）不进修复回路：再生成一次只会以同样方式
			// 失败，还白花两次调用与额度。修复回路的假设是"模型只是格式没跟对，把
			// 诊断回灌就有救"，截断不满足这个假设。
			if isUnrepairable(verr) || attempt == MaxSchemaRepairAttempts {
				break
			}
			cur.Messages = append(cur.Messages, repairMessages(comp, verr)...)
			continue
		}
		return comp, nil
	}
	if isUnrepairable(lastErr) {
		// 不能说"N 次受限重生成后仍不合法"——这类失败一次重生成都没做（做了也没用），
		// 错误的措辞会把排查方向引到"模型不听话"，而不是真正的成因（如输出上限不够）。
		return Completion{}, errors.Ef(errors.CodeModelOutputInvalid,
			"llm: 结构化输出不合法且重生成无法修复：%v", lastErr)
	}
	return Completion{}, errors.Ef(errors.CodeModelOutputInvalid,
		"llm: 结构化输出在 %d 次受限重生成后仍不合法：%v", MaxSchemaRepairAttempts, lastErr)
}

// validateCompletion 做第一层校验（JSON 语法 + Schema）：优先校验工具调用参数，
// 否则校验 Content 文本。
func validateCompletion(req CompletionRequest, comp Completion) error {
	if len(req.ResponseSchema) == 0 {
		return nil
	}
	if len(comp.ToolCalls) > 0 {
		for _, tc := range comp.ToolCalls {
			spec, ok := findTool(req.Tools, tc.Name)
			if !ok || len(spec.InputSchema) == 0 {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal(tc.Arguments, &args); err != nil {
				return errors.Wrap(err, errors.CodeValidationFailed,
					"llm: 工具调用参数非合法 JSON（tool="+tc.Name+"）")
			}
			if err := tools.ValidateArgs(spec.InputSchema, args); err != nil {
				return err
			}
		}
		return nil
	}
	if comp.Content == "" {
		return errors.E(errors.CodeValidationFailed, "llm: 结构化输出为空")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(comp.Content), &obj); err != nil {
		return invalidJSONError(comp, err)
	}
	return tools.ValidateArgs(req.ResponseSchema, obj)
}

// invalidJSONError 把"结构化输出不是合法 JSON"细化为可自诊断的错误。
//
// 为什么必须带诊断：`unexpected end of JSON input` 说明内容**没写完**——它不是格式跑偏
// （那会报 `invalid character ...`）。成因只有两个：输出被输出上限截断，或内容本身过短。
// 没有 finish_reason 与长度时，用户与运维只能看到一句"非合法 JSON"，无从判断是输出
// 上限太小、输入文档太长，还是模型本身有问题。
func invalidJSONError(comp Completion, cause error) error {
	if comp.FinishReason == FinishLength {
		// 截断标记为不可修复（见 isUnrepairable）：修复回路会把上一轮输出当 assistant
		// 消息回灌，输入只会更长、更容易再次截断，两次重生成必然同样失败——纯属白花
		// 额度，还会把失败原因说得更含糊。
		return unrepairable(errors.Wrap(cause, errors.CodeValidationFailed, fmt.Sprintf(
			"llm: 结构化输出非合法 JSON：输出被输出上限截断（finish_reason=length，输出 %d tokens，内容 %d 字符）；"+
				"同样的输入重生成只会再次截断，需提高输出上限或缩短输入文档",
			comp.OutputTokens, len(comp.Content))))
	}
	if comp.FinishReason == "" {
		return errors.Wrap(cause, errors.CodeValidationFailed, fmt.Sprintf(
			"llm: 结构化输出非合法 JSON（端点未返回 finish_reason，输出 %d tokens，内容 %d 字符）",
			comp.OutputTokens, len(comp.Content)))
	}
	return errors.Wrap(cause, errors.CodeValidationFailed, fmt.Sprintf(
		"llm: 结构化输出非合法 JSON（finish_reason=%s，输出 %d tokens，内容 %d 字符）",
		comp.FinishReason, comp.OutputTokens, len(comp.Content)))
}

// unrepairableError 标记"再重生成一次也不会变好"的校验失败。
//
// 与普通校验失败的区别是**控制流**：普通失败进入受限重生成，标记过的直接快速失败。
type unrepairableError struct{ err error }

func (e unrepairableError) Error() string { return e.err.Error() }

func (e unrepairableError) Unwrap() error { return e.err }

func unrepairable(err error) error { return unrepairableError{err: err} }

func isUnrepairable(err error) bool {
	var target unrepairableError
	return stderrors.As(err, &target)
}

// repairMessages 把上一轮输出与校验诊断回灌为追加消息（第九篇 §9.1 第 1 条）。
func repairMessages(comp Completion, verr error) []Message {
	bad := comp.Content
	if bad == "" && len(comp.ToolCalls) > 0 {
		if b, err := json.Marshal(comp.ToolCalls); err == nil {
			bad = string(b)
		}
	}
	return []Message{
		{Role: "assistant", Content: bad},
		{Role: "user", Content: "上一次输出未通过结构化校验，请修正后重新生成。校验诊断：" + verr.Error()},
	}
}

// findTool 按名字查找工具声明。
func findTool(specs []ToolSpec, name string) (ToolSpec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return ToolSpec{}, false
}
