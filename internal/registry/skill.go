package registry

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// SkillVersionSnapshot 是计划编译所需的**已发布**技能版本快照
// （registry.skills JOIN registry.skill_versions，status='PUBLISHED'）。
type SkillVersionSnapshot struct {
	SkillID        ids.ID
	VersionID      ids.ID
	TenantID       ids.ID
	SkillKey       string
	Name           string
	Domain         string
	Version        string
	PromptFragment string
	// ToolBindings 是该技能运行时要并入可用工具集合的 tool_key 列表
	// （写入时已由应用层校验指向 registry.tools 的 ACTIVE 工具）。
	ToolBindings  []string
	ContentDigest string
}

// ResolvePublishedSkillVersions 按技能键批量解析租户内**最新已发布**版本。
//
// 这是 Agent 计划编译的运行时热路径（dependency_manifest.skills → 展开）。
// 返回结果按 skill_key 升序；请求中不存在 / 未发布 / 已软删的键**不会出现**
// 在结果里，由调用方（internal/agent 计划编译）fail-closed 报错，
// 本层绝不静默补空或降级。
func (s *PluginService) ResolvePublishedSkillVersions(ctx context.Context, tenantID ids.ID, skillKeys []string) ([]SkillVersionSnapshot, error) {
	if len(skillKeys) == 0 {
		return []SkillVersionSnapshot{}, nil
	}
	var out []SkillVersionSnapshot
	err := readInTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		// ::text 投影 jsonb/uuid 列（pgx 读取惯例，见 store.queryMaps 的历史坑）。
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, v.id::text, s.tenant_id::text,
			       s.skill_key, s.name, s.domain,
			       v.version, v.prompt_fragment,
			       v.tool_bindings::text, v.content_digest
			  FROM registry.skills s
			  JOIN LATERAL (
			      SELECT id, version, prompt_fragment, tool_bindings, content_digest
			        FROM registry.skill_versions
			       WHERE skill_id = s.id AND status = 'PUBLISHED'
			       ORDER BY created_at DESC LIMIT 1
			  ) v ON TRUE
			 WHERE s.status = 'ACTIVE' AND s.skill_key = ANY($1)
			 ORDER BY s.skill_key ASC`, skillKeys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				skillTxt, versionTxt, tenantTxt string
				bindingsTxt                     string
				snap                            SkillVersionSnapshot
			)
			if err := rows.Scan(&skillTxt, &versionTxt, &tenantTxt,
				&snap.SkillKey, &snap.Name, &snap.Domain,
				&snap.Version, &snap.PromptFragment,
				&bindingsTxt, &snap.ContentDigest); err != nil {
				return err
			}
			if snap.SkillID, err = ids.Parse(skillTxt); err != nil {
				return err
			}
			if snap.VersionID, err = ids.Parse(versionTxt); err != nil {
				return err
			}
			if snap.TenantID, err = ids.Parse(tenantTxt); err != nil {
				return err
			}
			snap.ToolBindings = parseToolBindings(bindingsTxt)
			out = append(out, snap)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "解析已发布技能版本失败")
	}
	return out, nil
}

// parseToolBindings 从 tool_bindings::text（JSON 字符串数组）解析 tool_key 列表。
// 非数组/解析失败一律返回空切片 —— 该列在写入侧已保证是字符串数组，
// 这里只做防御性读取，绝不错误注入数据参与白名单合并。
func parseToolBindings(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return out
	}
	return append(out, arr...)
}
