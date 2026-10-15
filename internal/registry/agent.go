package registry

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// AgentRef 是智能体定义（registry.agents）。
type AgentRef struct {
	ID          ids.ID
	TenantID    ids.ID
	PluginID    ids.ID
	AgentKey    string
	Domain      string
	Description string
	Status      string
}

// AgentVersion 是智能体版本（registry.agent_versions）。
// 运行时固定引用 agent_version_id 而非 agent_key（第二篇 §4.2）。
type AgentVersion struct {
	ID                 ids.ID
	TenantID           ids.ID
	AgentID            ids.ID
	PluginVersionID    ids.ID
	Version            string
	Config             json.RawMessage
	PromptTemplate     json.RawMessage
	InputSchema        json.RawMessage
	OutputSchema       json.RawMessage
	DependencyManifest json.RawMessage
	Status             string
	ContentDigest      string
	CreatedAt          time.Time
	PublishedAt        *time.Time
}

// ToolRef 是工具定义（registry.tools）。
type ToolRef struct {
	ID          ids.ID
	TenantID    ids.ID
	PluginID    ids.ID
	ToolKey     string
	Description string
	Status      string
}

// ToolVersion 是工具版本（registry.tool_versions）。
// capabilities 只是声明，不等于授权（第二篇 §4.3）。
type ToolVersion struct {
	ID              ids.ID
	TenantID        ids.ID
	ToolID          ids.ID
	PluginVersionID ids.ID
	Version         string
	InputSchema     json.RawMessage
	OutputSchema    json.RawMessage
	Capabilities    []string
	EffectClass     string
	ExecutionConfig json.RawMessage
	ContentDigest   string
	Status          string
	CreatedAt       time.Time
}

// CatalogRepository 抽象智能体/工具目录查询（RLS 表，事务内读取）。
type CatalogRepository interface {
	SelectAgentByKey(ctx context.Context, tenantID ids.ID, agentKey string) (AgentRef, error)
	SelectAgentVersions(ctx context.Context, tenantID, agentID ids.ID) ([]AgentVersion, error)
	SelectToolByKey(ctx context.Context, tenantID ids.ID, toolKey string) (ToolRef, error)
	SelectToolVersion(ctx context.Context, tenantID, toolID ids.ID, version string) (ToolVersion, error)
}

// CatalogService 提供智能体/工具版本解析（仅 PUBLISHED 可被运行时绑定）。
type CatalogService struct {
	Pool *pgxpool.Pool
	repo CatalogRepository
}

// NewCatalogService 用生产存储（pgxpool）构造服务。
func NewCatalogService(pool *pgxpool.Pool) *CatalogService {
	return &CatalogService{Pool: pool, repo: pgCatalogRepository{pool: pool}}
}

// ResolveAgentVersion 解析智能体版本。version 为空时取最新 PUBLISHED 版本。
// 智能体不存在返回 CodePluginNotFound；版本不存在或未发布返回
// CodePluginVersionConflict（运行期绝不绑定未发布版本）。
func (s *CatalogService) ResolveAgentVersion(ctx context.Context, tenantID ids.ID, agentKey, version string) (AgentVersion, error) {
	agent, err := s.repo.SelectAgentByKey(ctx, tenantID, agentKey)
	if err != nil {
		if notFound(err) {
			return AgentVersion{}, errors.Ef(errors.CodePluginNotFound, "智能体 %q 不存在", agentKey)
		}
		return AgentVersion{}, errors.Wrap(err, errors.CodeInternal, "查询智能体失败")
	}
	versions, err := s.repo.SelectAgentVersions(ctx, tenantID, agent.ID)
	if err != nil {
		return AgentVersion{}, errors.Wrap(err, errors.CodeInternal, "查询智能体版本失败")
	}
	if version == "" {
		latest := latestPublished(versions)
		if latest == nil {
			return AgentVersion{}, errors.Ef(errors.CodePluginVersionConflict,
				"智能体 %q 没有已发布版本", agentKey)
		}
		return *latest, nil
	}
	for _, v := range versions {
		if v.Version != version {
			continue
		}
		if v.Status != VersionPublished {
			return AgentVersion{}, errors.Ef(errors.CodePluginVersionConflict,
				"智能体 %q 版本 %s 状态为 %s，仅 PUBLISHED 可解析", agentKey, version, v.Status)
		}
		return v, nil
	}
	return AgentVersion{}, errors.Ef(errors.CodePluginVersionConflict,
		"智能体 %q 版本 %s 不存在", agentKey, version)
}

// ListAgentVersions 列出智能体的全部版本（按版本号降序）。
func (s *CatalogService) ListAgentVersions(ctx context.Context, tenantID ids.ID, agentKey string) ([]AgentVersion, error) {
	agent, err := s.repo.SelectAgentByKey(ctx, tenantID, agentKey)
	if err != nil {
		if notFound(err) {
			return nil, errors.Ef(errors.CodePluginNotFound, "智能体 %q 不存在", agentKey)
		}
		return nil, errors.Wrap(err, errors.CodeInternal, "查询智能体失败")
	}
	versions, err := s.repo.SelectAgentVersions(ctx, tenantID, agent.ID)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "查询智能体版本失败")
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return versions, nil
}

// latestPublished 返回列表中版本号最大的 PUBLISHED 版本。
func latestPublished(versions []AgentVersion) *AgentVersion {
	var latest *AgentVersion
	for i := range versions {
		v := versions[i]
		if v.Status != VersionPublished {
			continue
		}
		if latest == nil || v.Version > latest.Version {
			latest = &v
		}
	}
	return latest
}

// pgCatalogRepository 是 CatalogRepository 的 PostgreSQL 实现。
type pgCatalogRepository struct {
	pool *pgxpool.Pool
}

func (r pgCatalogRepository) SelectAgentByKey(ctx context.Context, tenantID ids.ID, agentKey string) (AgentRef, error) {
	var agent AgentRef
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT id, tenant_id, plugin_id, agent_key, domain, description, status
			 FROM registry.agents WHERE tenant_id = $1 AND agent_key = $2`,
			tenantID.String(), agentKey)
		var (
			idText, tenantTxt, pluginTxt string
			scanErr                      error
		)
		if scanErr = row.Scan(&idText, &tenantTxt, &pluginTxt, &agent.AgentKey, &agent.Domain,
			&agent.Description, &agent.Status); scanErr != nil {
			if scanErr == pgx.ErrNoRows {
				return errRepoNotFound
			}
			return scanErr
		}
		if agent.ID, scanErr = ids.Parse(idText); scanErr != nil {
			return scanErr
		}
		if agent.TenantID, scanErr = ids.Parse(tenantTxt); scanErr != nil {
			return scanErr
		}
		if agent.PluginID, scanErr = ids.Parse(pluginTxt); scanErr != nil {
			return scanErr
		}
		return nil
	})
	return agent, err
}

func (r pgCatalogRepository) SelectAgentVersions(ctx context.Context, tenantID, agentID ids.ID) ([]AgentVersion, error) {
	var out []AgentVersion
	err := readInTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		rows, qErr := tx.Query(ctx,
			`SELECT id, tenant_id, agent_id, plugin_version_id, version, config, prompt_template,
			        input_schema, output_schema, dependency_manifest, status, content_digest, created_at, published_at
			 FROM registry.agent_versions WHERE tenant_id = $1 AND agent_id = $2
			 ORDER BY version DESC`,
			tenantID.String(), agentID.String())
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			v, scanErr := scanAgentVersion(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func scanAgentVersion(row pgx.Row) (AgentVersion, error) {
	var (
		v         AgentVersion
		idText    string
		tenantTxt string
		agentTxt  string
		pluginTx  string
		published *time.Time
	)
	err := row.Scan(&idText, &tenantTxt, &agentTxt, &pluginTx, &v.Version, &v.Config, &v.PromptTemplate,
		&v.InputSchema, &v.OutputSchema, &v.DependencyManifest, &v.Status, &v.ContentDigest,
		&v.CreatedAt, &published)
	if err != nil {
		return AgentVersion{}, err
	}
	var perr error
	if v.ID, perr = ids.Parse(idText); perr != nil {
		return AgentVersion{}, perr
	}
	if v.TenantID, perr = ids.Parse(tenantTxt); perr != nil {
		return AgentVersion{}, perr
	}
	if v.AgentID, perr = ids.Parse(agentTxt); perr != nil {
		return AgentVersion{}, perr
	}
	if v.PluginVersionID, perr = ids.Parse(pluginTx); perr != nil {
		return AgentVersion{}, perr
	}
	v.PublishedAt = published
	return v, nil
}
