package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/authorization"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// ---------------------------------------------------------------------------
// Ontologies
// ---------------------------------------------------------------------------

// ListOntologies 返回本体列表（cursor 分页 + ?status= 与 ?q= 筛选）。
//
// 软删除（status=DELETED）的条目不进列表：删除的本意是"从工作视图里消失"，
// 若仍列出来，调用方会以为删除没生效。审计链的保留体现在记录**仍存在**
// （详情/审计仍可按 id 查到），而不是继续出现在默认列表里；显式传
// status=DELETED 可查看已删除条目。
func (s *Store) ListOntologies(ctx context.Context, limit int, cursor, status, q string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT o.id::text AS id, o.ontology_key, o.name, o.domain, o.granularity,
                   o.description, o.status,
                   v.id::text AS latest_version_id, v.version AS latest_version,
                   v.status AS latest_version_status
              FROM ontology.ontologies o
              LEFT JOIN LATERAL (
                  SELECT id, version, status FROM ontology.ontology_versions
                   WHERE ontology_id = o.id ORDER BY created_at DESC LIMIT 1
              ) v ON TRUE
             WHERE o.status <> 'DELETED'
               AND ($1::text IS NULL OR o.ontology_key > $1)
               AND (($3::text = '' AND o.status <> 'DELETED') OR ($3::text <> '' AND o.status = $3::text))
               AND ($4::text = '' OR o.ontology_key ILIKE '%' || $4 || '%' OR o.name ILIKE '%' || $4 || '%' OR coalesce(o.description, '') ILIKE '%' || $4 || '%')
             ORDER BY o.ontology_key ASC
             LIMIT $2`, nullableStr(from), limit+1, status, q)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["ontology_key"].(string); ok {
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

// CreateOntologyInput 是创建本体的输入。
type CreateOntologyInput struct {
	OntologyKey string
	Name        string
	Domain      string
	Granularity string
	Description string
	Version     string
	Manifest    []byte
	// CategoryID 可选：本体归属的分类（ontology.categories）。空串表示不归类。
	CategoryID string
}

// CreateOntology 创建本体定义与首个 DRAFT 版本。
func (s *Store) CreateOntology(ctx context.Context, in CreateOntologyInput) (map[string]any, error) {
	if in.OntologyKey == "" || in.Name == "" || in.Domain == "" || in.Granularity == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_key/name/domain/granularity 必填")
	}
	if len(in.Manifest) == 0 || !json.Valid(in.Manifest) {
		return nil, errors.E(errors.CodeInvalidArgument, "manifest 必须是合法 JSON")
	}
	if in.Version == "" {
		in.Version = "0.1.0"
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		ontologyID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO ontology.ontologies
                (id, tenant_id, ontology_key, name, domain, granularity, description, category_id)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7)`,
			ontologyID, in.OntologyKey, in.Name, in.Domain, in.Granularity, in.Description,
			nullableStr(in.CategoryID)); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "ontology_key 已存在，或 category_id 不存在")
		}
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO ontology.ontology_versions
                (id, tenant_id, ontology_id, version, status, manifest, content_digest, created_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, 'DRAFT', $4::jsonb, $5, $6)`,
			versionID, ontologyID, in.Version, string(in.Manifest), digestBytes(in.Manifest), createdBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "本体版本已存在或写入失败")
		}
		out["id"] = ontologyID
		out["ontology_key"] = in.OntologyKey
		out["version_id"] = versionID
		out["version"] = in.Version
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PublishOntology 发布指定本体的最新可发布版本到目标环境。
func (s *Store) PublishOntology(ctx context.Context, ontologyID ids.ID, version, environment string) (map[string]any, error) {
	if ontologyID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_id 不合法")
	}
	if environment == "" {
		environment = "DEVELOPMENT"
	}
	switch environment {
	case "DEVELOPMENT", "STAGING", "PRODUCTION":
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "environment 不在契约枚举内")
	}
	publishedBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		var versionID string
		// 发布门禁：只允许 VALID / APPROVED 进入 PUBLISHED。
		//
		// 早期实现把 'DRAFT' 也列入白名单，等价于"未经校验即可直发生产"，
		// 与第五篇 §11.1「发布前置条件」直接冲突（Manifest/Schema 验证、
		// RDF/OWL 语法检查、SHACL 发布策略、业务规则冲突检查均须先通过）。
		// VALIDATING / REJECTED / DEPRECATED / DISABLED 同样不可发布。
		if err := tx.QueryRow(ctx, `
            UPDATE ontology.ontology_versions
               SET status = 'PUBLISHED', published_at = now()
             WHERE ontology_id = $1 AND ($2::text IS NULL OR version = $2)
               AND status IN ('VALID','APPROVED')
               AND id = (SELECT id FROM ontology.ontology_versions
                          WHERE ontology_id = $1 ORDER BY created_at DESC LIMIT 1)
            RETURNING id::text`, ontologyID.String(), nullableStr(version)).Scan(&versionID); err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeConflict,
					"没有可发布的本体版本：需先通过校验并处于 VALID/APPROVED（第五篇 §11.1）")
			}
			return errors.Wrap(err, errors.CodeInternal, "发布本体失败")
		}
		// 同一环境同一本体只保留一条有效发布指针。
		if _, err := tx.Exec(ctx, `
            UPDATE ontology.publications SET revoked_at = now()
             WHERE ontology_id = $1 AND environment = $2 AND revoked_at IS NULL`,
			ontologyID.String(), environment); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "撤销旧发布记录失败")
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO ontology.publications
                (id, tenant_id, ontology_id, ontology_version_id, environment, published_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5)`,
			ids.New().String(), ontologyID.String(), versionID, environment, publishedBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "写入发布记录失败")
		}
		out["ontology_id"] = ontologyID.String()
		out["version_id"] = versionID
		out["environment"] = environment
		out["status"] = "PUBLISHED"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OntologyLatestVersion 返回本体的最新版本状态与版本号（供校验/发布前置检查）。
func (s *Store) OntologyLatestVersion(ctx context.Context, ontologyID ids.ID) (string, string, error) {
	if ontologyID.IsZero() {
		return "", "", errors.E(errors.CodeInvalidArgument, "ontology_id 不合法")
	}
	var (
		status  string
		version string
	)
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
            SELECT v.status, v.version
              FROM ontology.ontologies o
              JOIN ontology.ontology_versions v ON v.ontology_id = o.id
             WHERE o.id = $1
             ORDER BY v.created_at DESC LIMIT 1`, ontologyID.String()).Scan(&status, &version)
		if err != nil {
			if err == pgx.ErrNoRows {
				return errors.E(errors.CodeNotFound, "本体不存在或没有版本")
			}
			return errors.Wrap(err, errors.CodeInternal, "读取本体版本失败")
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return status, version, nil
}

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

// ListPolicies 返回每个 policy_key 的最新版本。
func (s *Store) ListPolicies(ctx context.Context, limit int, cursor string, status string) (*ListResult, error) {
	from, err := decKey(cursor)
	if err != nil {
		return nil, err
	}
	res := &ListResult{Items: []map[string]any{}}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT DISTINCT ON (policy_key)
                   id::text AS id, policy_key, version, status, policy_language,
                   created_at, published_at
              FROM governance.policy_versions
             WHERE ($1::text IS NULL OR status = $1)
               AND ($2::text IS NULL OR policy_key > $2)
             ORDER BY policy_key ASC, created_at DESC
             LIMIT $3`, nullableStr(status), nullableStr(from), limit+1)
		if err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		res.Items = rows
		if hasMore && len(rows) > 0 {
			if k, ok := rows[len(rows)-1]["policy_key"].(string); ok {
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

// ListPolicyVersions 返回某个 policy_key 的全部版本。
func (s *Store) ListPolicyVersions(ctx context.Context, policyKey string) (*ListResult, error) {
	if policyKey == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "policy_key 必填")
	}
	res := &ListResult{Items: []map[string]any{}}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT id::text AS id, policy_key, version, status, policy_language,
                   content_digest, created_at, published_at
              FROM governance.policy_versions
             WHERE policy_key = $1
             ORDER BY created_at DESC`, policyKey)
		if err != nil {
			return err
		}
		res.Items = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// CreatePolicyVersionFromCurrent 基于 policy_key 的**最新已发布版本**克隆一个
// 新 DRAFT 版本（前端「创建新版本」按钮：已发布策略不可原地修改，§8.4）。
//
// 语义：based_on='current' = 最新 PUBLISHED（按 published_at 降序）；该 key
// 没有任何已发布版本时拒绝（409）——从草稿克隆草稿没有意义，且会造成版本谱系不明。
// 版本号自动派生：点分数字（1.2.3）递增最后一段；否则追加 -rev2/-rev3；
// 与既有版本号冲突时顺延，绝不静默覆盖（UNIQUE(tenant_id, policy_key, version) 兜底）。
func (s *Store) CreatePolicyVersionFromCurrent(ctx context.Context, policyKey string) (map[string]any, error) {
	if policyKey == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "policy_key 必填")
	}
	if len(policyKey) > 128 {
		return nil, errors.E(errors.CodeInvalidArgument, "policy_key 过长")
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		// 基准：最新 PUBLISHED。
		base, err := queryMaps(ctx, tx, `
            SELECT version, policy_language, policy_document::text AS document
              FROM governance.policy_versions
             WHERE policy_key = $1 AND status = 'PUBLISHED'
             ORDER BY published_at DESC NULLS LAST, created_at DESC
             LIMIT 1`, policyKey)
		if err != nil {
			return err
		}
		if len(base) == 0 {
			return errors.E(errors.CodeConflict,
				"策略 "+policyKey+" 没有已发布版本，无法作为新版本的克隆基准（仅 PUBLISHED 可基于）")
		}
		document := asString(base[0]["document"])
		language := asString(base[0]["policy_language"])
		baseVersion := asString(base[0]["version"])
		if !json.Valid([]byte(document)) {
			return errors.E(errors.CodeInternal, "基准版本文档不是合法 JSON（数据损坏）")
		}
		// DSL 结构校验与 CreatePolicy 同源：克隆的数据损坏要在写入前被拦下。
		if language == "JSON_DSL_V1" {
			if _, err := authorization.ParsePolicy([]byte(document)); err != nil {
				return errors.E(errors.CodeValidationFailed, "基准版本文档未通过 JSON DSL 校验（数据损坏）")
			}
		}
		// 既有版本号全集 → 派生下一个可用版本号。
		rows, err := queryMaps(ctx, tx,
			`SELECT version FROM governance.policy_versions WHERE policy_key = $1`, policyKey)
		if err != nil {
			return err
		}
		existing := make(map[string]bool, len(rows))
		for _, row := range rows {
			existing[asString(row["version"])] = true
		}
		next := nextDraftVersion(baseVersion, existing)
		if next == "" {
			return errors.E(errors.CodeConflict, "无法派生下一个版本号（版本号空间耗尽）")
		}
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO governance.policy_versions
                (id, tenant_id, policy_key, version, status, policy_language,
                 policy_document, content_digest, created_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, 'DRAFT', $4, $5::jsonb, $6, $7)`,
			versionID, policyKey, next, language, document, digestBytes([]byte(document)),
			createdBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "创建策略版本失败（version 可能已被并发占用）")
		}
		out["id"] = versionID
		out["policy_key"] = policyKey
		out["version"] = next
		out["based_on"] = baseVersion
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nextDraftVersion 从基准版本号派生下一个未占用的草稿版本号。
// 点分数字递增最后一段（1.2.3 → 1.2.4）；非点分数字追加 -revN；冲突顺延。
func nextDraftVersion(base string, existing map[string]bool) string {
	candidate := base
	if dottedNumeric(base) {
		segs := strings.Split(base, ".")
		for i := 0; i < 1000; i++ {
			n, err := strconv.Atoi(segs[len(segs)-1])
			if err != nil {
				break // 理论不可达（dottedNumeric 已校验）；防御：退到 -revN 分支
			}
			segs[len(segs)-1] = strconv.Itoa(n + 1)
			candidate = strings.Join(segs, ".")
			if !existing[candidate] {
				return candidate
			}
		}
	}
	for i := 2; i < 1000; i++ {
		candidate = fmt.Sprintf("%s-rev%d", base, i)
		if !existing[candidate] {
			return candidate
		}
	}
	return ""
}

// dottedNumeric 判断是否为"每段都是数字"的版本号（至少一段）。
func dottedNumeric(v string) bool {
	if v == "" {
		return false
	}
	for _, seg := range strings.Split(v, ".") {
		if seg == "" {
			return false
		}
		for _, r := range seg {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// CreatePolicyInput 是创建策略版本的输入。
type CreatePolicyInput struct {
	PolicyKey      string
	Version        string
	PolicyLanguage string
	PolicyDocument []byte
}

// CreatePolicy 校验并创建策略版本（DRAFT）。
//
// 复用 authorization.ParsePolicy 做 JSON DSL 结构校验（真实领域校验，
// 而非 API 层自造规则）。
func (s *Store) CreatePolicy(ctx context.Context, in CreatePolicyInput) (map[string]any, error) {
	if in.PolicyKey == "" || in.Version == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "policy_key 与 version 必填")
	}
	if in.PolicyLanguage == "" {
		in.PolicyLanguage = "JSON_DSL_V1"
	}
	switch in.PolicyLanguage {
	case "JSON_DSL_V1", "OPA_REGO_V1":
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "policy_language 不在契约枚举内")
	}
	if len(in.PolicyDocument) == 0 || !json.Valid(in.PolicyDocument) {
		return nil, errors.E(errors.CodeInvalidArgument, "policy_document 必须是合法 JSON")
	}
	if in.PolicyLanguage == "JSON_DSL_V1" {
		if _, err := authorization.ParsePolicy(in.PolicyDocument); err != nil {
			return nil, errors.E(errors.CodeValidationFailed, "策略文档未通过 JSON DSL 校验")
		}
	}
	createdBy, err := subjectFromContext(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		versionID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO governance.policy_versions
                (id, tenant_id, policy_key, version, status, policy_language,
                 policy_document, content_digest, created_by)
            VALUES ($1, app.current_tenant_id(), $2, $3, 'DRAFT', $4, $5::jsonb, $6, $7)`,
			versionID, in.PolicyKey, in.Version, in.PolicyLanguage,
			string(in.PolicyDocument), digestBytes(in.PolicyDocument), createdBy.String()); err != nil {
			return errors.Wrap(err, errors.CodeConflict, "policy_key+version 已存在")
		}
		out["id"] = versionID
		out["policy_key"] = in.PolicyKey
		out["version"] = in.Version
		out["status"] = "DRAFT"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
