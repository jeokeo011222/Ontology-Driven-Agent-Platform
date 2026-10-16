package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 本文件实现本体校验的读写路径（第五篇 §5.3 验证记录与问题明细、§11.1 发布前置条件）。
//
// 数据落点由迁移 000023 统一提供：
//   - ontology.validation_runs      —— 一次校验的结论（含 validation_profile / summary_json）
//   - ontology.validation_findings  —— 逐条发现项，focus_node / path / severity 与
//     shacl.Result{FocusNode, Path, Component, Message, Severity} 一一对应
//
// 这两张表在本文件之前**没有任何写入方**，因此前端「验证发现项」页签永远为空、
// 发布门禁也不可能真正拦住任何东西。本文件是它们的数据来源。

// ---------------------------------------------------------------------------
// 读：版本快照
// ---------------------------------------------------------------------------

// OntologyVersionSnapshot 是执行一次本体校验所需的版本快照。
type OntologyVersionSnapshot struct {
	OntologyID  string
	OntologyKey string
	Name        string
	VersionID   string
	Version     string
	Status      string
	Manifest    json.RawMessage
}

// OntologyVersionSnapshot 返回本体最新版本的校验快照。
//
// 与 OntologyLatestVersion 的分工：后者只回状态与版本号（供发布前置检查），
// 本方法额外带回 manifest 与 id——校验需要它来构造词项命名空间与数据图 IRI。
func (s *Store) OntologyVersionSnapshot(ctx context.Context, ontologyID ids.ID) (*OntologyVersionSnapshot, error) {
	if ontologyID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_id 不合法")
	}
	var out *OntologyVersionSnapshot
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		// manifest 必须显式 ::text：pgx 的 jsonb 编解码器把值解成 map[string]any，
		// 而本路径需要的是原始 JSON 字节（交给 graphload.ParseManifest 解析）。
		// 不转型时 asString 会拿到 map 而返回空串，校验会以"manifest 不能为空"
		// 静默失败——一个"看起来是数据问题、实际是读取方式问题"的误导。
		// 同 tool_version.go:34 的 input_schema/output_schema 即用此法。
		rows, err := queryMaps(ctx, tx, `
            SELECT o.id::text AS ontology_id, o.ontology_key, o.name,
                   v.id::text AS version_id, v.version, v.status,
                   v.manifest::text AS manifest
              FROM ontology.ontologies o
              JOIN ontology.ontology_versions v ON v.ontology_id = o.id
             WHERE o.id = $1
             ORDER BY v.created_at DESC
             LIMIT 1`, ontologyID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "本体不存在或没有版本")
		}
		row := rows[0]
		rec := &OntologyVersionSnapshot{
			OntologyID:  asString(row["ontology_id"]),
			OntologyKey: asString(row["ontology_key"]),
			Name:        asString(row["name"]),
			VersionID:   asString(row["version_id"]),
			Version:     asString(row["version"]),
			Status:      asString(row["status"]),
		}
		if raw := asString(row["manifest"]); raw != "" {
			rec.Manifest = json.RawMessage(raw)
		}
		out = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OntologyVersionSnapshotByVersionID 与 OntologyVersionSnapshot 同构，但按
// **版本 id**（v.id = $1）精确定位。推理运行（POST /reasoning/runs）的入参是
// ontology_version_id：必须钉住传入的那个版本，绝不允许"传版本 id 却被解析成
// 该本体最新版"的口径漂移——用户对着详情页选中的版本跑推理，结果却作用在
// 另一个版本上是不可接受的静默偷换。
func (s *Store) OntologyVersionSnapshotByVersionID(ctx context.Context, versionID ids.ID) (*OntologyVersionSnapshot, error) {
	if versionID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_version_id 不合法")
	}
	var out *OntologyVersionSnapshot
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := queryMaps(ctx, tx, `
            SELECT o.id::text AS ontology_id, o.ontology_key, o.name,
                   v.id::text AS version_id, v.version, v.status,
                   v.manifest::text AS manifest
              FROM ontology.ontology_versions v
              JOIN ontology.ontologies o ON o.id = v.ontology_id
             WHERE v.id = $1
             LIMIT 1`, versionID.String())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.E(errors.CodeNotFound, "本体版本不存在")
		}
		row := rows[0]
		rec := &OntologyVersionSnapshot{
			OntologyID:  asString(row["ontology_id"]),
			OntologyKey: asString(row["ontology_key"]),
			Name:        asString(row["name"]),
			VersionID:   asString(row["version_id"]),
			Version:     asString(row["version"]),
			Status:      asString(row["status"]),
		}
		if raw := asString(row["manifest"]); raw != "" {
			rec.Manifest = json.RawMessage(raw)
		}
		out = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 写：校验进行中的可见状态
// ---------------------------------------------------------------------------

// MarkOntologyValidating 把最新版本置为 VALIDATING，返回版本 id。
//
// 为什么需要一次独立的、先于长任务的写入：图装载与 SHACL 校验可能耗时数秒到数十秒，
// 若只在结束时一次性写状态，这段时间里版本仍显示 DRAFT，运维无法区分"没人点过校验"
// 与"正在校验"。VALIDATING 是第五篇状态机里为此存在的状态。
//
// 允许的来源状态刻意包含 VALIDATING 与 REJECTED：前者是上次校验中途失败留下的
// 可重试状态，后者是"改了再验"的正常路径。PUBLISHED / DEPRECATED / DISABLED 不可
// 重新校验——已发布版本的校验结论必须保持不可变，否则会出现"生产在用的版本被改成
// REJECTED"这类事后篡改。
func (s *Store) MarkOntologyValidating(ctx context.Context, ontologyID ids.ID) (string, error) {
	if ontologyID.IsZero() {
		return "", errors.E(errors.CodeInvalidArgument, "ontology_id 不合法")
	}
	var versionID string
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
            UPDATE ontology.ontology_versions
               SET status = 'VALIDATING'
             WHERE ontology_id = $1
               AND status IN ('DRAFT', 'VALIDATING', 'VALID', 'REJECTED')
               AND id = (SELECT id FROM ontology.ontology_versions
                          WHERE ontology_id = $1 ORDER BY created_at DESC LIMIT 1)
            RETURNING id::text`, ontologyID.String()).Scan(&versionID)
		if err == pgx.ErrNoRows {
			return errors.E(errors.CodeConflict,
				"当前版本状态不允许校验（已发布/已弃用/已停用的版本不可重新校验）")
		}
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "置本体版本为 VALIDATING 失败")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return versionID, nil
}

// ---------------------------------------------------------------------------
// 写：校验结论与发现项
// ---------------------------------------------------------------------------

// ValidationFindingInput 是一条待写入的校验发现项。
type ValidationFindingInput struct {
	// Severity 取库内枚举 INFO / WARNING / ERROR / CRITICAL。
	Severity string
	// Code 是稳定的发现项代码（前端据此做分组与去重，不能是自由文本）。
	Code string
	// FocusNode / Path 直接来自 shacl.Result，是"哪条数据、哪个字段"的定位信息。
	FocusNode string
	Path      string
	Message   string
	// Digest 是发现项的跨轮次稳定身份（shacl.Result.Digest()）。
	// 它让"上轮 3 条违规、本轮剩 2 条"这类回归对比成为可能——id 是随机 UUID、
	// validation_run_id 每轮都变，唯有摘要在轮次之间保持稳定。为空时写 NULL。
	Digest string
	// DetailsJSON 保存组件名（sh:minCount 等）与其它上下文；为空时写 '{}'。
	DetailsJSON []byte
}

// RecordOntologyValidationInput 是一次校验的完整结论。
type RecordOntologyValidationInput struct {
	OntologyID string
	VersionID  string
	// ValidatorVersion 记录校验器版本，便于"同一份数据、不同校验器结论不同"时追溯。
	ValidatorVersion string
	// InputDigest 是被校验输入（manifest 字节）的摘要。
	InputDigest string
	// ReportDigest 是校验报告规范化后的摘要；无外部报告文件时 report_uri 保持 NULL。
	ReportDigest string
	// StartedAt 是校验开始时刻（用于把 validation_runs 的 started_at/finished_at
	// 落成真实时间跨度，而不是两次 now() 的近似相等）。
	StartedAt *time.Time

	SyntaxValid     bool
	SHACLConformant bool
	// OWLConsistent / BusinessRulesValid 为 nil 表示**未执行**该项检查。
	// 必须与 false（执行了、不通过）区分开：把"没查"写成"不通过"会凭空造出失败，
	// 写成"通过"则是假绿。用 NULL 表达"未执行"是三值逻辑里唯一诚实的选项。
	OWLConsistent      *bool
	BusinessRulesValid *bool

	Findings []ValidationFindingInput
	// NewStatus 是校验后的版本状态：VALID 或 REJECTED。
	NewStatus string
}

// RecordOntologyValidation 在一个事务内写入校验记录、全部发现项，并流转版本状态。
//
// 为什么必须同事务：若先写 findings 再单独改状态，中间失败就会留下
// "有 N 条违规 finding，但版本仍是 VALID" 的组合——这恰好是最危险的一种残留，
// 因为它已经通过了发布门禁（PublishOntology 只认 VALID/APPROVED），
// 而界面上却列着一堆阻断项。合并进一个事务后，这种组合在结构上不可能出现。
func (s *Store) RecordOntologyValidation(ctx context.Context, in RecordOntologyValidationInput) (map[string]any, error) {
	if in.OntologyID == "" || in.VersionID == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_id 与 version_id 必填")
	}
	switch in.NewStatus {
	case "VALID", "REJECTED":
	default:
		return nil, errors.E(errors.CodeInvalidArgument, "NewStatus 只能是 VALID 或 REJECTED")
	}
	if in.ValidatorVersion == "" || in.InputDigest == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "validator_version 与 input_digest 必填")
	}
	// 结论与发现项必须自洽：声明通过却有阻断级发现项，是自相矛盾的状态。
	// 此处显式拒绝，避免把矛盾写进库里留给后人猜。
	//
	// 反向（REJECTED 但一条阻断项都没有）刻意**不**拦截：sh:severity=Warning 的形状
	// 不产生阻断项，而 SHACL 仍可能因其它原因为不合规；更重要的是，将来接入 OWL
	// 一致性检查时，"不一致"未必表现为某条 finding。过度约束会让将来的校验器
	// 无法表达自己的结论。
	for _, f := range in.Findings {
		if in.NewStatus == "VALID" && (f.Severity == "ERROR" || f.Severity == "CRITICAL") {
			return nil, errors.E(errors.CodeInternal,
				"结论自相矛盾：声明 VALID 但存在阻断级发现项（校验链内部错误，已拒绝落库）")
		}
	}

	runStatus := "FAILED"
	if in.SHACLConformant {
		runStatus = "PASSED"
	}
	startedAt := time.Now().UTC()
	if in.StartedAt != nil {
		startedAt = *in.StartedAt
	}

	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		runID := ids.New().String()
		if _, err := tx.Exec(ctx, `
            INSERT INTO ontology.validation_runs
                (id, tenant_id, ontology_version_id, validator_version, input_digest, status,
                 syntax_valid, owl_consistent, shacl_conformant, business_rules_valid,
                 report_digest, started_at, finished_at)
            VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5,
                    $6, $7, $8, $9, $10, $11, now())`,
			runID, in.VersionID, in.ValidatorVersion, in.InputDigest, runStatus,
			in.SyntaxValid, in.OWLConsistent, in.SHACLConformant, in.BusinessRulesValid,
			nullableStr(in.ReportDigest), startedAt); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "写入校验记录失败")
		}

		written := 0
		for _, f := range in.Findings {
			details := f.DetailsJSON
			if len(details) == 0 || !json.Valid(details) {
				details = []byte("{}")
			}
			if _, err := tx.Exec(ctx, `
                INSERT INTO ontology.validation_findings
                    (id, tenant_id, validation_run_id, severity, finding_code,
                     focus_node, path, message, details_json, finding_digest)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`,
				ids.New().String(), runID, f.Severity, f.Code,
				nullableStr(f.FocusNode), nullableStr(f.Path), f.Message, string(details),
				nullableStr(f.Digest)); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "写入校验发现项失败")
			}
			written++
		}

		var newStatus string
		err := tx.QueryRow(ctx, `
            UPDATE ontology.ontology_versions
               SET status = $2
             WHERE id = $1 AND status IN ('DRAFT', 'VALIDATING', 'VALID', 'REJECTED')
            RETURNING status`, in.VersionID, in.NewStatus).Scan(&newStatus)
		if err == pgx.ErrNoRows {
			return errors.E(errors.CodeConflict,
				"版本状态已变化（可能已发布或在其它会话中被修改），校验结论未应用")
		}
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新本体版本状态失败")
		}

		out["validation_run_id"] = runID
		out["status"] = newStatus
		out["run_status"] = runStatus
		out["findings"] = written
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 读：校验发现项（前端「验证发现项」页签的数据源）
// ---------------------------------------------------------------------------

// clientSeverity 把库内严重级别映射为前端契约声明的三档。
//
// 库内枚举是 INFO/WARNING/ERROR/CRITICAL（与 validation_runs.status 等大写枚举一致），
// 而 web/src/types/domain.ts 的 ShaclFinding 声明为 'info'|'warning'|'violation'。
// 映射写成**全函数**（default 分支兜底为 violation）而不是"未知就原样返回"：
// 未知取值落到"未阻断"会让门禁被绕过，落到空串则会让前端渲染出空白级别——
// 两种都是自查矩阵反复记录过的静默退化。
func clientSeverity(dbSeverity string) (severity, level string) {
	switch dbSeverity {
	case "INFO":
		return "info", "提示"
	case "WARNING":
		return "warning", "警告"
	default: // ERROR / CRITICAL 以及任何未知取值
		return "violation", "违规"
	}
}

// queryLatestFindings 读取本体的最新一次校验运行与它的全部发现项。
//
// 返回的 items 恒为非 nil 切片：前端对 findings 直接调用 .filter/.map，
// 返回 null 会让详情页抛 TypeError（而不是显示空态）。契约里"没有数据"应当表达为
// 空数组，而不是缺失字段。
func queryLatestFindings(ctx context.Context, tx pgx.Tx, ontologyID string) (map[string]any, error) {
	rows, err := queryMaps(ctx, tx, `
        SELECT r.id::text AS run_id, r.status AS run_status, r.validator_version,
               r.syntax_valid, r.owl_consistent, r.shacl_conformant, r.business_rules_valid,
               r.started_at, r.finished_at
          FROM ontology.validation_runs r
          JOIN ontology.ontology_versions v ON v.id = r.ontology_version_id
         WHERE v.ontology_id = $1
         ORDER BY r.started_at DESC, r.id DESC
         LIMIT 1`, ontologyID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"items": []map[string]any{}}
	if len(rows) == 0 {
		// 从未校验过：明确的空态，而不是伪造一条"通过"。
		out["run"] = nil
		return out, nil
	}
	run := rows[0]
	out["run"] = run
	runID := asString(run["run_id"])

	frows, err := queryMaps(ctx, tx, `
        SELECT id::text AS id, severity, finding_code, focus_node, path, message, details_json,
               finding_digest
          FROM ontology.validation_findings
         WHERE validation_run_id = $1
         ORDER BY CASE severity WHEN 'CRITICAL' THEN 0 WHEN 'ERROR' THEN 1
                                WHEN 'WARNING' THEN 2 ELSE 3 END,
                  finding_code, id`, runID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(frows))
	for _, f := range frows {
		severity, level := clientSeverity(asString(f["severity"]))
		item := map[string]any{
			"id":         asString(f["id"]),
			"severity":   severity,
			"level":      level,
			"message":    asString(f["message"]),
			"blocking":   severity == "violation",
			"code":       asString(f["finding_code"]),
			"focus_node": asString(f["focus_node"]),
			"path":       asString(f["path"]),
		}
		// 焦点节点/路径可能为空（例如针对整个类而非具体实例的约束）：
		// 此时**不写**该键，前端用 ?? '—' 显示占位符。
		if item["focus_node"] == "" {
			delete(item, "focus_node")
		}
		if item["path"] == "" {
			delete(item, "path")
		}
		// 摘要同理：迁移前写入或未带形状身份的旧行没有摘要，写空串会伪造一个
		// "已知但为空"的身份，误导回归对比；此时不写该键。
		if d := asString(f["finding_digest"]); d != "" {
			item["finding_digest"] = d
		}
		items = append(items, item)
	}
	out["items"] = items
	return out, nil
}

// OntologyValidationFindings 返回本体最新一次校验的运行摘要与发现项。
func (s *Store) OntologyValidationFindings(ctx context.Context, ontologyID ids.ID) (map[string]any, error) {
	if ontologyID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "ontology_id 不合法")
	}
	out := map[string]any{}
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		res, err := queryLatestFindings(ctx, tx, ontologyID.String())
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
