package knowledge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/action"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 摄取任务种类（kb.ingest_jobs.kind 的枚举）。
const (
	KindEmbed   = "EMBED"
	KindExtract = "EXTRACT"
)

const (
	// leaseTTL 是任务租约时长；执行中每批续期，超时视为持有者死亡。
	leaseTTL = "120 seconds"
	// ingestPollInterval 是无任务可领时的轮询间隔。
	ingestPollInterval = 2 * time.Second
	// maxAttemptsDefault 与表默认一致（防御性冗余：读取后兜底）。
	maxAttemptsDefault = 3
)

// Runner 是知识摄取任务的运行器：领取（FOR UPDATE SKIP LOCKED）→
// 租约 → 分批执行并续期 → 成功 / 退避重试。
type Runner struct {
	Pool      *pgxpool.Pool
	Embedder  Embedder
	Completer Completer
	Graph     GraphWriter
	// EmbeddingModel 是全局默认嵌入模型（ODAP_KB_EMBEDDING_MODEL）；
	// 任务行上创建时快照的 embedding_model 优先（重试不漂移）。
	EmbeddingModel string
	// ExtractModel 是抽取补全的点名模型；空表示由网关按能力确定性择优。
	ExtractModel string
	Logger       *slog.Logger
	// Actions 是模块 D 动作记录器（可选）：任务终态留痕；nil 时跳过。
	Actions *action.Recorder
}

// ClaimedJob 是一次领取到的任务快照。
type ClaimedJob struct {
	ID                      ids.ID
	TenantID                ids.ID
	DocumentID              ids.ID
	Kind                    string
	EmbeddingModel          string
	TargetOntologyVersionID string
	Attempt                 int
	MaxAttempts             int
	// LeaseOwner 是领取时写入的租约持有者（tick 续期以此守护）。
	LeaseOwner string
}

// NewLeaseOwner 生成任务租约持有者标识（主机名 + 随机后缀，避免多 worker 撞名）。
func NewLeaseOwner() string {
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", host, hex.EncodeToString(b))
}

// ClaimNext 在租户上下文内原子领取一个可执行任务（FOR UPDATE SKIP LOCKED）：
// PENDING，或 FAILED 且 attempt<max_attempts 且到达 next_run_at 的退避任务。
// 领取即置 RUNNING + 续租约（lease_epoch+1），并按分块数设置 progress_total。
// 无可领任务返回 (nil, nil)。
func (r *Runner) ClaimNext(ctx context.Context, tenantID ids.ID, owner string) (*ClaimedJob, error) {
	if r == nil || r.Pool == nil {
		return nil, errors.E(errors.CodeInternal, "摄取任务运行器未装配数据库连接")
	}
	var job *ClaimedJob
	err := tenantTx(ctx, r.Pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
            UPDATE kb.ingest_jobs SET
                status = 'RUNNING',
                lease_owner = $1,
                lease_epoch = lease_epoch + 1,
                lease_expires_at = now() + interval '`+leaseTTL+`',
                progress_total = (SELECT count(*) FROM kb.chunks c WHERE c.document_id = kb.ingest_jobs.document_id),
                updated_at = now()
            WHERE id = (
                SELECT id FROM kb.ingest_jobs
                 WHERE (status = 'PENDING'
                        OR (status = 'FAILED' AND attempt < max_attempts AND next_run_at <= now()))
                 ORDER BY next_run_at ASC, created_at ASC
                 LIMIT 1
                 FOR UPDATE SKIP LOCKED
            )
            RETURNING id::text, document_id::text, kind, embedding_model,
                      coalesce(target_ontology_version_id::text, ''), attempt, max_attempts`, owner)
		var (
			idText, docText string
			cj              ClaimedJob
		)
		if err := row.Scan(&idText, &docText, &cj.Kind, &cj.EmbeddingModel,
			&cj.TargetOntologyVersionID, &cj.Attempt, &cj.MaxAttempts); err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return errors.Wrap(err, errors.CodeInternal, "领取摄取任务失败")
		}
		cj.ID, _ = ids.Parse(idText)
		cj.DocumentID, _ = ids.Parse(docText)
		cj.TenantID = tenantID
		cj.LeaseOwner = owner
		if cj.MaxAttempts <= 0 {
			cj.MaxAttempts = maxAttemptsDefault
		}
		job = &cj
		return nil
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// Run 执行已领取的任务（EMBED：分块嵌入回填；EXTRACT：本体约束下的知识抽取）。
// 进度回调统一走 tick（progress_done/stage + 租约续期）。
func (r *Runner) Run(ctx context.Context, job *ClaimedJob) error {
	if job == nil {
		return errors.E(errors.CodeInternal, "任务为空")
	}
	tick := func(done, total int, stage string) error {
		return r.tick(ctx, job, done, total, stage)
	}
	switch job.Kind {
	case KindEmbed:
		model := job.EmbeddingModel
		if model == "" {
			model = r.EmbeddingModel
		}
		return EmbedDocument(ctx, r.Pool, job.TenantID, job.DocumentID.String(), model, r.Embedder, tick)
	case KindExtract:
		if job.TargetOntologyVersionID == "" {
			return errors.E(errors.CodeInvalidArgument, "EXTRACT 任务缺少 target_ontology_version_id")
		}
		return ExtractDocument(ctx, ExtractOptions{
			Pool:                    r.Pool,
			Completer:               r.Completer,
			Graph:                   r.Graph,
			TenantID:                job.TenantID,
			JobID:                   job.ID,
			DocumentID:              job.DocumentID.String(),
			TargetOntologyVersionID: job.TargetOntologyVersionID,
			ModelID:                 r.ExtractModel,
			OnProgress:              tick,
		})
	default:
		return errors.Ef(errors.CodeInternal, "未知任务类型 %q", job.Kind)
	}
}

// tick 更新任务进度并续租约（每批/每分块一次；租约过期前持续保活）。
func (r *Runner) tick(ctx context.Context, job *ClaimedJob, done, total int, stage string) error {
	return tenantTx(ctx, r.Pool, job.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
            UPDATE kb.ingest_jobs SET
                progress_done = $2, progress_total = $3, stage = $4,
                lease_expires_at = now() + interval '`+leaseTTL+`',
                updated_at = now()
            WHERE id = $1 AND status = 'RUNNING' AND lease_owner = $5`,
			job.ID.String(), done, total, stage, job.LeaseOwner)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "更新任务进度失败")
		}
		if tag.RowsAffected() == 0 {
			// 行不在 RUNNING 或持有者已变更：租约丢失，尽快停止执行避免双写。
			return errors.E(errors.CodeConflict, "任务租约已丢失，停止执行")
		}
		return nil
	})
}

// Complete 标记任务成功（终态）。
func (r *Runner) Complete(ctx context.Context, job *ClaimedJob) error {
	return tenantTx(ctx, r.Pool, job.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
            UPDATE kb.ingest_jobs SET
                status = 'SUCCEEDED', progress_done = progress_total, stage = 'done',
                error = '', lease_owner = '', lease_expires_at = NULL, updated_at = now()
            WHERE id = $1`, job.ID.String())
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "标记任务成功失败")
		}
		return nil
	})
}

// Fail 标记任务失败：attempt+1 并按新 attempt 做退避（attempt*30s）。
// attempt 达到 max_attempts 后 ClaimNext 不再领取（终态 FAILED）。
func (r *Runner) Fail(ctx context.Context, job *ClaimedJob, cause error) error {
	msg := cause.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	return tenantTx(ctx, r.Pool, job.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
            UPDATE kb.ingest_jobs SET
                status = 'FAILED', attempt = attempt + 1, error = $2,
                next_run_at = now() + ((attempt + 1) * interval '30 seconds'),
                lease_owner = '', lease_expires_at = NULL, updated_at = now()
            WHERE id = $1`, job.ID.String(), msg)
		if err != nil {
			return errors.Wrap(err, errors.CodeInternal, "标记任务失败失败")
		}
		return nil
	})
}

// IngestJobLoop 启动 concurrency 个"领取-执行"协程（阻塞直至 ctx 取消）。
// 多协程并发领取依赖 FOR UPDATE SKIP LOCKED 去重；单轮内按租户逐个尝试。
func (r *Runner) IngestJobLoop(ctx context.Context, tenants func(ctx context.Context) []ids.ID, concurrency int) {
	if concurrency <= 0 {
		concurrency = 2
	}
	for i := 0; i < concurrency; i++ {
		go r.ingestWorker(ctx, NewLeaseOwner(), tenants)
	}
	<-ctx.Done()
}

// ingestWorker 是单个领取-执行协程：轮询租户 → 领取 → 执行 → 结算。
func (r *Runner) ingestWorker(ctx context.Context, owner string, tenants func(ctx context.Context) []ids.ID) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		claimed := false
		for _, tenantID := range tenants(ctx) {
			job, err := r.ClaimNext(ctx, tenantID, owner)
			if err != nil {
				r.log().Warn("领取摄取任务失败", "error", err)
				continue
			}
			if job == nil {
				continue
			}
			claimed = true
			r.execute(ctx, job)
		}
		if !claimed {
			sleepCtx(ctx, ingestPollInterval)
		}
	}
}

// execute 执行并结算单个任务（失败按退避重试，超上限即终态）。
func (r *Runner) execute(ctx context.Context, job *ClaimedJob) {
	if err := r.Run(ctx, job); err != nil {
		if stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded) {
			// worker 停机：不结算为失败，等待租约过期由运维口径处理。
			r.log().Info("worker 停机，任务保持 RUNNING 等待租约", "job", job.ID.String())
			return
		}
		r.log().Warn("摄取任务执行失败", "job", job.ID.String(), "kind", job.Kind, "error", err)
		if ferr := r.Fail(ctx, job, err); ferr != nil {
			r.log().Error("标记任务失败时出错", "job", job.ID.String(), "error", ferr)
		}
		// 模块 D：仅终态失败（重试配额耗尽）留痕；可重试失败由任务列表呈现。
		if job.Attempt+1 >= job.MaxAttempts {
			r.recordJobAction(ctx, job, action.OutcomeError, err.Error())
		}
		return
	}
	if err := r.Complete(ctx, job); err != nil {
		r.log().Error("标记任务成功时出错", "job", job.ID.String(), "error", err)
		return
	}
	r.recordJobAction(ctx, job, action.OutcomeSuccess, "")
}

// recordJobAction 模块 D：摄取任务终态动作留痕（旁路观测；Actions 未装配时跳过）。
func (r *Runner) recordJobAction(ctx context.Context, job *ClaimedJob, outcome, detail string) {
	if r.Actions == nil {
		return
	}
	kindLabel := "知识摄取任务 EMBED"
	if job.Kind == KindExtract {
		kindLabel = "知识摄取任务 EXTRACT"
	}
	title := kindLabel + "成功"
	if outcome == action.OutcomeError {
		title = kindLabel + "终态失败"
	}
	summary := "document=" + job.DocumentID.String() + " attempt=" + strconv.Itoa(job.Attempt)
	if detail != "" {
		summary += " error=" + detail
	}
	if err := r.Actions.Record(ctx, action.Input{
		Type:      action.TypeAction,
		Title:     title,
		Summary:   summary,
		Outcome:   outcome,
		ActorKind: action.ActorWorker,
		ActorName: "worker",
	}); err != nil && r.Logger != nil {
		r.Logger.Error("知识摄取任务动作留痕失败", "job", job.ID.String(), "error", err)
	}
}

// sleepCtx 可取消休眠。
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r *Runner) log() *slog.Logger {
	if r.Logger == nil {
		return slog.Default()
	}
	return r.Logger
}
