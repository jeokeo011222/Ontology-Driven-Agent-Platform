package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Skill 展开（计划编译期）—— Skill 作为一等 registry 资源的运行时落点。
//
// Agent 版本通过 dependency_manifest.skills（skill_key 列表）声明技能依赖。
// 计划编译时对每个键取**最新已发布**技能版本：
//   1. prompt_fragment 以「## 技能：{name}」小节追加进 Agent 提示词；
//   2. tool_bindings 的 tool_key 并入本次运行可用工具集合（与
//      Intent.AllowedTools / 计划 Node.ToolKey 的既有白名单机制对齐）；
//   3. 技能版本 digest 纳入 VersionLock.ToolBindingDigest（BindSkills），
//      技能版本一旦变更 → 计划缓存失效 → 重新编译。
//
// fail-closed 原则：技能不存在 / 没有已发布版本 / 加载失败 → 计划编译失败，
// 返回明确错误；严禁跳过或静默忽略（未发布的技能不得参与运行）。
// ---------------------------------------------------------------------------

// SkillSnapshot 是计划编译所需的技能版本快照（与存储实现解耦的最小面）。
type SkillSnapshot struct {
	SkillKey       string
	Name           string
	Version        string
	VersionID      string
	PromptFragment string
	ToolBindings   []string
	ContentDigest  string
}

// SkillLoader 抽象「按 skill_key 批量取最新已发布版本」。
// 生产实现：registry.PluginService.ResolvePublishedSkillVersions（RLS 租户事务）
// 经 SkillLoaderFunc 适配注入；测试用内存假实现。
// 请求中不存在/未发布的键不会出现在返回值里，由 CompileSkills fail-closed。
type SkillLoader interface {
	PublishedSkillVersions(ctx context.Context, tenantID ids.ID, skillKeys []string) ([]SkillSnapshot, error)
}

// SkillLoaderFunc 适配任意函数为 SkillLoader（生产装配点：cmd 层把
// registry.PluginService 的查询结果映射成 []SkillSnapshot）。
type SkillLoaderFunc func(ctx context.Context, tenantID ids.ID, skillKeys []string) ([]SkillSnapshot, error)

// PublishedSkillVersions 实现 SkillLoader。
func (f SkillLoaderFunc) PublishedSkillVersions(ctx context.Context, tenantID ids.ID, skillKeys []string) ([]SkillSnapshot, error) {
	return f(ctx, tenantID, skillKeys)
}

// SkillExpansion 是技能展开结果：提示词小节、并入的工具键、版本锁定摘要。
type SkillExpansion struct {
	// PromptSections 形如「## 技能：{name}\n{prompt_fragment}」，按 skill_key 升序。
	PromptSections []string
	// ToolKeys 是全部技能 tool_bindings 的去重并集（升序），并入可用工具集合。
	ToolKeys []string
	// Digest 是技能版本锁定摘要：sha256(skill_key@version@content_digest 按
	// skill_key 升序拼接)。技能版本任何变更都会改变该值。
	Digest string
}

// ApplyToPrompt 把技能小节追加到 Agent 系统提示词（空展开时原样返回）。
func (e SkillExpansion) ApplyToPrompt(systemPrompt string) string {
	if len(e.PromptSections) == 0 {
		return systemPrompt
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(systemPrompt, "\n"))
	for _, sec := range e.PromptSections {
		b.WriteString("\n\n")
		b.WriteString(sec)
	}
	return b.String()
}

// MergeToolKeys 把技能工具键并入既有可用工具集合（去重 + 升序，返回新切片）。
func (e SkillExpansion) MergeToolKeys(existing []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(e.ToolKeys))
	out := make([]string, 0, len(existing)+len(e.ToolKeys))
	for _, k := range existing {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, k := range e.ToolKeys {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CompileSkills 对 manifest.skills 逐个取已发布技能版本并展开。
//
// skillKeys 为空 → 返回零值展开（无技能依赖的 Agent 不受影响，内置/内置清单
// 语义不变）。任一键缺失或未发布 → 返回 CodeNotFound 明确错误（fail-closed，
// 禁止跳过或静默忽略）。
func CompileSkills(ctx context.Context, loader SkillLoader, tenantID ids.ID, skillKeys []string) (*SkillExpansion, error) {
	if len(skillKeys) == 0 {
		return &SkillExpansion{}, nil
	}
	// 键序规范化：请求序不影响产物（小节与摘要都按 skill_key 升序落定）。
	want := make([]string, len(skillKeys))
	copy(want, skillKeys)
	sort.Strings(want)

	snaps, err := loader.PublishedSkillVersions(ctx, tenantID, want)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "加载技能版本失败（计划编译拒绝降级）")
	}
	byKey := make(map[string]SkillSnapshot, len(snaps))
	for _, s := range snaps {
		byKey[s.SkillKey] = s
	}
	// fail-closed：逐键核对，缺失（不存在 / 未发布 / 已软删）即失败。
	for _, k := range want {
		if _, ok := byKey[k]; !ok {
			return nil, errors.Ef(errors.CodeNotFound,
				"技能 %q 不存在或没有已发布版本（计划编译失败，拒绝静默跳过）", k)
		}
	}

	exp := &SkillExpansion{
		PromptSections: make([]string, 0, len(want)),
		ToolKeys:       []string{},
	}
	toolSeen := map[string]struct{}{}
	digestParts := make([]string, 0, len(want))
	for _, k := range want {
		s := byKey[k]
		exp.PromptSections = append(exp.PromptSections,
			fmt.Sprintf("## 技能：%s\n%s", s.Name, s.PromptFragment))
		for _, tk := range s.ToolBindings {
			if tk == "" {
				continue
			}
			if _, dup := toolSeen[tk]; dup {
				continue
			}
			toolSeen[tk] = struct{}{}
			exp.ToolKeys = append(exp.ToolKeys, tk)
		}
		digestParts = append(digestParts, s.SkillKey+"@"+s.Version+"@"+s.ContentDigest)
	}
	sort.Strings(exp.ToolKeys)
	sum := sha256.Sum256([]byte(strings.Join(digestParts, ";")))
	exp.Digest = hex.EncodeToString(sum[:])
	return exp, nil
}

// BindSkills 把技能版本摘要并入版本锁定（ToolBindingDigest）。
//
// skillDigest 为空 → 原样返回（无技能依赖的锁定摘要不变）。
// 否则 ToolBindingDigest ← sha256("tool:"+旧值+";skills:"+skillDigest)：
// 工具绑定或技能版本任一变化都会得到新摘要，计划缓存随之下一次编译失效。
func (v VersionLock) BindSkills(skillDigest string) VersionLock {
	if skillDigest == "" {
		return v
	}
	sum := sha256.Sum256([]byte("tool:" + v.ToolBindingDigest + ";skills:" + skillDigest))
	v.ToolBindingDigest = hex.EncodeToString(sum[:])
	return v
}
