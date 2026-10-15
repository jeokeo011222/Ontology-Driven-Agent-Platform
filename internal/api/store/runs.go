package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
)

// runColumns 是 Run 的显式投影（uuid/jsonb 一律 ::text，保证稳定序列化）。
//
// 本体绑定列（objective / business_type / ontology_version_id / plan /
// review_conclusion，迁移 000035）统一追加在末尾：既有列的相对顺序是 scanRun
// 的位置契约，插在中间会让所有读取路径静默错位。
const runColumns = `
        id::text, tenant_id::text, requested_by::text, agent_version_id::text,
        status, input_payload::text, input_digest, output_payload::text, output_digest,
        correlation_id::text, attempt_count, max_attempts, priority, available_at,
        deadline_at, cancel_requested_at, last_reconciliation_at,
        lease_owner, lease_expires_at, lease_epoch,
        created_at, started_at, finished_at, error_code, error_summary,
        objective, business_type, ontology_version_id::text,
        plan::text, review_conclusion::text`

// Run 是 runtime.agent_runs 的读模型。
type Run struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	RequestedBy    string     `json:"requested_by"`
	AgentVersionID string     `json:"agent_version_id"`
	Status         string     `json:"status"`
	InputPayload   []byte     `json:"-"`
	InputDigest    string     `json:"input_digest"`
	OutputPayload  []byte     `json:"-"`
	OutputDigest   *string    `json:"output_digest,omitempty"`
	CorrelationID  string     `json:"correlation_id"`
	AttemptCount   int        `json:"attempt_count"`
	MaxAttempts    int        `json:"max_attempts"`
	Priority       int        `json:"priority"`
	AvailableAt    time.Time  `json:"available_at"`
	DeadlineAt     *time.Time `json:"deadline_at,omitempty"`
	CancelReqAt    *time.Time `json:"cancel_requested_at,omitempty"`
	LastReconAt    *time.Time `json:"last_reconciliation_at,omitempty"`
	LeaseOwner     *string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	LeaseEpoch     int64      `json:"lease_epoch"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	ErrorCode      *string    `json:"error_code,omitempty"`
	ErrorSummary   *string    `json:"error_summary,omitempty"`

	// 本体绑定（迁移 000035）。Agent 驱动的 Run 全部为 NULL；本体驱动的审查 Run
	// 反之没有 agent_version_id。两者都是「不适用」而非「查过且为空」。
	Objective         *string `json:"objective,omitempty"`
	BusinessType      *string `json:"business_type,omitempty"`
	OntologyVersionID *string `json:"ontology_version_id,omitempty"`
	// Plan / ReviewConclusion 是 jsonb 文本列，经 MarshalJSON 透出为原生 JSON。
	Plan             []byte `json:"-"`
	ReviewConclusion []byte `json:"-"`
}

// MarshalJSON 把 jsonb 文本列作为原生 JSON 输出（失败时退化为字符串）。
func (r Run) MarshalJSON() ([]byte, error) {
	type alias Run
	type out struct {
		alias
		InputPayload     json.RawMessage `json:"input_payload,omitempty"`
		OutputPayload    json.RawMessage `json:"output_payload,omitempty"`
		Plan             json.RawMessage `json:"plan,omitempty"`
		ReviewConclusion json.RawMessage `json:"review_conclusion,omitempty"`
	}
	return json.Marshal(out{
		alias:            alias(r),
		InputPayload:     rawJSON(r.InputPayload),
		OutputPayload:    rawJSON(r.OutputPayload),
		Plan:             rawJSON(r.Plan),
		ReviewConclusion: rawJSON(r.ReviewConclusion),
	})
}

// rawJSON 返回合法 JSON 原始字节；空值返回 nil（omitempty 生效）。
func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}

// RunStep 是 runtime.run_steps 的读模型。
type RunStep struct {
	StepNo        int             `json:"step_no"`
	ID            string          `json:"id"`
	StepType      string          `json:"step_type"`
	Status        string          `json:"status"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
	ErrorCode     *string         `json:"error_code,omitempty"`
	ErrorSummary  *string         `json:"error_summary,omitempty"`
	ResultPayload json.RawMessage `json:"result_payload,omitempty"`
}

// RunDependency 是 runtime.agent_run_dependencies 的读模型。
type RunDependency struct {
	DepType    string `json:"dep_type"`
	DepID      string `json:"dep_id"`
	DepVersion string `json:"dep_version"`
	DepDigest  string `json:"dep_digest"`
}

// RunStepInput 是创建 Run 时随之写入的一条步骤（同一事务，保证原子）。
//
// 为什么要在创建时一并写入：本体审查是一次同步完成的流程，它的「处理过程」
// 就是本次请求真实执行过的阶段。先建 Run 再补写步骤会留下「Run 已存在但步骤
// 为空」的中间态——那正是任务详情页看到「过程缺失」的样子，且无法与「流程真的
// 没跑」区分。同一事务写入让二者不可分离。
type RunStepInput struct {
	StepNo        int
	StepType      string
	Status        string
	ResultPayload []byte
	StartedAt     *time.Time
	FinishedAt    *time.Time
	ErrorCode     *string
	ErrorSummary  *string
}

// RunDependencyInput 是创建 Run 时随之写入的一条版本锁依赖（ADR-002）。
type RunDependencyInput struct {
	DepType    string
	DepID      ids.ID
	DepVersion string
	DepDigest  string
}

// CreateRunInput 是创建 Run 的输入（DTO 校验后由处理器填入）。
type CreateRunInput struct {
	// RunID 允许调用方预先指定 Run id；零值时由 store 生成。
	//
	// 为什么需要让调用方指定：本体驱动的审查需要在**建库之前**就把「本次 Run 专属
	// 的命名图 IRI」算出来（图 IRI 由 run id 派生，见 handlers/ontology_review.go），
	// 这样「图里的三元组」与「任务记录」指向同一个可追溯标识。若等落库后再取名，
	// 图就必须先建在一个临时名字下，随后无法从任务反查到它。
	RunID       ids.ID
	RequestedBy ids.ID
	// AgentVersionID 与 OntologyVersionID 至少填一个：
	//   - Agent 驱动：AgentVersionID 非零，OntologyVersionID 可空；
	//   - 本体驱动：OntologyVersionID 非零，AgentVersionID 留零（写 NULL）。
	AgentVersionID    ids.ID
	OntologyVersionID ids.ID
	// Objective / BusinessType 是本体驱动 Run 的自然语言输入（Agent 驱动时留空）。
	Objective    string
	BusinessType string
	// Plan / ReviewConclusion 是本体审查的产物（jsonb 原样字节，可为空）。
	Plan             []byte
	ReviewConclusion []byte
	// OutputPayload / OutputDigest 是 Run 的产出摘要。本体审查在**同一次请求内**
	// 就得到结论，因此它与 ReviewConclusion 一起在创建事务内写入：若改由落库后再
	// UPDATE 补写，就会留下「任务已完成、产出为空」的中间态，任务详情页看到的是
	// 一份没有结论的完成记录。
	OutputPayload []byte
	OutputDigest  string
	InputPayload  []byte
	InputDigest   string
	CorrelationID ids.ID
	Priority      int
	MaxAttempts   int
	DeadlineAt    *time.Time
	// Steps / Dependencies 随 Run 在同一事务内写入（可为空）。
	Steps        []RunStepInput
	Dependencies []RunDependencyInput
}

// CreateRun 插入一条 CREATED 状态的 Run（唯一执行资源的创建入口）。
//
// 若携带 Steps / Dependencies，则与 Run 在同一租户事务内写入：本体审查的
// 「处理过程」与「任务」必须同生同死，不允许出现只有其一的状态。
func (s *Store) CreateRun(ctx context.Context, in CreateRunInput) (*Run, error) {
	if in.RequestedBy.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "requested_by 必填")
	}
	if in.AgentVersionID.IsZero() && in.OntologyVersionID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "agent_version_id 与 ontology_version_id 至少填一个")
	}
	if !json.Valid(in.InputPayload) {
		return nil, errors.E(errors.CodeInvalidArgument, "input_payload 必须是合法 JSON")
	}
	if in.CorrelationID.IsZero() {
		in.CorrelationID = ids.New()
	}
	if in.Priority < 0 || in.Priority > 1000 {
		return nil, errors.E(errors.CodeInvalidArgument, "priority 必须在 0..1000 之间")
	}
	runID := in.RunID
	if runID.IsZero() {
		runID = ids.New()
	}
	var out *Run
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
            INSERT INTO runtime.agent_runs
                (id, tenant_id, requested_by, agent_version_id, status,
                 input_payload, input_digest, correlation_id, priority, max_attempts, deadline_at,
                 objective, business_type, ontology_version_id, plan, review_conclusion,
                 output_payload, output_digest)
            VALUES ($1, app.current_tenant_id(), $2, $3, 'CREATED',
                    $4::jsonb, $5, $6, $7, $8, $9,
                    $10, $11, $12, $13::jsonb, $14::jsonb,
                    $15::jsonb, $16)
            RETURNING `+runColumns,
			runID.String(), in.RequestedBy.String(), nullableID(in.AgentVersionID),
			string(in.InputPayload), in.InputDigest, in.CorrelationID.String(),
			in.Priority, in.MaxAttempts, in.DeadlineAt,
			nullableStr(in.Objective), nullableStr(in.BusinessType), nullableID(in.OntologyVersionID),
			nullableJSONText(in.Plan), nullableJSONText(in.ReviewConclusion),
			nullableJSONText(in.OutputPayload), nullableStr(in.OutputDigest))
		run, err := scanRun(row)
		if err != nil {
			return err
		}
		for _, st := range in.Steps {
			if st.StepType == "" || st.Status == "" {
				return errors.E(errors.CodeInvalidArgument, "run_step 的 step_type/status 必填")
			}
			if _, err := tx.Exec(ctx, `
                INSERT INTO runtime.run_steps
                    (id, tenant_id, run_id, step_no, step_type, status,
                     result_payload, started_at, finished_at, error_code, error_summary)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10)`,
				ids.New().String(), runID.String(), st.StepNo, st.StepType, st.Status,
				nullableJSONText(st.ResultPayload), st.StartedAt, st.FinishedAt,
				st.ErrorCode, st.ErrorSummary); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "写入 Run 步骤失败")
			}
		}
		for _, dep := range in.Dependencies {
			if dep.DepType == "" || dep.DepID.IsZero() || dep.DepDigest == "" {
				return errors.E(errors.CodeInvalidArgument, "run_dependency 的 dep_type/dep_id/dep_digest 必填")
			}
			if _, err := tx.Exec(ctx, `
                INSERT INTO runtime.agent_run_dependencies
                    (id, tenant_id, run_id, dep_type, dep_id, dep_version, dep_digest)
                VALUES ($1, app.current_tenant_id(), $2, $3, $4, $5, $6)`,
				ids.New().String(), runID.String(), dep.DepType, dep.DepID.String(),
				dep.DepVersion, dep.DepDigest); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "写入 Run 版本锁依赖失败")
			}
		}
		out = &run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetRun 按 id 读取 Run（RLS 限制为本租户）。
func (s *Store) GetRun(ctx context.Context, runID ids.ID) (*Run, error) {
	if runID.IsZero() {
		return nil, errors.E(errors.CodeInvalidArgument, "run_id 不合法")
	}
	var out *Run
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runtime.agent_runs WHERE id = $1`, runID.String())
		run, err := scanRun(row)
		if err != nil {
			return err
		}
		out = &run
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RunListFilter 是 Run 列表过滤条件。
type RunListFilter struct {
	Status string
	// Q 为可选关键字查询：匹配 run id / correlation_id / error_summary。
	Q      string
	Limit  int
	Cursor string
	// CreatedAfter/Before 为可选的 created_at 过滤（contracts/03 §1.5）。
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
}

// ListRuns 按 created_at 降序 cursor 分页返回 Run。
func (s *Store) ListRuns(ctx context.Context, f RunListFilter) ([]Run, string, error) {
	if f.Status != "" && !runstate.Valid(f.Status) {
		return nil, "", errors.E(errors.CodeInvalidArgument, "status 不是已登记 Run 状态")
	}
	cur, err := DecodeCursor(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	var (
		curTime *time.Time
		curID   *string
	)
	if cur != nil {
		curTime, curID = &cur.Time, &cur.Key
	}
	var (
		status *string
		after  = f.CreatedAfter
		before = f.CreatedBefore
	)
	if f.Status != "" {
		status = &f.Status
	}

	var (
		runs    []Run
		hasMore bool
	)
	err = s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT `+runColumns+`
              FROM runtime.agent_runs
             WHERE ($1::text IS NULL OR status = $1)
               AND ($2::timestamptz IS NULL OR (created_at, id) < ($2::timestamptz, $3::uuid))
               AND ($4::timestamptz IS NULL OR created_at >= $4)
               AND ($5::timestamptz IS NULL OR created_at <= $5)
               AND ($7::text = '' OR id::text ILIKE '%' || $7 || '%'
                    -- correlation_id 是 uuid 列：coalesce(uuid, '') 会让 PostgreSQL
                    -- 把 '' 解析为 uuid 并在**计划期**报 invalid input syntax（本端点
                    -- 因此长期 500）；必须先 ::text 再 coalesce。
                    OR coalesce(correlation_id::text, '') ILIKE '%' || $7 || '%'
                    OR coalesce(error_summary, '') ILIKE '%' || $7 || '%')
             ORDER BY created_at DESC, id DESC
             LIMIT $6`,
			status, curTime, curID, after, before, f.Limit+1, f.Q)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询 Run 列表失败")
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, run)
		}
		if err := rows.Err(); err != nil {
			return errors.Wrap(err, errors.CodeInternal, "遍历 Run 列表失败")
		}
		if len(runs) > f.Limit {
			hasMore = true
			runs = runs[:f.Limit]
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if hasMore && len(runs) > 0 {
		last := runs[len(runs)-1]
		next = nextCursorOf(last.CreatedAt, last.ID, true)
	}
	return runs, next, nil
}

// ListRunSteps 返回 Run 的步骤（按 step_no 升序）。
func (s *Store) ListRunSteps(ctx context.Context, runID ids.ID) ([]RunStep, error) {
	var out []RunStep
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT id::text, step_no, step_type, status, started_at, finished_at,
                   error_code, error_summary, result_payload::text
              FROM runtime.run_steps
             WHERE run_id = $1
             ORDER BY step_no ASC`, runID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询 Run 步骤失败")
		}
		defer rows.Close()
		for rows.Next() {
			var (
				st      RunStep
				payload *string
			)
			if err := rows.Scan(&st.ID, &st.StepNo, &st.StepType, &st.Status,
				&st.StartedAt, &st.FinishedAt, &st.ErrorCode, &st.ErrorSummary, &payload); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "读取 Run 步骤失败")
			}
			if payload != nil && json.Valid([]byte(*payload)) {
				st.ResultPayload = json.RawMessage(*payload)
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListRunDependencies 返回 Run 的版本锁依赖（ADR-002）。
func (s *Store) ListRunDependencies(ctx context.Context, runID ids.ID) ([]RunDependency, error) {
	var out []RunDependency
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
            SELECT dep_type, dep_id::text, dep_version, dep_digest
              FROM runtime.agent_run_dependencies
             WHERE run_id = $1
             ORDER BY dep_type ASC`, runID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "查询 Run 依赖失败")
		}
		defer rows.Close()
		for rows.Next() {
			var d RunDependency
			if err := rows.Scan(&d.DepType, &d.DepID, &d.DepVersion, &d.DepDigest); err != nil {
				return errors.Wrap(err, errors.CodeInternal, "读取 Run 依赖失败")
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RunExists 报告 Run 是否在本租户可见（用于写路径前置校验）。
func (s *Store) RunExists(ctx context.Context, runID ids.ID) (string, error) {
	var status string
	err := s.TenantTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status FROM runtime.agent_runs WHERE id = $1`, runID.String()).Scan(&status)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", errors.E(errors.CodeNotFound, "Run 不存在或不在可见范围")
		}
		return "", errors.Wrap(err, errors.CodeInternal, "读取 Run 状态失败")
	}
	return status, nil
}

// scanRun 从一行扫描 Run（行或 rows 均可）。
func scanRun(row scannable) (Run, error) {
	var r Run
	var inPayload, outPayload, plan, conclusion *string
	// agent_version_id 在本体驱动的 Run 上是 NULL（Agent 与本体只能绑定其一），
	// 而 Run.AgentVersionID 用空串表示「不适用」。因此必须经一个可空中转扫描：
	// 直接把 NULL 扫进 string 会报 "cannot scan NULL into *string"，
	// 并且会让整条本体审查链路在建库环节失败——错误信息还会伪装成读失败。
	var agentVersionID *string
	err := row.Scan(
		&r.ID, &r.TenantID, &r.RequestedBy, &agentVersionID, &r.Status,
		&inPayload, &r.InputDigest, &outPayload, &r.OutputDigest,
		&r.CorrelationID, &r.AttemptCount, &r.MaxAttempts, &r.Priority, &r.AvailableAt,
		&r.DeadlineAt, &r.CancelReqAt, &r.LastReconAt,
		&r.LeaseOwner, &r.LeaseExpiresAt, &r.LeaseEpoch,
		&r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.ErrorCode, &r.ErrorSummary,
		&r.Objective, &r.BusinessType, &r.OntologyVersionID, &plan, &conclusion,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return r, errors.E(errors.CodeNotFound, "Run 不存在或不在可见范围")
		}
		return r, errors.Wrap(err, errors.CodeInternal, "读取 Run 失败")
	}
	if agentVersionID != nil {
		r.AgentVersionID = *agentVersionID
	}
	if inPayload != nil {
		r.InputPayload = []byte(*inPayload)
	}
	if outPayload != nil {
		r.OutputPayload = []byte(*outPayload)
	}
	if plan != nil {
		r.Plan = []byte(*plan)
	}
	if conclusion != nil {
		r.ReviewConclusion = []byte(*conclusion)
	}
	return r, nil
}

// nullableID 把零值 ID 表示为 SQL NULL（「不适用」），非零值表示为 uuid 文本。
//
// 刻意不复用 nullableStr(id.String())：ids.ID 的零值 String() 是空串，
// 直接传给 uuid 列会因类型不合法而报错，反而把「不适用」变成「写入失败」。
func nullableID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := id.String()
	return &s
}

// nullableJSONText 把可空的 JSON 载荷转为 text 参数（空/零长转 NULL）。
//
// 必须走 text 而不是 []byte：jsonb 列上的 `$n::jsonb` 要求参数是可 cast 到 jsonb 的
// 类型，而 []byte 会被 pgx 编码为 bytea —— PostgreSQL 没有 bytea→jsonb 的转换，
// 语句会在执行期报 "cannot cast type bytea to jsonb"。传 *string 则编码为 text，
// 与 ::jsonb 兼容；NULL 时跳过 cast，写入 SQL NULL。
func nullableJSONText(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	return &s
}

// scannable 抽象 pgx.Row 与 pgx.Rows 的共同 Scan。
type scannable interface {
	Scan(dest ...any) error
}
