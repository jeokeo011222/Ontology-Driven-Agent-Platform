package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// RegisterModelRevisionInput 是「模型版本登记」的输入。
//
// 一条登记 = 一个端点上的一个模型版本（llm.model_revisions 行）＋ 其能力档案
// （llm.capability_profiles 行，按能力内容寻址）。二者缺一，端点都不会被
// Model Registry 装配（见 internal/llm/registry.go Load 的排除原因），
// 于是自动生成端点一直 fail-closed 返回「模型网关未装配」。
type RegisterModelRevisionInput struct {
	EndpointID string
	ModelID    string
	// RevisionLabel 是模型版本的修订标识（可空）。同 (endpoint_id, model_id,
	// revision_label) 视为同一条登记，重复登记走更新而非新建。
	RevisionLabel string
	// CapabilityRevision 是能力档案的修订标识（必填，见迁移 000019 的 NOT NULL）。
	CapabilityRevision string
	// CapabilityVerified 为 true 时能力档案落 VERIFIED 并记录验证时间；
	// 否则落 UNVERIFIED（仍参与路由，但装配期会留痕说明能力未经探测验证）。
	CapabilityVerified bool
	Capabilities       llm.ProviderCapabilities
}

// RegisterModelRevision 登记一个模型版本。
//
// 写入顺序受外键约束：能力档案必须先于模型版本（llm.model_revisions.capability_digest
// 外键指向 llm.capability_profiles.capability_digest）。
//
// 能力档案采用**内容寻址**（主键 = 规范化能力的 SHA-256），因此摘要与内容恒等；
// 摘要算法只有 llm.CapabilityDigest 一处实现，与 seed 路径共用，
// 两条写入路径不可能分裂出两条内容相同但摘要不同的档案。
func (s *Store) RegisterModelRevision(ctx context.Context, in RegisterModelRevisionInput) (map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}

	endpointID := strings.TrimSpace(in.EndpointID)
	if endpointID == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "endpoint_id 必填")
	}
	modelID := strings.TrimSpace(in.ModelID)
	if modelID == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "model_id 必填")
	}
	revisionLabel := strings.TrimSpace(in.RevisionLabel)
	capabilityRevision := strings.TrimSpace(in.CapabilityRevision)
	if capabilityRevision == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "capability_revision 必填（能力档案的修订标识）")
	}
	// max_context_tokens <= 0 的能力档案会在装配期被 decodeCapabilitiesJSON 排除，
	// 于是用户"登记成功了"却仍然不参与路由——属于典型的静默失败，故在此前置拒绝。
	if in.Capabilities.MaxContextTokens <= 0 {
		return nil, errors.Ef(errors.CodeInvalidArgument,
			"max_context_tokens 必须为正整数（实际 %d）；为 0 或负数会导致该模型在装配期被排除、不参与路由",
			in.Capabilities.MaxContextTokens)
	}

	// 端点必须存在（数据库 FK 会兜底，但先查一遍返回更友好的错误）。
	var exists bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM llm.endpoints WHERE endpoint_id = $1::uuid)`,
		endpointID).Scan(&exists); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "检查模型端点存在性失败")
	}
	if !exists {
		return nil, errors.E(errors.CodeNotFound, "模型端点不存在："+endpointID)
	}

	capJSON, err := llm.CapabilityJSON(in.Capabilities)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "序列化能力声明失败")
	}
	digest := llm.CapabilityDigest(in.Capabilities)
	if digest == "" {
		return nil, errors.E(errors.CodeInternal, "计算能力摘要失败")
	}

	capStatus := "UNVERIFIED"
	if in.CapabilityVerified {
		capStatus = "VERIFIED"
	}

	// ---- ① 能力档案（内容寻址，先于模型版本写入）----
	// 同一份能力内容的多个模型版本复用同一条档案；DO UPDATE 只刷新修订与状态，
	// 并对已验证状态做**单调**处理：已 VERIFIED 的档案不被后续未验证登记降级，
	// verified_at 取先到者。RETURNING 直接回读库内真值（状态可能是既有的 VERIFIED，
	// 未必等于本次请求声明的 capStatus），避免用请求值冒充落库结果。
	var (
		capStatusFinal string
		capRevFinal    string
		capCapsRaw     []byte
	)
	if err := s.Pool.QueryRow(ctx, `
        INSERT INTO llm.capability_profiles
            (capability_digest, capabilities, revision, status, verified_at)
        VALUES ($1, $2::jsonb, $3, $4,
                CASE WHEN $4 = 'VERIFIED' THEN now() ELSE NULL END)
        ON CONFLICT (capability_digest) DO UPDATE
           SET revision = EXCLUDED.revision,
               status = CASE WHEN llm.capability_profiles.status = 'VERIFIED'
                             THEN 'VERIFIED' ELSE EXCLUDED.status END,
               verified_at = COALESCE(llm.capability_profiles.verified_at, EXCLUDED.verified_at)
        RETURNING status, revision, capabilities`,
		digest, string(capJSON), capabilityRevision, capStatus,
	).Scan(&capStatusFinal, &capRevFinal, &capCapsRaw); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "写入能力档案失败")
	}

	// ---- ② 模型版本（同 (endpoint_id, model_id, revision_label) 视为同一条登记）----
	// llm.model_revisions 没有该组合的唯一约束，因此先查后写，避免每次登记都新增一行。
	var existingID string
	err = s.Pool.QueryRow(ctx, `
        SELECT model_revision_id::text FROM llm.model_revisions
         WHERE endpoint_id = $1::uuid AND model_id = $2
           AND coalesce(revision_label, '') = coalesce(NULLIF($3, ''), '')
         LIMIT 1`, endpointID, modelID, revisionLabel).Scan(&existingID)
	switch {
	case err == nil:
		// 已存在：更新能力指向与状态（重新登记即视为重新启用）。
		if _, uerr := s.Pool.Exec(ctx, `
            UPDATE llm.model_revisions
               SET capability_digest = $2, status = 'ACTIVE'
             WHERE model_revision_id = $1::uuid`, existingID, digest); uerr != nil {
			return nil, errors.Wrap(uerr, errors.CodeInternal, "更新模型版本失败")
		}
	case err == pgx.ErrNoRows:
		existingID = ids.New().String()
		if _, ierr := s.Pool.Exec(ctx, `
            INSERT INTO llm.model_revisions
                (model_revision_id, endpoint_id, model_id, revision_label, capability_digest, status)
            VALUES ($1, $2::uuid, $3, NULLIF($4, ''), $5, 'ACTIVE')`,
			existingID, endpointID, modelID, revisionLabel, digest); ierr != nil {
			return nil, errors.Wrap(ierr, errors.CodeInternal, "创建模型版本失败")
		}
	default:
		return nil, errors.Wrap(err, errors.CodeInternal, "查询模型版本失败")
	}

	return map[string]any{
		"model_revision_id":   existingID,
		"endpoint_id":         endpointID,
		"model_id":            modelID,
		"revision_label":      revisionLabel,
		"capability_digest":   digest,
		"capability_revision": capRevFinal,
		"capability_status":   capStatusFinal,
		"capabilities":        json.RawMessage(capCapsRaw),
		"status":              "ACTIVE",
	}, nil
}

// ListModelRevisions 列出某端点已登记的模型版本（未登记时返回空集）。
//
// 端点不存在返回 CodeNotFound（区分"没登记过"与"端点都不存在"）。
func (s *Store) ListModelRevisions(ctx context.Context, endpointID string) ([]map[string]any, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "endpoint_id 必填")
	}

	var exists bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM llm.endpoints WHERE endpoint_id = $1::uuid)`,
		endpointID).Scan(&exists); err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "检查模型端点存在性失败")
	}
	if !exists {
		return nil, errors.E(errors.CodeNotFound, "模型端点不存在："+endpointID)
	}

	rows, err := s.queryMapsNoTenant(ctx, `
        SELECT r.model_revision_id::text, r.endpoint_id::text, r.model_id,
               coalesce(r.revision_label, '') AS revision_label,
               r.capability_digest, r.status,
               coalesce(cp.revision, '') AS capability_revision,
               coalesce(cp.status, '') AS capability_status,
               coalesce(cp.capabilities, '{}'::jsonb) AS capabilities,
               r.created_at
          FROM llm.model_revisions r
          LEFT JOIN llm.capability_profiles cp ON cp.capability_digest = r.capability_digest
         WHERE r.endpoint_id = $1::uuid
         ORDER BY r.model_id, r.revision_label`, endpointID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// DeleteModelRevision 删除一条模型版本登记，带下游保护。
//
// 保护条件：llm.usage_ledger 引用该 model_revision_id → 409 Conflict。
// 账本是业务推理的对账真值（审计不可篡改），一旦某模型版本被真实调用过，
// 其登记行就不能硬删——否则账本里的 model_revision_id 会指向不存在的行。
//
// 归属校验：model_revision_id 必须属于路径上的 endpoint_id，否则 404，
// 避免通过另一个端点的路径删除本端点之外的登记。
func (s *Store) DeleteModelRevision(ctx context.Context, endpointID, modelRevisionID string) error {
	if s == nil || s.Pool == nil {
		return errors.E(errors.CodeUnavailable, "数据库连接池未就绪")
	}
	endpointID = strings.TrimSpace(endpointID)
	modelRevisionID = strings.TrimSpace(modelRevisionID)
	if endpointID == "" || modelRevisionID == "" {
		return errors.E(errors.CodeInvalidArgument, "endpoint_id 与 model_revision_id 均必填")
	}

	var belongs bool
	if err := s.Pool.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM llm.model_revisions
                       WHERE model_revision_id = $1::uuid AND endpoint_id = $2::uuid)`,
		modelRevisionID, endpointID).Scan(&belongs); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查模型版本归属失败")
	}
	if !belongs {
		return errors.E(errors.CodeNotFound, "模型版本登记不存在或不属于该端点")
	}

	var ledgerCount int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM llm.usage_ledger WHERE model_revision_id = $1::uuid`,
		modelRevisionID).Scan(&ledgerCount); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "检查用量账本引用失败")
	}
	if ledgerCount > 0 {
		return errors.Ef(errors.CodeConflict,
			"该模型版本已产生 %d 条用量账本记录（审计真值不可硬删），无法删除登记", ledgerCount)
	}

	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM llm.model_revisions WHERE model_revision_id = $1::uuid`, modelRevisionID)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "删除模型版本失败")
	}
	if tag.RowsAffected() == 0 {
		return errors.E(errors.CodeNotFound, "模型版本登记不存在")
	}
	return nil
}
