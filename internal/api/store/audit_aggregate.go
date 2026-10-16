package store

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// AuditFilter 是审计查询过滤条件（第十一篇）。
//
// Outcome 为自由文本匹配（审计事件表 outcome 列无 CHECK 约束，实际写入
// SUCCESS / DENIED / REJECTED / ERROR 等取值，契约亦声明为自由 string）；
// 为空串表示不过滤。
type AuditFilter struct {
	RunID         *ids.ID
	ResourceType  string
	Action        string
	EventType     string
	Outcome       string
	ActorID       string
	OccurredAfter *time.Time
	Limit         int
	Cursor        string
}

// ListAuditEvents 按 occurred_at 降序 cursor 分页返回审计事件。
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) (*ListResult, error) {
	cur, err := DecodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}
	var (
		curTime *time.Time
		curID   *string
	)
	if cur != nil {
		curTime, curID = &cur.Time, &cur.Key
	}
	var runID *string
	if f.RunID != nil {
		if f.RunID.IsZero() {
			return nil, errors.E(errors.CodeInvalidArgument, "run_id 不合法")
		}
		v := f.RunID.String()
		runID = &v
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 投影口径说明：审计事件表并没有嵌套的 `actor` 对象，也没有 `decision_id`
		// 与 `policy_version` 列。哈希链用的是 event_digest / prev_digest（§8.7），
		// 主体展示名需从 iam.users 反查（actor_id 为文本，可能为空/非 UUID）。
		// 早期前端按 `item.actor.display_name`、`item.hash.slice(...)` 渲染，字段不存在
		// 直接抛 "Cannot read properties of undefined (reading 'display_name')" 导致整页白屏。
		// 这里把真实列一次性投影为前端可用的扁平字段（hash / prev_hash 即 digest 列）。
		rows, err := queryMaps(ctx, tx, `
            SELECT e.id::text AS id, e.event_type, e.actor_type, e.actor_id,
                   COALESCE(u.display_name, '') AS actor_display_name,
                   COALESCE(u.login_name, '') AS actor_login_name,
                   e.resource_type, e.resource_id, e.action, e.outcome,
                   e.request_id, e.run_id::text AS run_id, e.occurred_at,
                   e.segment_id, e.seq_no,
                   e.event_digest AS hash, e.prev_digest AS prev_hash
              FROM audit.audit_events e
              LEFT JOIN iam.users u ON u.id::text = e.actor_id
             WHERE ($1::uuid IS NULL OR e.run_id = $1::uuid)
               AND ($2::text IS NULL OR e.resource_type = $2)
               AND ($3::text IS NULL OR e.action = $3)
               AND ($4::text IS NULL OR e.event_type = $4)
               AND ($5::text IS NULL OR e.outcome = $5)
               AND ($6::timestamptz IS NULL OR e.occurred_at >= $6)
               AND ($7::timestamptz IS NULL OR (e.occurred_at, e.id) < ($7::timestamptz, $8::uuid))
               AND ($9::text IS NULL OR e.actor_id = $9)
             ORDER BY e.occurred_at DESC, e.id DESC
             LIMIT $10`,
			runID, nullableStr(f.ResourceType), nullableStr(f.Action), nullableStr(f.EventType),
			nullableStr(f.Outcome), f.OccurredAfter, curTime, curID, nullableStr(f.ActorID), f.Limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > f.Limit
		if hasMore {
			rows = rows[:f.Limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			if t, k, ok := keyedRow(last, "occurred_at", "id"); ok {
				res.NextCursor = nextCursorOf(t, k, true)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// BFF 聚合端点（第十七篇 §11.1）
// ---------------------------------------------------------------------------

// WorkspaceSummary 返回工作台聚合摘要。
func (s *Store) WorkspaceSummary(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		byStatus, err := queryMaps(ctx, tx, `
            SELECT status, count(*)::int AS n FROM runtime.agent_runs GROUP BY status`)
		if err != nil {
			return err
		}
		runs := map[string]int{}
		total := 0
		for _, r := range byStatus {
			st, _ := r["status"].(string)
			n := toInt(r["n"])
			runs[st] = n
			total += n
		}
		pending, err := queryMaps(ctx, tx, `
            SELECT count(*)::int AS n FROM workflow.approvals WHERE status = 'PENDING'`)
		if err != nil {
			return err
		}
		unknownOps, err := queryMaps(ctx, tx, `
            SELECT count(*)::int AS n FROM integration.erp_operations
             WHERE status IN ('UNKNOWN','RECONCILING')`)
		if err != nil {
			return err
		}
		out["runs_by_status"] = runs
		out["runs_total"] = total
		out["terminal_runs"] = runs["COMPLETED"] + runs["FAILED"] + runs["CANCELLED"] + runs["EXPIRED"]
		out["pending_approvals"] = firstInt(pending)
		out["unknown_external_operations"] = firstInt(unknownOps)
		out["reconciliation_pending"] = runs["REQUIRES_RECONCILIATION"]
		out["generated_at"] = time.Now().UTC()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Apps 返回业务工作台的「业务应用」视图。
//
// 数据源是**企业本体**而非 Agent：业务应用就是一套可被业务用户使用的本体
// （业务对象 + 规则），Agent 只是执行手段、由管理端「智能体」页单独管理。
// 此前这里查 registry.agents，导致业务应用页展示成了「所有智能体」的清单，
// 与产品口径（业务应用 = 本体）不符。

// 软删除（DELETED）的本体不出现在应用列表：删除的语义是从工作视图消失；
// 审计链的保留体现为记录仍可按 id 查到，而不是继续出现在默认列表里。
func (s *Store) Apps(ctx context.Context) (map[string]any, error) {
	out := map[string]any{"items": []map[string]any{}}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT o.id::text AS id, o.ontology_key, o.name, o.domain,
                   o.description, o.status,
                   v.version AS latest_version, v.status AS latest_version_status
              FROM ontology.ontologies o
              LEFT JOIN LATERAL (
                  SELECT version, status FROM ontology.ontology_versions
                   WHERE ontology_id = o.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE o.status <> 'DELETED'
             ORDER BY o.ontology_key ASC LIMIT 200`)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		out["items"] = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OntologyVersionLabels 批量把本体版本 id 映射为版本号文本。
//
// 用途：Run 视图要展示「本体 1.0.0」这类可读标识，而 runtime.agent_runs 只存
// ontology_version_id。批量一次取回，避免在列表接口里对每一行 Run 各查一次
// （N+1 会把 10 行/页的列表变成 11 次数据库往返）。
//
// 查不到的 id 直接缺席（调用方按「未知」处理），绝不回落伪造成某个版本号。
func (s *Store) OntologyVersionLabels(ctx context.Context, versionIDs []ids.ID) (map[string]string, error) {
	labels := map[string]string{}
	if len(versionIDs) == 0 {
		return labels, nil
	}
	raw := make([]string, 0, len(versionIDs))
	for _, v := range versionIDs {
		if !v.IsZero() {
			raw = append(raw, v.String())
		}
	}
	if len(raw) == 0 {
		return labels, nil
	}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT id::text, version FROM ontology.ontology_versions
             WHERE id = ANY($1::uuid[])`, raw)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询本体版本号失败")
		}
		defer rows.Close()
		for rows.Next() {
			var id, version string
			if err := rows.Scan(&id, &version); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "读取本体版本号失败")
			}
			labels[id] = version
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return labels, nil
}

// AdminOverview 处理 GET /api/v1/admin/overview 的服务端聚合（第十七篇 §5.3）。
//
// 设计书要求管理概览按四层呈现：系统健康、业务运行、语义健康、治理与质量，
// 且每项指标都能说明统计范围与数据来源（不许只堆一个综合分）。
// 此前这里只返回 counts/platform_users —— 前端按设计书写好的四层结构取不到字段，
// `data.system_health.map` 直接抛 TypeError，整个 /admin 白屏。
// 现补齐四层；所有数字都来自本租户真实表，不做估算、不写死常量。
func (s *Store) AdminOverview(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		counts := map[string]int{}
		specs := []struct {
			key string
			sql string
		}{
			{"roles", `SELECT count(*)::int AS n FROM governance.roles`},
			{"role_bindings", `SELECT count(*)::int AS n FROM governance.role_bindings`},
			{"policy_versions", `SELECT count(*)::int AS n FROM governance.policy_versions`},
			{"agents", `SELECT count(*)::int AS n FROM registry.agents`},
			{"tools", `SELECT count(*)::int AS n FROM registry.tools`},
			{"ontologies", `SELECT count(*)::int AS n FROM ontology.ontologies`},
			{"active_installations", `SELECT count(*)::int AS n FROM registry.plugin_installations WHERE installation_state = 'ACTIVE'`},
			{"evaluation_runs", `SELECT count(*)::int AS n FROM evaluation.runs`},
			{"audit_events", `SELECT count(*)::int AS n FROM audit.audit_events`},
		}
		for _, sp := range specs {
			rows, err := queryMaps(ctx, tx, sp.sql)
			if err != nil {
				return err
			}
			counts[sp.key] = firstInt(rows)
		}
		out["counts"] = counts

		// scalar 是单值计数助手，避免每处都重复 queryMaps + firstInt。
		scalar := func(sql string) (int, error) {
			rows, err := queryMaps(ctx, tx, sql)
			if err != nil {
				return 0, err
			}
			return firstInt(rows), nil
		}

		// ── 业务运行：Run 状态分布（与 /workspace/summary 同一口径，避免两页数字打架）──
		statusRows, err := queryMaps(ctx, tx, `
            SELECT status, count(*)::int AS n FROM runtime.agent_runs GROUP BY status`)
		if err != nil {
			return err
		}
		byStatus := map[string]int{}
		totalRuns := 0
		for _, r := range statusRows {
			st, _ := r["status"].(string)
			n := toInt(r["n"])
			byStatus[st] = n
			totalRuns += n
		}
		// 终态 = COMPLETED / FAILED / CANCELLED / EXPIRED（contracts/01 §1 状态机）
		terminal := byStatus["COMPLETED"] + byStatus["FAILED"] + byStatus["CANCELLED"] + byStatus["EXPIRED"]
		successRate := 0.0
		if terminal > 0 {
			successRate = float64(byStatus["COMPLETED"]) / float64(terminal)
		}
		pendingApprovals, err := scalar(`
            SELECT count(*)::int AS n FROM workflow.approvals WHERE status = 'PENDING'`)
		if err != nil {
			return err
		}
		pendingRecon, err := scalar(`
            SELECT count(*)::int AS n FROM integration.erp_operations
             WHERE status IN ('UNKNOWN','RECONCILING','MANUAL_REVIEW')`)
		if err != nil {
			return err
		}
		out["business"] = map[string]any{
			"total_runs":              totalRuns,
			"success_rate":            successRate,
			"failures":                byStatus["FAILED"],
			"pending_approvals":       pendingApprovals,
			"pending_reconciliations": pendingRecon,
		}

		// ── 语义健康 ──
		ontologyFailures, err := scalar(`
            SELECT count(*)::int AS n FROM ontology.validation_runs
             WHERE syntax_valid IS FALSE OR owl_consistent IS FALSE
                OR shacl_conformant IS FALSE OR business_rules_valid IS FALSE`)
		if err != nil {
			return err
		}
		regressions, err := scalar(`
            SELECT count(*)::int AS n FROM planning.validation_runs WHERE status = 'FAILED'`)
		if err != nil {
			return err
		}
		// 数据映射（/api/v1/data-sources）尚未接入平台：本租户没有任何映射问题记录
		// 可供统计。这里必须返回 JSON null 而不是 0 —— 返回 0 会被读成"已统计且没有
		// 问题"，把"功能未接入"伪装成"通过"，属于必须禁止的静默降级。
		// 前端据此渲染"未接入"，而不是数字 0。
		freshRows, err := queryMaps(ctx, tx, `
            SELECT max(occurred_at) AS last_at FROM audit.audit_events`)
		if err != nil {
			return err
		}
		freshness := "无数据"
		if len(freshRows) > 0 {
			if t, ok := freshRows[0]["last_at"].(time.Time); ok && !t.IsZero() {
				freshness = t.UTC().Format(time.RFC3339)
			}
		}
		out["semantic"] = map[string]any{
			"ontology_validation_failures": ontologyFailures,
			"mapping_issues":               nil,
			"data_freshness":               freshness,
			"semantic_regressions":         regressions,
		}

		// ── 治理与质量 ──
		policyChanges, err := scalar(`
            SELECT count(*)::int AS n FROM governance.policy_versions
             WHERE created_at >= now() - interval '30 days'`)
		if err != nil {
			return err
		}
		// 门禁决策词表：PASSED / PENDING / FAILED（release_mgmt.quality_gate_decisions.decision）
		failedGates, err := scalar(`
            SELECT count(*)::int AS n FROM release_mgmt.quality_gate_decisions
             WHERE upper(decision) IN ('FAILED','REJECTED','BLOCKED')`)
		if err != nil {
			return err
		}
		pendingReleases, err := scalar(`
            SELECT count(*)::int AS n FROM release_mgmt.quality_gate_decisions
             WHERE upper(decision) = 'PENDING'`)
		if err != nil {
			return err
		}
		securityAlerts, err := scalar(`
            SELECT count(*)::int AS n FROM audit.audit_events
             WHERE outcome IN ('DENIED','REJECTED')
               AND occurred_at >= now() - interval '24 hours'`)
		if err != nil {
			return err
		}
		out["governance"] = map[string]any{
			"recent_policy_changes": policyChanges,
			"failed_gates":          failedGates,
			"open_security_alerts":  securityAlerts,
			"pending_releases":      pendingReleases,
		}

		// ── 系统健康 ──
		// 能执行到这里说明 API 与数据库都在正常工作；其余组件由真实指标推导，
		// detail 写清依据，避免"绿灯但没有依据"。
		deadLetters, err := scalar(`
            SELECT count(*)::int AS n FROM execution.task_outbox WHERE status = 'DEAD'`)
		if err != nil {
			return err
		}
		// routable = 启用供应商名下、启用且**已登记模型版本**的端点数量。
		// 缺模型版本档案的端点会被装配期显式排除（不参与路由），因此只看
		// "有没有登记提供方"会把"AI 生成不可用"误报成"健康"。
		providerRows, err := queryMaps(ctx, tx, `
            SELECT (SELECT count(*)::int FROM llm.providers) AS total,
                   (SELECT count(*)::int FROM llm.providers WHERE status = 'ACTIVE') AS active,
                   (SELECT count(*)::int
                      FROM llm.endpoints e
                      JOIN llm.providers p ON p.provider_id = e.provider_id
                     WHERE e.status = 'ACTIVE' AND p.status = 'ACTIVE'
                       AND EXISTS (SELECT 1 FROM llm.model_revisions r
                                    WHERE r.endpoint_id = e.endpoint_id)) AS routable`)
		if err != nil {
			return err
		}
		providerTotal, providerActive, providerRoutable := 0, 0, 0
		if len(providerRows) > 0 {
			providerTotal = toInt(providerRows[0]["total"])
			providerActive = toInt(providerRows[0]["active"])
			providerRoutable = toInt(providerRows[0]["routable"])
		}

		out["system_health"] = []map[string]any{
			{
				"component": "API",
				"state":     "healthy",
				"detail":    "本聚合请求已成功到达 BFF 层",
			},
			{
				"component": "数据库",
				"state":     "healthy",
				"detail":    "租户事务与 RLS 会话建立成功",
			},
			{
				"component": "Worker",
				"state":     workerState(deadLetters),
				"detail":    workerDetail(deadLetters),
			},
			{
				"component": "模型提供方",
				"state":     providerState(providerTotal, providerActive, providerRoutable),
				"detail":    providerDetail(providerTotal, providerActive, providerRoutable),
			},
			{
				"component": "外部连接器",
				"state":     connectorState(pendingRecon),
				"detail":    connectorDetail(totalRuns, pendingRecon),
			},
		}
		out["generated_at"] = time.Now().UTC()
		return nil
	})
	if err != nil {
		return nil, err
	}
	// iam.users / iam.tenants 无 RLS，按平台级统计（仅计数，不泄漏明细）。
	users, err := s.queryMapsNoTenant(ctx, `SELECT count(*)::int AS n FROM iam.users`)
	if err != nil {
		// 早前这里吞掉错误：查询失败时 platform_users 直接不写，前端再 ?? 0，
		// 于是"查不到"被渲染成"0 个用户"。必须上抛，让聚合端点如实失败。
		return nil, errors.Wrap(err, errors.CodeInternal, "统计平台用户数失败")
	}
	out["platform_users"] = firstInt(users)
	return out, nil
}

// workerState 用"执行死信"判定 Worker 健康：死信非零说明投递或消费在持续失败。
func workerState(deadLetters int) string {
	if deadLetters > 0 {
		return "degraded"
	}
	return "healthy"
}

func workerDetail(deadLetters int) string {
	if deadLetters > 0 {
		return "执行死信队列非空（" + strconv.Itoa(deadLetters) + " 条），需人工处置"
	}
	return "执行死信队列为空"
}

// providerState：未登记任何提供方、登记了但全部未启用、或**没有任何可路由端点**，
// 都判为降级。
//
// 此前"未登记"被判为 healthy，依据是"推理由内置 mock 提供方承担"；该内置
// mock 供应商已下线。且只看"有没有登记提供方"也不够：缺模型版本档案的端点会在
// 装配期被排除，AI 生成类能力实际返回不可用 —— 继续报 healthy 就是把"功能不可用"
// 报成"正常"，属静默降级。
func providerState(total, active, routable int) string {
	if total == 0 || active == 0 || routable == 0 {
		return "degraded"
	}
	return "healthy"
}

func providerDetail(total, active, routable int) string {
	if total == 0 {
		return "未登记任何模型提供方：本体/Agent/工具/工作流的 AI 自动生成将返回不可用，请先在「模型与推理」接入真实模型端点"
	}
	if routable == 0 {
		// 这里只能断言"没有可路由端点"；具体被排除的原因（缺模型版本 /
		// 凭据不可解析）以装配期日志为准，不在此臆测。
		return "已登记 " + strconv.Itoa(total) + " 个提供方（启用 " + strconv.Itoa(active) +
			" 个），但没有可路由端点（端点缺模型版本档案，或凭据不可解析）：AI 自动生成当前返回不可用。" +
			"请检查「模型与推理」的接入配置，并查看 API 启动日志中的端点排除原因"
	}
	return "已登记 " + strconv.Itoa(total) + " 个，其中启用 " + strconv.Itoa(active) +
		" 个，可路由端点 " + strconv.Itoa(routable) + " 个"
}

// connectorState：未收敛的外部操作是连接器层最直接的健康信号。
func connectorState(unresolved int) string {
	if unresolved > 0 {
		return "degraded"
	}
	return "healthy"
}

func connectorDetail(totalRuns, unresolved int) string {
	return "本租户 Run " + strconv.Itoa(totalRuns) + " 个，未收敛外部操作 " + strconv.Itoa(unresolved) + " 条"
}

// AdminRoles 返回租户角色清单（按 role_key 升序 cursor 分页 + 可选关键字）。
//
// 分页契约见 contracts/03 §1.4：仅 cursor 式。游标键即排序键 role_key
// （governance.roles 上 UNIQUE (tenant_id, role_key)，唯且有序），空串表示首页。
func (s *Store) AdminRoles(ctx context.Context, limit int, cursor, q string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, role_key, name, description, is_system_role, created_at
              FROM governance.roles
             WHERE ($1::text IS NULL OR role_key > $1)
               AND ($2::text = '' OR role_key ILIKE '%' || $2 || '%' OR name ILIKE '%' || $2 || '%')
             ORDER BY role_key ASC
             LIMIT $3`, nullableStr(from), q, limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["role_key"].(string); ok {
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

// roleBindingCursor 是角色绑定列表的复合游标键。
//
// 排序为 <role_key, login_name, rb.id>：前两列是人工可读的展示序，rb.id 兜底
// 保证唯一（同一角色 + 同一登录名可有多条绑定）。复合键经 JSON 编成单一文本
// 再走 encKey/decKey，避免分隔符转义问题。
type roleBindingCursor struct {
	RoleKey   string `json:"r"`
	LoginName string `json:"l"`
	ID        string `json:"i"`
}

func encRoleBindingKey(c roleBindingCursor) string {
	b, _ := json.Marshal(c)
	return encKey(string(b))
}

func decRoleBindingKey(raw string) (roleBindingCursor, error) {
	k, err := decKey(raw)
	if err != nil {
		return roleBindingCursor{}, err
	}
	if k == "" {
		return roleBindingCursor{}, nil
	}
	var c roleBindingCursor
	if err := json.Unmarshal([]byte(k), &c); err != nil || c.ID == "" {
		return roleBindingCursor{}, errors.E(errors.CodeInvalidArgument, "cursor 不合法")
	}
	return c, nil
}

// AdminRoleBindings 返回角色绑定清单（含主体与范围，cursor 分页 + 可选关键字）。
func (s *Store) AdminRoleBindings(ctx context.Context, limit int, cursor, q string) (*ListResult, error) {
	cur, err := decRoleBindingKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT rb.id::text AS id, r.role_key, r.name AS role_name, rb.scope_type,
                   rb.scope_org_id::text AS scope_org_id, u.login_name, u.display_name,
                   rb.valid_from, rb.valid_until
              FROM governance.role_bindings rb
              JOIN governance.roles r ON r.tenant_id = rb.tenant_id AND r.id = rb.role_id
              JOIN iam.memberships m ON m.tenant_id = rb.tenant_id AND m.id = rb.membership_id
              JOIN iam.users u ON u.id = m.user_id
             WHERE ($1::text IS NULL OR (r.role_key, u.login_name, rb.id) > ($1::text, $2::text, $3::uuid))
               AND ($4::text = '' OR r.role_key ILIKE '%' || $4 || '%' OR r.name ILIKE '%' || $4 || '%'
                    OR u.login_name ILIKE '%' || $4 || '%')
             ORDER BY r.role_key ASC, u.login_name ASC, rb.id ASC
             LIMIT $5`,
			nullableStr(cur.RoleKey), nullableStr(cur.LoginName), nullableStr(cur.ID), q, limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			last := rows[len(rows)-1]
			roleKey, _ := last["role_key"].(string)
			loginName, _ := last["login_name"].(string)
			id, _ := last["id"].(string)
			res.NextCursor = encRoleBindingKey(roleBindingCursor{RoleKey: roleKey, LoginName: loginName, ID: id})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// AdminModelProviders 返回模型供应商与端点（llm schema 为平台级共享定义）。
func (s *Store) AdminModelProviders(ctx context.Context) (map[string]any, error) {
	providers, err := s.queryMapsNoTenant(ctx, `
        SELECT provider_id, display_name, provider_type, status, created_at
          FROM llm.providers ORDER BY provider_id ASC`)
	if err != nil {
		return nil, err
	}
	endpoints, err := s.queryMapsNoTenant(ctx, `
        SELECT e.endpoint_id::text AS endpoint_id, e.provider_id, e.endpoint_name,
               e.deployment_type, e.region_code, e.base_url, e.status, e.created_at,
               coalesce(e.credential_masked, '') AS credential_masked,
               CASE WHEN e.credential_ciphertext IS NOT NULL THEN 'CIPHERTEXT'
                    WHEN e.credential_ref IS NOT NULL THEN 'ENV_REF'
                    ELSE 'NONE' END AS credential_source,
               (e.credential_ciphertext IS NOT NULL OR e.credential_ref IS NOT NULL) AS has_credential,
               coalesce((SELECT string_agg(DISTINCT r.model_id, ',')
                           FROM llm.model_revisions r
                          WHERE r.endpoint_id = e.endpoint_id), '') AS model_ids
          FROM llm.endpoints e ORDER BY e.endpoint_name ASC`)
	if err != nil {
		return nil, err
	}
	if providers == nil {
		providers = []map[string]any{}
	}
	if endpoints == nil {
		endpoints = []map[string]any{}
	}
	return map[string]any{"providers": providers, "endpoints": endpoints}, nil
}

// AdminConnectors 返回当前租户已启用的连接器（按 plugin_key 升序 cursor 分页 + 可选关键字）。
func (s *Store) AdminConnectors(ctx context.Context, limit int, cursor, q string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT p.id::text AS id, p.plugin_key, p.name, p.plugin_kind,
                   i.installation_state, i.installed_at, i.updated_at
              FROM registry.plugin_installations i
              JOIN registry.plugins p ON p.id = i.plugin_id
             WHERE i.installation_state = 'ACTIVE'
               AND ($1::text IS NULL OR p.plugin_key > $1)
               AND ($2::text = '' OR p.plugin_key ILIKE '%' || $2 || '%' OR p.name ILIKE '%' || $2 || '%')
             ORDER BY p.plugin_key ASC
             LIMIT $3`, nullableStr(from), q, limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["plugin_key"].(string); ok {
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

// GetActiveConnector 按 id 返回当前租户已启用（ACTIVE）的连接器详情。
// 与 AdminConnectors 同一真源：registry.plugin_installations JOIN registry.plugins。
// 不存在或未启用 → CodeNotFound（"不存在"与"未启用"对调用方是同一件事）。
func (s *Store) GetActiveConnector(ctx context.Context, connectorID string) (map[string]any, error) {
	if connectorID == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "连接器 id 必填")
	}
	if _, err := ids.Parse(connectorID); err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, "连接器 id 不合法")
	}
	var out map[string]any
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT p.id::text AS id, p.plugin_key, p.name, p.plugin_kind,
                   i.installation_state, i.installed_at
              FROM registry.plugin_installations i
              JOIN registry.plugins p ON p.id = i.plugin_id
             WHERE p.id = $1 AND i.installation_state = 'ACTIVE'
             LIMIT 1`, connectorID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.Ef(errors.CodeNotFound, "连接器 %s 不存在或未启用", connectorID)
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OpsAlerts 返回运维告警视图：死信、失败运行、需人工复核运行、未知外部操作。
func (s *Store) OpsAlerts(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		dead, err := queryMaps(ctx, tx, `
            SELECT count(*)::int AS n FROM execution.task_outbox WHERE status = 'DEAD'`)
		if err != nil {
			return err
		}
		failed, err := queryMaps(ctx, tx, `
            SELECT count(*)::int AS n FROM runtime.agent_runs WHERE status IN ('FAILED','MANUAL_REVIEW')`)
		if err != nil {
			return err
		}
		unknown, err := queryMaps(ctx, tx, `
            SELECT count(*)::int AS n FROM runtime.tool_calls WHERE status IN ('UNKNOWN','MANUAL_REVIEW')`)
		if err != nil {
			return err
		}
		recon, err := queryMaps(ctx, tx, `
            SELECT count(*)::int AS n FROM integration.erp_operations WHERE status IN ('UNKNOWN','RECONCILING','MANUAL_REVIEW')`)
		if err != nil {
			return err
		}
		out["dead_letter_events"] = firstInt(dead)
		out["failed_or_manual_runs"] = firstInt(failed)
		out["unknown_tool_calls"] = firstInt(unknown)
		out["unresolved_external_operations"] = firstInt(recon)
		out["generated_at"] = time.Now().UTC()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// firstInt 读取单行计数结果的 n 列。
func firstInt(rows []map[string]any) int {
	if len(rows) == 0 {
		return 0
	}
	return toInt(rows[0]["n"])
}

// toInt 把 pgx 可能返回的整数类型归一化为 int。
func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}
