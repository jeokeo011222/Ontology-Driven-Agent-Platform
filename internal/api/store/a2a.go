package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// A2A 远程智能体注册表（a2a.remote_agents，迁移 000043）
//
// 模块 C「Server 发布 + 注册表」的持久化面：登记远端 A2A Server 的地址与
// 出站认证引用（环境变量名，明文凭据绝不落库/日志），供网关出站调用与
// 连通性探测使用。全部读写走租户事务（RLS）。
//
// 约定与既有 registry 资源一致：删除一律软删（status=DELETED），软删行
// 不出现在默认列表与读取路径；jsonb/数组列读取显式 ::text / 原生投影。
// ---------------------------------------------------------------------------

// remoteAgentColumns 是 remote_agents 的统一投影。
// capabilities 以 ::text 读出后在 remoteAgentRows 里解析为原生 JSON；
// tool_whitelist 保持 text[] 原生投影（pgx 返回 []string，可安全序列化）。
const remoteAgentColumns = `
    id::text AS id, agent_key, name, description, base_url,
    auth_header_name, auth_header_ref, capabilities::text AS capabilities,
    tool_whitelist, status, last_probe_at, last_probe_ok, last_probe_error,
    created_at, updated_at`

// a2aAgentKeyPattern 是 agent_key 的合法形态：小写字母或数字开头，
// 2~64 位小写字母/数字/下划线/连字符（与平台内 seed 的 agent_key 风格一致）。
var a2aAgentKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// validateA2AAgentKey 服务端校验 agent_key。
func validateA2AAgentKey(key string) error {
	if !a2aAgentKeyPattern.MatchString(key) {
		return errors.E(errors.CodeInvalidArgument,
			"agent_key 不合法：须为 2~64 位小写字母/数字开头，仅含小写字母、数字、下划线或连字符")
	}
	return nil
}

// validateA2ABaseURL 服务端校验 base_url：必须是 http(s):// 开头的绝对地址。
func validateA2ABaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.E(errors.CodeInvalidArgument, "base_url 必须是 http(s):// 开头的绝对地址（含主机名）")
	}
	return nil
}

// validRemoteAgentStatus 是 status 筛选参数的合法取值。
var validRemoteAgentStatus = map[string]bool{
	"ACTIVE": true, "DISABLED": true, "DELETED": true,
}

// remoteAgentRows 把查询结果行的 capabilities 文本解析为原生 JSON；
// 解析失败时置为 null（不编造），其余列由 queryMaps 归一化。
func remoteAgentRows(rows []map[string]any) []map[string]any {
	for _, row := range rows {
		if s, ok := row["capabilities"].(string); ok && json.Valid([]byte(s)) {
			row["capabilities"] = json.RawMessage(s)
		} else if ok {
			row["capabilities"] = nil
		}
	}
	return rows
}

// CreateRemoteAgentInput 是创建远程智能体登记的输入。
type CreateRemoteAgentInput struct {
	AgentKey       string
	Name           string
	Description    string
	BaseURL        string
	AuthHeaderName string
	AuthHeaderRef  string
	// Capabilities 是 jsonb 载荷（可为空 → '{}'）。
	Capabilities []byte
	// ToolWhitelist 是允许远端调用的本平台工具键白名单；nil → 空数组（= 不允许任何工具）。
	ToolWhitelist []string
}

// CreateRemoteAgent 登记一个远端 A2A Server（UNIQUE(tenant_id, agent_key)）。
func (s *Store) CreateRemoteAgent(ctx context.Context, in CreateRemoteAgentInput) (map[string]any, error) {
	in.AgentKey = strings.TrimSpace(in.AgentKey)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.Name == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "name 必填")
	}
	if err := validateA2AAgentKey(in.AgentKey); err != nil {
		return nil, err
	}
	if err := validateA2ABaseURL(in.BaseURL); err != nil {
		return nil, err
	}
	caps, _ := optionalJSON(in.Capabilities)
	if in.ToolWhitelist == nil {
		in.ToolWhitelist = []string{}
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		agentID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO a2a.remote_agents
                (id, agent_key, name, description, base_url,
                 auth_header_name, auth_header_ref, capabilities, tool_whitelist, created_by)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)`,
			agentID, in.AgentKey, in.Name, in.Description, in.BaseURL,
			in.AuthHeaderName, in.AuthHeaderRef, string(caps), in.ToolWhitelist, createdBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "远程智能体已存在（agent_key 与本租户已有登记冲突）或写入失败")
		}
		rows, err := queryMaps(ctx, tx, `SELECT `+remoteAgentColumns+` FROM a2a.remote_agents WHERE id = $1`, agentID)
		if err != nil {
			return err
		}
		out = remoteAgentRows(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.E(errors.CodeInternal, "远程智能体写入后读取失败")
	}
	return out[0], nil
}

// GetRemoteAgent 按 id 读取远程智能体（软删行视为不存在）。
func (s *Store) GetRemoteAgent(ctx context.Context, id ids.ID) (map[string]any, error) {
	var out []map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx,
			`SELECT `+remoteAgentColumns+` FROM a2a.remote_agents WHERE id = $1 AND status <> 'DELETED'`,
			id.String())
		if err != nil {
			return err
		}
		out = remoteAgentRows(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.E(errors.CodeNotFound, "远程智能体不存在")
	}
	return out[0], nil
}

// ListRemoteAgents 返回远程智能体列表（按 agent_key 升序 cursor 分页，
// 支持 ?status= 与 ?q= 过滤）。软删行默认不出现，除非显式 status=DELETED。
func (s *Store) ListRemoteAgents(ctx context.Context, limit int, cursor, status, q string) (*ListResult, error) {
	if status != "" && !validRemoteAgentStatus[status] {
		return nil, errors.E(errors.CodeInvalidArgument, "status 只能是 ACTIVE、DISABLED 或 DELETED")
	}
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT `+remoteAgentColumns+`
              FROM a2a.remote_agents
             WHERE ($1::text IS NULL OR agent_key > $1)
               AND (($3::text = '' AND status <> 'DELETED') OR ($3::text <> '' AND status = $3::text))
               AND ($4::text = '' OR agent_key ILIKE '%' || $4 || '%'
                    OR name ILIKE '%' || $4 || '%'
                    OR coalesce(description, '') ILIKE '%' || $4 || '%'
                    OR base_url ILIKE '%' || $4 || '%')
             ORDER BY agent_key ASC
             LIMIT $2`, nullableStr(from), limit+1, status, q)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = remoteAgentRows(rows)
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["agent_key"].(string); ok {
				res.NextCursor = encKey(k)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// UpdateRemoteAgentInput 是 PATCH 更新输入（空串/nil = 不改动；agent_key 不可变）。
type UpdateRemoteAgentInput struct {
	Name           string
	Description    string
	BaseURL        string
	AuthHeaderName string
	AuthHeaderRef  string
	// Status 仅接受 ACTIVE / DISABLED（DELETED 只能走 Delete，fail-closed）。
	Status string
	// Capabilities 非 nil 时整体替换（必须是合法 JSON）；nil = 不改动。
	Capabilities []byte
	// ToolWhitelist 非 nil 时整体替换（可为空数组清空白名单）；nil = 不改动。
	ToolWhitelist []string
}

// UpdateRemoteAgent 更新远程智能体登记（PATCH 语义：空值不改）。
func (s *Store) UpdateRemoteAgent(ctx context.Context, id ids.ID, in UpdateRemoteAgentInput) (map[string]any, error) {
	if in.Status != "" && in.Status != "ACTIVE" && in.Status != "DISABLED" {
		return nil, errors.E(errors.CodeInvalidArgument, "status 只能是 ACTIVE 或 DISABLED（删除请走 DELETE）")
	}
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	if in.BaseURL != "" {
		if err := validateA2ABaseURL(in.BaseURL); err != nil {
			return nil, err
		}
	}
	var caps *string
	if in.Capabilities != nil {
		if !json.Valid(in.Capabilities) {
			return nil, errors.E(errors.CodeInvalidArgument, "capabilities 必须是合法 JSON")
		}
		c := string(in.Capabilities)
		caps = &c
	}
	var out []map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE a2a.remote_agents
               SET name             = COALESCE(NULLIF($2, ''), name),
                   description      = COALESCE(NULLIF($3, ''), description),
                   base_url         = COALESCE(NULLIF($4, ''), base_url),
                   auth_header_name = COALESCE(NULLIF($5, ''), auth_header_name),
                   auth_header_ref  = COALESCE(NULLIF($6, ''), auth_header_ref),
                   status           = COALESCE(NULLIF($7, ''), status),
                   capabilities     = COALESCE($8::jsonb, capabilities),
                   tool_whitelist   = COALESCE($9, tool_whitelist),
                   updated_at       = now()
             WHERE id = $1 AND status <> 'DELETED'`,
			id.String(), nullableStr(in.Name), nullableStr(in.Description), nullableStr(in.BaseURL),
			nullableStr(in.AuthHeaderName), nullableStr(in.AuthHeaderRef), nullableStr(in.Status),
			caps, in.ToolWhitelist)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新远程智能体失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "远程智能体不存在")
		}
		rows, err := queryMaps(ctx, tx,
			`SELECT `+remoteAgentColumns+` FROM a2a.remote_agents WHERE id = $1`, id.String())
		if err != nil {
			return err
		}
		out = remoteAgentRows(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.E(errors.CodeInternal, "远程智能体更新后读取失败")
	}
	return out[0], nil
}

// DeleteRemoteAgent 软删除远程智能体（status → DELETED）。
func (s *Store) DeleteRemoteAgent(ctx context.Context, id ids.ID) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE a2a.remote_agents SET status = 'DELETED', updated_at = now()
             WHERE id = $1 AND status <> 'DELETED'`, id.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "删除远程智能体失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "远程智能体不存在或已删除")
		}
		return nil
	})
}

// UpdateRemoteAgentProbe 写入最近一次连通性探测结论。
//
// 探测失败是预期内的诊断结果（错误信息入 last_probe_error 供界面展示），
// 而不是系统故障；凭据与认证头取值绝不入 error 文本。
func (s *Store) UpdateRemoteAgentProbe(ctx context.Context, id ids.ID, ok bool, probeErr string) error {
	return s.TenantTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE a2a.remote_agents
               SET last_probe_at = now(), last_probe_ok = $2, last_probe_error = $3, updated_at = now()
             WHERE id = $1 AND status <> 'DELETED'`, id.String(), ok, probeErr)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "写入探测结论失败")
		}
		if tag.RowsAffected() == 0 {
			return errors.E(errors.CodeNotFound, "远程智能体不存在或已删除")
		}
		return nil
	})
}

// LatestPublishedAgentVersion 按 agent_key 解析最新**已发布**的 Agent 版本 id
// （A2A 网关把 metadata.agent_key 映射为可执行 AgentVersionID 的唯一入口）。
//
// 不存在 / 软删 / 没有已发布版本一律 404（fail-closed，绝不静默回退到别的智能体）。
func (s *Store) LatestPublishedAgentVersion(ctx context.Context, agentKey string) (ids.ID, error) {
	var versionID string
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
            SELECT v.id::text
              FROM registry.agents a
              JOIN LATERAL (
                  SELECT id FROM registry.agent_versions
                   WHERE agent_id = a.id AND status = 'PUBLISHED'
                   ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE a.agent_key = $1 AND a.status <> 'DELETED'`, agentKey).Scan(&versionID)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return ids.ID{}, errors.Ef(errors.CodeNotFound,
				"智能体 %q 不存在、已删除或没有已发布版本（A2A 调用拒绝悬空引用）", agentKey)
		}
		return ids.ID{}, errors.Wrap(err, errors.CodeInternal, "解析智能体版本失败")
	}
	return ids.Parse(versionID)
}

// DefaultChatAgentVersion 解析 A2A 缺省路由的智能体版本：
// 首个「已发布且带非空系统提示词」的 Agent（按创建时间取最旧，保证确定性，
// 不随新增 Agent 漂移）。run-orchestrator 等编排型 Agent 没有 prompt_template，
// 派给它们的消息会在 render_prompt 阶段 fail-closed——所以缺省路由绝不能落
// 在编排器上；没有任何可对话 Agent 时明确报错，让调用方用 metadata.agent_key
// 显式指定。
func (s *Store) DefaultChatAgentVersion(ctx context.Context) (string, ids.ID, error) {
	var agentKey, versionID string
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
            SELECT a.agent_key, v.id::text
              FROM registry.agents a
              JOIN LATERAL (
                  SELECT id, prompt_template, created_at FROM registry.agent_versions
                   WHERE agent_id = a.id AND status = 'PUBLISHED'
                   ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE a.status <> 'DELETED'
               AND coalesce(v.prompt_template::jsonb ->> 'system', '') <> ''
             ORDER BY v.created_at ASC
             LIMIT 1`).Scan(&agentKey, &versionID)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ids.ID{}, errors.E(errors.CodeNotFound,
				"没有可用的可对话智能体（缺少含系统提示词的已发布 Agent）：请在请求的 metadata.agent_key 显式指定，或先登记并发布一个业务 Agent")
		}
		// 诊断信息必须可见：数据库层错误被 wrap 掩盖时无法定位（冒烟教训）。
		return "", ids.ID{}, errors.Wrap(err, errors.CodeInternal,
			fmt.Sprintf("解析缺省智能体失败：%v", err))
	}
	vid, err := ids.Parse(versionID)
	if err != nil {
		return "", ids.ID{}, errors.Wrap(err, errors.CodeInternal, "智能体版本 id 不合法")
	}
	return agentKey, vid, nil
}

// A2AAgentCardSkills 汇总 Agent Card 的 skills 数组：
//   - registry 里 status='ACTIVE' 的全部 Agent（内置 + 用户自建），各一条
//     {id: agent_key, name, description}（name 取所属插件名，缺失时回退 agent_key）；
//   - 内置技能 enterprise-knowledge-qa 若存在（未软删）再追加一条。
//
// 运行期实时装配，不落静态数据。
func (s *Store) A2AAgentCardSkills(ctx context.Context) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		agents, err := queryMaps(ctx, tx, `
            SELECT a.agent_key AS id, coalesce(nullif(p.name, ''), a.agent_key) AS name,
                   coalesce(a.description, '') AS description
              FROM registry.agents a
              LEFT JOIN registry.plugins p ON p.id = a.plugin_id
             WHERE a.status = 'ACTIVE'
             ORDER BY a.agent_key ASC`)
		if err != nil {
			return err
		}
		out = append(out, agents...)
		skills, err := queryMaps(ctx, tx, `
            SELECT s.skill_key AS id, s.name AS name, coalesce(s.description, '') AS description
              FROM registry.skills s
             WHERE s.status <> 'DELETED' AND s.skill_key = 'enterprise-knowledge-qa'`)
		if err != nil {
			return err
		}
		out = append(out, skills...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
