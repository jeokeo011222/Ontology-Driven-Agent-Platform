package seed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
)

// Applier 把数据集写入数据库。
//
// 写入分两阶段：
//  1. 平台级（iam.tenants / iam.users / llm.* —— 无 RLS），用连接池直接执行；
//  2. 租户级（其余全部），在带 app.tenant_id 的租户事务内执行，受 RLS FORCE 约束。
//
// 这个划分不是风格问题：租户级表都启用了 FORCE ROW LEVEL SECURITY，
// 若不设置租户上下文就直接写，会被策略拒绝（或更糟——静默写错租户）。
type Applier struct {
	Pool   *pgxpool.Pool
	Logger *slog.Logger

	// Replace 为 true 时，先清除"本数据集声明过的行"再写入。
	//
	// 适用场景：演示数据仍在迭代，而版本行一旦落入终态（PUBLISHED 等）就不可变 ——
	// 此时改数据文件重跑会在版本摘要那一关报错。--replace 提供一条显式的重建路径，
	// 而不是让人手工拼 SQL 清库（那才是真正容易出错的操作）。
	//
	// 注意作用域：只删除数据文件中声明过的 ID，不做表级 TRUNCATE，
	// 因此不会波及同租户下的其他数据。
	Replace bool
}

// Apply 幂等地应用整个数据集。
func (a *Applier) Apply(ctx context.Context, ds *Dataset) error {
	if a.Pool == nil {
		return fmt.Errorf("seed: 数据库连接池未就绪")
	}
	if a.Replace {
		if err := a.purgePlatform(ctx, ds); err != nil {
			return fmt.Errorf("seed: 平台级清理失败: %w", err)
		}
	}
	if err := a.applyPlatform(ctx, ds); err != nil {
		return fmt.Errorf("seed: 平台级写入失败: %w", err)
	}

	tenantID := ids.MustParse(ds.Tenant.ID)
	subjectID := ids.MustParse(ds.Tenant.Users[0].ID)
	tctx := tenantctx.WithIdentity(ctx, tenantctx.Identity{TenantID: tenantID, SubjectID: subjectID})

	// 租户事务走平台层唯一实现 pgxutil.TenantTx。
	// 此前这里直接调用 api/middleware.TenantTx，使领域包反向依赖 HTTP 传输层
	// （违反第十六篇 §4.3 依赖方向，由 tests/unit 架构测试捕获）。
	if err := pgxutil.TenantTx(tctx, a.Pool, tenantID, func(tx pgx.Tx) error {
		if a.Replace {
			if err := a.purgeTenant(ctx, tx, ds); err != nil {
				return err
			}
		}
		return a.applyTenant(ctx, tx, ds)
	}); err != nil {
		return fmt.Errorf("seed: 租户级写入失败: %w", err)
	}
	return nil
}

// execer 抽象 pgx.Tx 与 *pgxpool.Pool 的公共 Exec 能力（两者签名一致）。
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// deleteScoped 按键集合删除（空集合直接跳过，避免发出无意义的 DELETE）。
func deleteScoped(ctx context.Context, ex execer, table, col string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := ex.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s = ANY($1)", table, col), ids); err != nil {
		return fmt.Errorf("清理 %s(%s) 失败: %w", table, col, err)
	}
	return nil
}

// purgePlatform 清理平台级（llm.*）中本数据集声明过的行。
func (a *Applier) purgePlatform(ctx context.Context, ds *Dataset) error {
	var providerIDs, endpointIDs []string
	for _, p := range ds.Providers {
		providerIDs = append(providerIDs, p.ID)
		for _, e := range p.Endpoints {
			endpointIDs = append(endpointIDs, e.ID)
		}
	}
	for _, s := range []struct {
		table, col string
		ids        []string
	}{
		{"llm.model_revisions", "endpoint_id", endpointIDs},
		{"llm.endpoints", "provider_id", providerIDs},
		{"llm.providers", "provider_id", providerIDs},
	} {
		if err := deleteScoped(ctx, a.Pool, s.table, s.col, s.ids); err != nil {
			return err
		}
	}
	// 能力档案是内容寻址的共享资源：只有当没有任何模型版本再引用它时才清理，
	// 否则会连带破坏其他供应商/模型的能力绑定。
	if _, err := a.Pool.Exec(ctx, `
        DELETE FROM llm.capability_profiles cp
         WHERE NOT EXISTS (
               SELECT 1 FROM llm.model_revisions mr
                WHERE mr.capability_digest = cp.capability_digest)`); err != nil {
		return fmt.Errorf("清理孤立能力档案失败: %w", err)
	}
	return nil
}

// purgeTenant 按外键依赖的逆序清理租户级行。
//
// 顺序不可随意调整：子表（版本、构件、运行）必须先于父表（插件、本体、组织）删除，
// 否则会被外键挡住。这里按"声明过的 ID"限定范围，不触及数据集之外的数据。
func (a *Applier) purgeTenant(ctx context.Context, tx pgx.Tx, ds *Dataset) error {
	var (
		orgIDs, membershipIDs, roleIDs, bindingIDs []string
		pluginIDs, pluginVerIDs                    []string
		agentIDs, agentVerIDs                      []string
		toolIDs, toolVerIDs                        []string
		workflowIDs, workflowVerIDs                []string
		ontIDs, ontVerIDs                          []string
		planIDs, runIDs                            []string
	)
	for _, o := range ds.Tenant.Orgs {
		orgIDs = append(orgIDs, o.ID)
	}
	for _, m := range ds.Tenant.Memberships {
		membershipIDs = append(membershipIDs, m.ID)
	}
	for _, r := range ds.Tenant.Roles {
		roleIDs = append(roleIDs, r.ID)
	}
	for _, b := range ds.Tenant.Bindings {
		bindingIDs = append(bindingIDs, b.ID)
	}
	for _, ag := range ds.Agents {
		agentIDs = append(agentIDs, ag.ID)
		agentVerIDs = append(agentVerIDs, ag.Version.ID)
		pluginIDs = append(pluginIDs, ag.Plugin.ID)
		pluginVerIDs = append(pluginVerIDs, ag.Plugin.VersionID)
	}
	for _, tl := range ds.Tools {
		toolIDs = append(toolIDs, tl.ID)
		toolVerIDs = append(toolVerIDs, tl.Version.ID)
		pluginIDs = append(pluginIDs, tl.Plugin.ID)
		pluginVerIDs = append(pluginVerIDs, tl.Plugin.VersionID)
	}
	for _, wf := range ds.Workflows {
		workflowIDs = append(workflowIDs, wf.ID)
		workflowVerIDs = append(workflowVerIDs, wf.Version.ID)
		pluginIDs = append(pluginIDs, wf.Plugin.ID)
		pluginVerIDs = append(pluginVerIDs, wf.Plugin.VersionID)
	}
	for _, o := range ds.Ontologies {
		ontIDs = append(ontIDs, o.ID)
		for _, v := range o.Versions {
			ontVerIDs = append(ontVerIDs, v.ID)
		}
	}
	for _, p := range ds.Plans {
		planIDs = append(planIDs, p.ID)
	}
	for _, r := range ds.Runs {
		runIDs = append(runIDs, r.ID)
	}

	steps := []struct {
		table, col string
		ids        []string
	}{
		{"runtime.agent_runs", "id", runIDs},
		{"planning.plan_revisions", "plan_id", planIDs},
		{"planning.plans", "plan_id", planIDs},
		{"registry.agent_versions", "id", agentVerIDs},
		{"registry.agents", "id", agentIDs},
		{"registry.tool_versions", "id", toolVerIDs},
		{"registry.tools", "id", toolIDs},
		{"registry.workflow_versions", "id", workflowVerIDs},
		{"registry.workflows", "id", workflowIDs},
		{"ontology.publications", "ontology_version_id", ontVerIDs},
		{"ontology.validation_runs", "ontology_version_id", ontVerIDs},
		{"ontology.ontology_artifacts", "ontology_version_id", ontVerIDs},
		{"ontology.ontology_versions", "id", ontVerIDs},
		{"ontology.ontologies", "id", ontIDs},
		{"registry.plugin_versions", "id", pluginVerIDs},
		{"registry.plugins", "id", pluginIDs},
		{"governance.role_bindings", "id", bindingIDs},
		{"iam.memberships", "id", membershipIDs},
		{"governance.role_permissions", "role_id", roleIDs},
		{"governance.roles", "id", roleIDs},
		{"iam.org_closure", "descendant_id", orgIDs},
		{"iam.organizations", "id", orgIDs},
	}
	for _, s := range steps {
		if err := deleteScoped(ctx, tx, s.table, s.col, s.ids); err != nil {
			return err
		}
	}
	return nil
}

// DefaultDevLoginPassword 是种子为演示账号写入的初始口令。
//
// 仅供本地演示：该口令随源码公开，任何生产部署都不得依赖它（生产账号请走
// 注册/管理员创建流程自行设置口令）。可用环境变量 ODAP_DEV_LOGIN_PASSWORD 覆盖，
// 供多人共享的演示环境各自设置不同口令。
const DefaultDevLoginPassword = "odap-demo-2026"

// devLoginPassword 返回种子使用的演示口令（环境变量优先）。
func devLoginPassword() string {
	if v := os.Getenv("ODAP_DEV_LOGIN_PASSWORD"); v != "" {
		return v
	}
	return DefaultDevLoginPassword
}

// applyPlatform 写入平台级（无 RLS）行：租户、用户、模型供应商。
func (a *Applier) applyPlatform(ctx context.Context, ds *Dataset) error {
	// 租户：用 DO UPDATE 归一 key/name，保证与数据集定义一致
	// （即使库中已存在同 ID 但不同 key 的租户，例如早期手工创建的租户）。
	if _, err := a.Pool.Exec(ctx, `
        INSERT INTO iam.tenants (id, tenant_key, name, status)
        VALUES ($1, $2, $3, 'ACTIVE')
        ON CONFLICT (id) DO UPDATE
           SET tenant_key = EXCLUDED.tenant_key, name = EXCLUDED.name`,
		ds.Tenant.ID, ds.Tenant.Key, ds.Tenant.Name); err != nil {
		return fmt.Errorf("写入租户失败: %w", err)
	}

	for _, u := range ds.Tenant.Users {
		if _, err := a.Pool.Exec(ctx, `
            INSERT INTO iam.users (id, login_name, display_name, email)
            VALUES ($1, $2, $3, NULLIF($4, ''))
            ON CONFLICT (id) DO NOTHING`,
			u.ID, u.LoginName, u.DisplayName, u.Email); err != nil {
			// login_name 有 UNIQUE 约束：若库中已存在同名但不同 ID 的残留用户，
			// 这里会失败。这是有意为之——宁可报错，也不要注入一个 created_by
			// 指向不存在用户的"半套数据"。清理方式见报错信息。
			return fmt.Errorf("写入用户 %s 失败（若提示 login_name 冲突，"+
				"说明库中存在同名残留用户，需先清理）: %w", u.LoginName, err)
		}
	}

	// 演示账号的本地口令：iam.credentials 与 iam.users 是 1:1（user_id UNIQUE）。
	//
	// 用 DO NOTHING 而非 DO UPDATE：口令一旦被改过（注册/管理员重置），重跑种子
	// 不应把它悄悄改回去 —— 数据集的职责是"补齐缺失"，不是"覆盖用户状态"。
	// 代价是库中若已存在错误口令也不会被修正，此时需按报错提示手工清理该行。
	hash, herr := bcrypt.GenerateFromPassword([]byte(devLoginPassword()), bcrypt.DefaultCost)
	if herr != nil {
		return fmt.Errorf("生成演示口令哈希失败: %w", herr)
	}
	hashStr := string(hash)
	for _, u := range ds.Tenant.Users {
		if _, err := a.Pool.Exec(ctx, `
            INSERT INTO iam.credentials (id, user_id, password_hash, password_salt)
            VALUES ($1, $2, $3, $3)
            ON CONFLICT (user_id) DO NOTHING`,
			ids.New().String(), u.ID, hashStr); err != nil {
			return fmt.Errorf("写入用户 %s 的本地凭据失败: %w", u.LoginName, err)
		}
	}

	// 模型供应商为平台级共享资源（llm schema 无 tenant_id）。
	for _, p := range ds.Providers {
		if err := a.applyProvider(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// applyProvider 写入供应商 → 端点 → 能力档案 → 模型版本。
//
// llm.* 是平台级配置（非"已发布版本"），因此用 DO UPDATE 全字段收敛：
// 只更新少数字段会让 providers.json 里的其余改动静默失效。
// 写入顺序受外键约束：能力档案必须先于模型版本（见 applyCapabilityProfile）。
func (a *Applier) applyProvider(ctx context.Context, p Provider) error {
	if _, err := a.Pool.Exec(ctx, `
        INSERT INTO llm.providers (provider_id, display_name, provider_type, status)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (provider_id) DO UPDATE
           SET display_name = EXCLUDED.display_name,
               provider_type = EXCLUDED.provider_type,
               status = EXCLUDED.status`,
		p.ID, p.DisplayName, p.Type, p.Status); err != nil {
		return fmt.Errorf("写入模型供应商 %s 失败: %w", p.ID, err)
	}
	for _, e := range p.Endpoints {
		if _, err := a.Pool.Exec(ctx, `
            INSERT INTO llm.endpoints
                (endpoint_id, provider_id, endpoint_name, deployment_type, region_code,
                 credential_ref, base_url, status)
            VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), $8)
            ON CONFLICT (endpoint_id) DO UPDATE
               SET provider_id = EXCLUDED.provider_id,
                   endpoint_name = EXCLUDED.endpoint_name,
                   deployment_type = EXCLUDED.deployment_type,
                   region_code = EXCLUDED.region_code,
                   credential_ref = EXCLUDED.credential_ref,
                   base_url = EXCLUDED.base_url,
                   status = EXCLUDED.status`,
			e.ID, p.ID, e.Name, e.DeploymentType, e.RegionCode, e.CredentialRef,
			e.BaseURL, e.Status); err != nil {
			return fmt.Errorf("写入端点 %s 失败: %w", e.ID, err)
		}
		for _, m := range e.Models {
			caps, err := decodeCapabilities(m.Capabilities)
			if err != nil {
				return fmt.Errorf("模型版本 %s 的能力声明非法: %w", m.ID, err)
			}
			capDigest := caps.digest()
			if capDigest == "" {
				return fmt.Errorf("seed: 计算模型版本 %s 的能力摘要失败", m.ID)
			}
			// 能力档案必须先于模型版本写入（外键依赖）。
			if err := a.applyCapabilityProfile(ctx, capDigest, m); err != nil {
				return err
			}
			if _, err := a.Pool.Exec(ctx, `
                INSERT INTO llm.model_revisions
                    (model_revision_id, endpoint_id, model_id, revision_label,
                     capability_digest, pricing_profile_id, status)
                VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7)
                ON CONFLICT (model_revision_id) DO UPDATE
                   SET endpoint_id = EXCLUDED.endpoint_id,
                       model_id = EXCLUDED.model_id,
                       revision_label = EXCLUDED.revision_label,
                       capability_digest = EXCLUDED.capability_digest,
                       pricing_profile_id = EXCLUDED.pricing_profile_id,
                       status = EXCLUDED.status`,
				m.ID, e.ID, m.ModelID, m.RevisionLabel, capDigest,
				m.PricingProfileID, m.Status); err != nil {
				return fmt.Errorf("写入模型版本 %s 失败: %w", m.ID, err)
			}
		}
	}
	return nil
}

// applyCapabilityProfile 写入内容寻址的能力档案（llm.capability_profiles）。
//
// 主键即能力内容的规范化 SHA-256，因此"摘要 == 内容"是结构保证，
// 不存在手工填写哈希导致漂移的可能；相同能力的模型版本自动复用同一条档案。
// 写入前必须已由 Validate 保证"同内容不同声明"的歧义被拒绝。
func (a *Applier) applyCapabilityProfile(ctx context.Context, digest string, m ModelRevision) error {
	status := "UNVERIFIED"
	if m.CapabilityVerified {
		status = "VERIFIED"
	}
	if _, err := a.Pool.Exec(ctx, `
        INSERT INTO llm.capability_profiles
            (capability_digest, capabilities, revision, status, verified_at)
        VALUES ($1, $2::jsonb, $3, $4,
                CASE WHEN $4 = 'VERIFIED' THEN now() ELSE NULL END)
        ON CONFLICT (capability_digest) DO UPDATE
           SET revision = EXCLUDED.revision,
               status = EXCLUDED.status,
               verified_at = COALESCE(llm.capability_profiles.verified_at, EXCLUDED.verified_at)`,
		digest, string(m.Capabilities), m.CapabilityRevision, status); err != nil {
		return fmt.Errorf("写入能力档案 %s… 失败: %w", short8(digest), err)
	}
	return nil
}

// applyTenant 在租户事务内写入全部租户级行。
func (a *Applier) applyTenant(ctx context.Context, tx pgx.Tx, ds *Dataset) error {
	t := ds.Tenant

	// ---- 组织与成员 ----
	for _, o := range t.Orgs {
		if _, err := tx.Exec(ctx, `
            INSERT INTO iam.organizations (id, tenant_id, org_code, name, org_type)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			o.ID, o.OrgCode, o.Name, o.OrgType); err != nil {
			return fmt.Errorf("写入组织 %s 失败: %w", o.ID, err)
		}
		// 自反闭包（depth=0）：组织层级闭包至少包含自身。
		if _, err := tx.Exec(ctx, `
            INSERT INTO iam.org_closure (tenant_id, ancestor_id, descendant_id, depth)
            VALUES (app.current_tenant_id(), $1, $1, 0)
            ON CONFLICT DO NOTHING`, o.ID); err != nil {
			return fmt.Errorf("写入组织闭包 %s 失败: %w", o.ID, err)
		}
	}
	for _, m := range t.Memberships {
		if _, err := tx.Exec(ctx, `
            INSERT INTO iam.memberships (id, tenant_id, user_id, org_id)
            VALUES ($1, app.current_tenant_id(), $2, $3)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			m.ID, m.UserID, m.OrgID); err != nil {
			return fmt.Errorf("写入成员关系 %s 失败: %w", m.ID, err)
		}
	}

	// ---- 角色、权限与绑定 ----
	for _, r := range t.Roles {
		if _, err := tx.Exec(ctx, `
            INSERT INTO governance.roles (id, tenant_id, role_key, name, description, is_system_role)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			r.ID, r.RoleKey, r.Name, r.Description, r.SystemRole); err != nil {
			return fmt.Errorf("写入角色 %s 失败: %w", r.ID, err)
		}
		if len(r.Permissions) > 0 {
			if _, err := tx.Exec(ctx, `
                INSERT INTO governance.role_permissions (tenant_id, role_id, permission_id)
                SELECT app.current_tenant_id(), $1, p.id FROM governance.permissions p
                 WHERE p.permission_key = ANY($2)
                ON CONFLICT DO NOTHING`, r.ID, r.Permissions); err != nil {
				return fmt.Errorf("写入角色权限 %s 失败: %w", r.ID, err)
			}
		}
	}
	for _, b := range t.Bindings {
		grantedBy := b.GrantedBy
		if grantedBy == "" {
			grantedBy = t.Users[0].ID
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO governance.role_bindings
                (id, tenant_id, membership_id, role_id, scope_type, granted_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)
            ON CONFLICT DO NOTHING`,
			b.ID, b.MembershipID, b.RoleID, b.ScopeType, grantedBy); err != nil {
			return fmt.Errorf("写入角色绑定 %s 失败: %w", b.ID, err)
		}
	}

	// ---- Registry：Agent / Tool（含各自的插件与插件版本）----
	for _, ag := range ds.Agents {
		if err := insertPlugin(ctx, tx, ag.Plugin, t.Users[0].ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.agents (id, tenant_id, plugin_id, agent_key, domain, description, is_builtin)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			ag.ID, ag.Plugin.ID, ag.Key, ag.Domain, ag.Description, ag.Builtin); err != nil {
			return fmt.Errorf("写入 Agent %s 失败: %w", ag.Key, err)
		}
		content, _ := json.Marshal(map[string]any{
			"input": ag.Version.InputSchema, "output": ag.Version.OutputSchema,
			"prompt_template": ag.Version.PromptTemplate, "config": ag.Version.Config,
			"dependency_manifest": ag.Version.DependencyManifest,
		})
		digest := digestBytes(content)
		if err := execVersionUpsert(ctx, tx, `
            INSERT INTO registry.agent_versions AS av
                (id, tenant_id, agent_id, plugin_version_id, version, config, prompt_template,
                 input_schema, output_schema, dependency_manifest, content_digest, status, published_at)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4,
                    COALESCE(NULLIF($5,'')::jsonb, '{}'::jsonb),
                    COALESCE(NULLIF($6,'')::jsonb, '{}'::jsonb),
                    $7::jsonb, $8::jsonb,
                    COALESCE(NULLIF($9,'')::jsonb, '{}'::jsonb),
                    $10, $11,
                    CASE WHEN $11 = 'PUBLISHED' THEN now() ELSE NULL END)
            ON CONFLICT (tenant_id, agent_id, version) DO UPDATE
               SET config = EXCLUDED.config,
                   prompt_template = EXCLUDED.prompt_template,
                   input_schema = EXCLUDED.input_schema,
                   output_schema = EXCLUDED.output_schema,
                   dependency_manifest = EXCLUDED.dependency_manifest,
                   content_digest = EXCLUDED.content_digest,
                   status = EXCLUDED.status,
                   published_at = EXCLUDED.published_at
             WHERE av.status NOT IN ('PUBLISHED', 'DEPRECATED', 'DISABLED')`,
			[]any{ag.Version.ID, ag.ID, ag.Plugin.VersionID, ag.Version.Version,
				string(ag.Version.Config), string(ag.Version.PromptTemplate),
				string(ag.Version.InputSchema), string(ag.Version.OutputSchema),
				string(ag.Version.DependencyManifest), digest, ag.Version.Status},
			`SELECT content_digest FROM registry.agent_versions
              WHERE tenant_id = app.current_tenant_id() AND agent_id = $1 AND version = $2`,
			[]any{ag.ID, ag.Version.Version},
			digest, fmt.Sprintf("Agent 版本 %s@%s", ag.Key, ag.Version.Version)); err != nil {
			return err
		}
	}
	for _, tl := range ds.Tools {
		if err := insertPlugin(ctx, tx, tl.Plugin, t.Users[0].ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.tools (id, tenant_id, plugin_id, tool_key, description, is_builtin)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			tl.ID, tl.Plugin.ID, tl.Key, tl.Description, tl.Builtin); err != nil {
			return fmt.Errorf("写入工具 %s 失败: %w", tl.Key, err)
		}
		content, _ := json.Marshal(map[string]any{
			"input": tl.Version.InputSchema, "output": tl.Version.OutputSchema,
			"capabilities": tl.Version.Capabilities, "effect_class": tl.Version.EffectClass,
			"execution_config": tl.Version.ExecutionConfig,
		})
		digest := digestBytes(content)
		if err := execVersionUpsert(ctx, tx, `
            INSERT INTO registry.tool_versions AS tv
                (id, tenant_id, tool_id, plugin_version_id, version, input_schema, output_schema,
                 capabilities, effect_class, execution_config, content_digest, status)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6::jsonb,
                    $7, $8, COALESCE(NULLIF($9,'')::jsonb, '{}'::jsonb), $10, $11)
            ON CONFLICT (tenant_id, tool_id, version) DO UPDATE
               SET input_schema = EXCLUDED.input_schema,
                   output_schema = EXCLUDED.output_schema,
                   capabilities = EXCLUDED.capabilities,
                   effect_class = EXCLUDED.effect_class,
                   execution_config = EXCLUDED.execution_config,
                   content_digest = EXCLUDED.content_digest,
                   status = EXCLUDED.status
             WHERE tv.status NOT IN ('PUBLISHED', 'DEPRECATED', 'DISABLED')`,
			[]any{tl.Version.ID, tl.ID, tl.Plugin.VersionID, tl.Version.Version,
				string(tl.Version.InputSchema), string(tl.Version.OutputSchema),
				tl.Version.Capabilities, tl.Version.EffectClass,
				string(tl.Version.ExecutionConfig), digest, tl.Version.Status},
			`SELECT content_digest FROM registry.tool_versions
              WHERE tenant_id = app.current_tenant_id() AND tool_id = $1 AND version = $2`,
			[]any{tl.ID, tl.Version.Version},
			digest, fmt.Sprintf("工具版本 %s@%s", tl.Key, tl.Version.Version)); err != nil {
			return err
		}
	}

	// ---- 工作流（含插件与插件版本）----
	// 与 Agent / Tool 完全同构：工作流本身是插件式资产，版本行由 000018 的触发器
	// 保证"已发布即不可变"，因此同样走 execVersionUpsert 做内容一致性校验。
	for _, wf := range ds.Workflows {
		if err := insertPlugin(ctx, tx, wf.Plugin, t.Users[0].ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO registry.workflows
                (id, tenant_id, plugin_id, workflow_key, name, description, is_builtin)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			wf.ID, wf.Plugin.ID, wf.Key, wf.Name, wf.Description, wf.Builtin); err != nil {
			return fmt.Errorf("写入工作流 %s 失败: %w", wf.Key, err)
		}
		content, _ := json.Marshal(map[string]any{
			"definition": wf.Version.Definition,
			"input":      wf.Version.InputSchema,
			"output":     wf.Version.OutputSchema,
		})
		digest := digestBytes(content)
		if err := execVersionUpsert(ctx, tx, `
            INSERT INTO registry.workflow_versions AS wv
                (id, tenant_id, workflow_id, plugin_version_id, version, definition,
                 input_schema, output_schema, content_digest, status, published_at)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb,
                    COALESCE(NULLIF($6,'')::jsonb, '{}'::jsonb),
                    COALESCE(NULLIF($7,'')::jsonb, '{}'::jsonb),
                    $8, $9,
                    CASE WHEN $9 = 'PUBLISHED' THEN now() ELSE NULL END)
            ON CONFLICT (tenant_id, workflow_id, version) DO UPDATE
               SET definition = EXCLUDED.definition,
                   input_schema = EXCLUDED.input_schema,
                   output_schema = EXCLUDED.output_schema,
                   content_digest = EXCLUDED.content_digest,
                   status = EXCLUDED.status,
                   published_at = EXCLUDED.published_at
             WHERE wv.status NOT IN ('PUBLISHED', 'DEPRECATED', 'DISABLED')`,
			[]any{wf.Version.ID, wf.ID, wf.Plugin.VersionID, wf.Version.Version,
				string(wf.Version.Definition), string(wf.Version.InputSchema),
				string(wf.Version.OutputSchema), digest, wf.Version.Status},
			`SELECT content_digest FROM registry.workflow_versions
              WHERE tenant_id = app.current_tenant_id() AND workflow_id = $1 AND version = $2`,
			[]any{wf.ID, wf.Version.Version},
			digest, fmt.Sprintf("工作流版本 %s@%s", wf.Key, wf.Version.Version)); err != nil {
			return err
		}
	}

	// ---- 本体 ----
	for _, o := range ds.Ontologies {
		if _, err := tx.Exec(ctx, `
            INSERT INTO ontology.ontologies
                (id, tenant_id, ontology_key, name, domain, granularity, description, owner_org_id)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, NULLIF($7,'')::uuid)
            ON CONFLICT (tenant_id, id) DO NOTHING`,
			o.ID, o.Key, o.Name, o.Domain, o.Granularity, o.Description, o.OwnerOrgID); err != nil {
			return fmt.Errorf("写入本体 %s 失败: %w", o.Key, err)
		}
		for _, v := range o.Versions {
			// 摘要必须覆盖构件内容：否则改了 OWL/SHACL 文件内容而 manifest 未动时，
			// 版本行被判为"内容一致"而静默跳过，构件保持旧内容却报告成功。
			var buf bytes.Buffer
			buf.Write(v.Manifest)
			for _, a := range v.Artifacts {
				buf.WriteString(a.ID)
				buf.WriteString(a.Type)
				buf.WriteString(a.LogicalPath)
				buf.WriteString(a.MediaType)
				buf.WriteString(a.StorageURI)
				buf.WriteString(a.Content)
			}
			digest := digestBytes(buf.Bytes())
			if err := execVersionUpsert(ctx, tx, `
                INSERT INTO ontology.ontology_versions AS ov
                    (id, tenant_id, ontology_id, version, status, manifest, content_digest, created_by, published_at)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6, $7,
                        CASE WHEN $4 = 'PUBLISHED' THEN now() ELSE NULL END)
                ON CONFLICT (tenant_id, ontology_id, version) DO UPDATE
                   SET status = EXCLUDED.status,
                       manifest = EXCLUDED.manifest,
                       content_digest = EXCLUDED.content_digest,
                       published_at = EXCLUDED.published_at
                 WHERE ov.status NOT IN ('PUBLISHED', 'DEPRECATED', 'DISABLED', 'REJECTED')`,
				[]any{v.ID, o.ID, v.Version, v.Status, string(v.Manifest), digest, t.Users[0].ID},
				`SELECT content_digest FROM ontology.ontology_versions
                  WHERE tenant_id = app.current_tenant_id() AND ontology_id = $1 AND version = $2`,
				[]any{o.ID, v.Version},
				digest, fmt.Sprintf("本体版本 %s@%s", o.Key, v.Version)); err != nil {
				return err
			}
			for _, a := range v.Artifacts {
				contentDigest := digestBytes([]byte(a.Content))
				size := int64(len(a.Content))
				// 构件内容只随"非终态"的父版本一起收敛；父版本已发布时，
				// 内容分叉已在版本摘要那一关报错，这里不再改动（保持已发布版本不可变）。
				if _, err := tx.Exec(ctx, `
                    INSERT INTO ontology.ontology_artifacts AS art
                        (id, tenant_id, ontology_version_id, artifact_type, logical_path,
                         media_type, storage_uri, content_digest, size_bytes)
                    VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7, $8)
                    ON CONFLICT (tenant_id, ontology_version_id, logical_path) DO UPDATE
                       SET artifact_type = EXCLUDED.artifact_type,
                           media_type = EXCLUDED.media_type,
                           storage_uri = EXCLUDED.storage_uri,
                           content_digest = EXCLUDED.content_digest,
                           size_bytes = EXCLUDED.size_bytes
                     WHERE NOT EXISTS (
                           SELECT 1 FROM ontology.ontology_versions ov
                            WHERE ov.tenant_id = app.current_tenant_id()
                              AND ov.id = art.ontology_version_id
                              AND ov.status IN ('PUBLISHED','DEPRECATED','DISABLED','REJECTED'))`,
					a.ID, v.ID, a.Type, a.LogicalPath, a.MediaType, a.StorageURI,
					contentDigest, size); err != nil {
					return fmt.Errorf("写入本体构件 %s 失败: %w", a.LogicalPath, err)
				}
			}
			for _, vr := range v.ValidationRuns {
				if _, err := tx.Exec(ctx, `
                    INSERT INTO ontology.validation_runs
                        (id, tenant_id, ontology_version_id, validator_version, input_digest, status,
                         syntax_valid, owl_consistent, shacl_conformant, business_rules_valid)
                    VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7, $8, $9)
                    ON CONFLICT (tenant_id, id) DO NOTHING`,
					vr.ID, v.ID, vr.ValidatorVersion, digestBytes([]byte(vr.ID)), vr.Status,
					vr.SyntaxValid, vr.OWLConsistent, vr.SHACLConformant, vr.BusinessRulesValid); err != nil {
					return fmt.Errorf("写入本体校验记录 %s 失败: %w", vr.ID, err)
				}
			}
			if v.Publication != nil {
				if _, err := tx.Exec(ctx, `
                    INSERT INTO ontology.publications
                        (id, tenant_id, ontology_id, ontology_version_id, environment, published_by)
                    VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)
                    ON CONFLICT DO NOTHING`,
					v.Publication.ID, o.ID, v.ID, v.Publication.Environment, t.Users[0].ID); err != nil {
					return fmt.Errorf("写入本体发布 %s 失败: %w", o.Key, err)
				}
			}
		}
	}

	// ---- 计划（含修订）----
	for _, p := range ds.Plans {
		if _, err := tx.Exec(ctx, `
            INSERT INTO planning.plans (plan_id, tenant_id, intent_id, domain, status, created_by, version)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, 'seed', $5)
            ON CONFLICT (plan_id) DO NOTHING`,
			p.ID, p.IntentID, p.Domain, p.Status, p.Version); err != nil {
			return fmt.Errorf("写入计划 %s 失败: %w", p.ID, err)
		}
		rev := p.Revision
		if rev.PolicyVersion == "" {
			rev.PolicyVersion = "v1"
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO planning.plan_revisions
                (plan_revision_id, plan_id, revision_number, plan_ir, plan_digest,
                 ontology_digest, action_contract_digest, tool_binding_digest,
                 policy_version, validation_status)
            VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10)
            ON CONFLICT (plan_id, revision_number) DO NOTHING`,
			rev.ID, p.ID, rev.RevisionNumber, string(rev.PlanIR),
			digestBytes(rev.PlanIR), digestBytes([]byte(p.ID+"ontology")),
			digestBytes([]byte(p.ID+"action")), digestBytes([]byte(p.ID+"tool")),
			rev.PolicyVersion, rev.ValidationStatus); err != nil {
			return fmt.Errorf("写入计划修订 %s 失败: %w", p.ID, err)
		}
	}

	// ---- 演示运行 ----
	for _, r := range ds.Runs {
		priority, maxAttempts := r.Priority, r.MaxAttempts
		if priority <= 0 {
			priority = 100
		}
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO runtime.agent_runs
                (id, tenant_id, requested_by, agent_version_id, status,
                 input_payload, input_digest, correlation_id, priority, max_attempts)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5::jsonb, $6, NULLIF($7,'')::uuid, $8, $9)
            ON CONFLICT (id) DO NOTHING`,
			r.ID, r.RequestedBy, r.AgentVersionID, r.Status,
			string(r.InputPayload), digestBytes(r.InputPayload), r.CorrelationID,
			priority, maxAttempts); err != nil {
			return fmt.Errorf("写入演示运行 %s 失败: %w", r.ID, err)
		}
	}
	return nil
}

// insertPlugin 写入插件与插件版本。
//
// status 必须用 'CREATED'：registry.plugin_versions 的检查约束不接受 'DRAFT'
// （合法集为 CREATED/VALIDATING/TESTING/STAGED/APPROVED/PUBLISHED/DEPRECATED/DISABLED/REJECTED）。
func insertPlugin(ctx context.Context, tx pgx.Tx, p Plugin, createdBy string) error {
	manifest := p.Manifest
	if len(manifest) == 0 {
		manifest = json.RawMessage(fmt.Sprintf(`{"kind":%q}`, p.Kind))
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO registry.plugins (id, tenant_id, plugin_key, plugin_kind, name, description)
        VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)
        ON CONFLICT (tenant_id, id) DO NOTHING`,
		p.ID, p.Key, p.Kind, p.Name, p.Description); err != nil {
		return fmt.Errorf("写入插件 %s 失败: %w", p.Key, err)
	}
	digest := digestBytes(manifest)
	return execVersionUpsert(ctx, tx, `
        INSERT INTO registry.plugin_versions AS pv
            (id, tenant_id, plugin_id, version, manifest, artifact_digest, status, created_by)
        VALUES ($1, app.current_tenant_id(), $2, $3, $4::jsonb, $5, 'CREATED', $6)
        ON CONFLICT (plugin_id, version) DO UPDATE
           SET manifest = EXCLUDED.manifest,
               artifact_digest = EXCLUDED.artifact_digest
         WHERE pv.status NOT IN ('PUBLISHED', 'DEPRECATED', 'DISABLED', 'REJECTED')`,
		[]any{p.VersionID, p.ID, p.Version, string(manifest), digest, createdBy},
		`SELECT artifact_digest FROM registry.plugin_versions
          WHERE plugin_id = $1 AND version = $2`,
		[]any{p.ID, p.Version},
		digest, fmt.Sprintf("插件版本 %s@%s", p.Key, p.Version))
}

// execVersionUpsert 写入"不可变版本"行，并保证数据文件与库不会静默分叉。
//
// 语义（三种情形都必须有明确结果，不允许"命令成功但库没变"）：
//
//   - 行不存在                     → 插入；
//   - 行存在且处于非终态           → 用数据文件内容覆盖（seed 的职责是让库收敛到文件）；
//   - 行存在且处于终态（已发布类） → 内容与文件一致则幂等通过；不一致则报错。
//
// 第三点是关键：若沿用 `ON CONFLICT DO NOTHING`，改动数据文件后重跑 seed 会
// 保留旧内容、命令行却报告成功 —— 这正是本项目反复强调要消灭的静默降级。
//
// 实现要点：`DO UPDATE ... WHERE <非终态>` 使 RowsAffected 具备判别力 ——
// 1 表示"插入"或"覆盖"，0 表示"冲突命中且被 WHERE 挡下（终态行）"。
func execVersionUpsert(ctx context.Context, tx pgx.Tx, insertSQL string, args []any,
	selectSQL string, selectArgs []any, wantDigest, what string) error {
	tag, err := tx.Exec(ctx, insertSQL, args...)
	if err != nil {
		return fmt.Errorf("写入 %s 失败: %w", what, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var got string
	if err := tx.QueryRow(ctx, selectSQL, selectArgs...).Scan(&got); err != nil {
		return fmt.Errorf("回读 %s 摘要失败: %w", what, err)
	}
	if got != wantDigest {
		return fmt.Errorf("seed: %s 处于终态（已发布/停用）且内容与数据文件不一致"+
			"（库 %s… ≠ 文件 %s…）。已发布版本不可变：请提升版本号，或用 seed --replace 重建演示数据",
			what, short8(got), short8(wantDigest))
	}
	return nil
}

// short8 截取摘要前 8 位，用于报错信息（完整摘要太长，无法一眼比对）。
func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// digestBytes 返回 SHA-256 十六进制摘要。
func digestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
