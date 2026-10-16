package action

// Recorder 把一次"决策/动作"落成两条腿：
//   1. 元数据行 → action.actions（RLS 租户事务内写入，迁移 000044）；
//   2. PROV-O 溯源三元组 → 图存储独立命名图 prov:{tenant}:{action_id}，
//      并把图 IRI 回填到行（prov_graph_iri 列）。
//
// 相似检索嵌入按"显式状态、绝不阻塞"的原则异步补算：
//   - EmbedFn 未装配（或无可嵌入文本）→ embed_status='SKIPPED'，显式留痕；
//   - 嵌入调用失败 / 返回空向量 / 维度不符 → embed_status='FAILED'+embed_error，
//     Record 仍返回成功——动作记录是主流程的事实，嵌入只是增强项，二者绝不能
//     绑死（"主流程不因嵌入失败而失败，但状态必须显式"）。
//
// 审计哈希链由调用方（HTTP 层 recordAudit / worker 审计路径）负责，本包不管。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/clock"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
)

// embeddingDim 是 action.actions.embedding 列的向量维度（vector(1024)）。
//
// 嵌入供应商返回其他维度时**绝不落列**：vector(1024) 列会直接拒绝并让整条
// UPDATE 失败；更严重的是不同维度向量混进检索空间会产生静默垃圾结果。
// 显式前置校验，把失败转成 embed_status='FAILED'+原因。
const embeddingDim = 1024

// embedErrorMaxRunes 是 embed_error 的长度上限（防止把供应商大段报错原样入库）。
const embedErrorMaxRunes = 512

// EmbedFn 是嵌入补算的最小依赖面：`func(ctx, tenantID, texts) ([][]float32, error)`。
//
// 刻意是函数指针而非 llm.Gateway 依赖：领域包不感知网关的装配细节（路由/限流/
// 记账都封在适配器里），测试也能用确定性的假函数注入。可为 nil——未配置嵌入
// 模型时动作记录必须照常工作（SKIPPED 显式留痕）。
type EmbedFn func(ctx context.Context, tenantID ids.ID, texts []string) ([][]float32, error)

// tenantExecuter 抽象"在租户上下文（app.tenant_id SET LOCAL）内执行一条语句"。
//
// action.actions 启用了租户 RLS，所有写入必须带租户上下文；抽象出最小面后，
// 生产实现走 pgxutil.TenantTx（全仓唯一的租户事务原语），测试用假执行器记录
// 语句与参数即可断言行为，不需要真库。
type tenantExecuter interface {
	execTenant(ctx context.Context, tenantID ids.ID, sql string, args ...any) (pgconn.CommandTag, error)
}

// poolExecuter 是 tenantExecuter 的生产实现（pgx 连接池 + 租户事务）。
type poolExecuter struct{ pool *pgxpool.Pool }

func (p poolExecuter) execTenant(ctx context.Context, tenantID ids.ID, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := pgxutil.TenantTx(ctx, p.pool, tenantID, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, sql, args...)
		tag = t
		return err
	})
	if err != nil {
		return pgconn.CommandTag{}, errors.Wrap(err, errors.CodeInternal, "action: 写入 action.actions 失败")
	}
	return tag, nil
}

// Recorder 是动作记录器（handlers.RecordActionForFlow 与 worker 各流的落点）。
type Recorder struct {
	// db 是行写入通道；Graph 是溯源图存储；二者缺一即 Record 失败（诚实报错，
	// 绝不出现"只写了一半还没人知道"）。
	db    tenantExecuter
	Graph graphstore.GraphStore

	// Embed 与 EmbedModelID：嵌入补算通道。Embed 为 nil → 所有动作 SKIPPED。
	Embed        EmbedFn
	EmbedModelID string

	// Clock 与 Logger 可选：Clock 缺省用真实时间；Logger 缺省不记日志
	// （嵌入失败已显式落库 embed_error，日志只是运维可见性的增强）。
	Clock  clock.Clock
	Logger *slog.Logger
}

// NewRecorder 构造生产 Recorder（db 通道来自 pgx 连接池）。
func NewRecorder(pool *pgxpool.Pool, graph graphstore.GraphStore, embed EmbedFn, embedModelID string) *Recorder {
	var db tenantExecuter
	if pool != nil {
		db = poolExecuter{pool: pool}
	}
	return &Recorder{db: db, Graph: graph, Embed: embed, EmbedModelID: embedModelID}
}

// newRecorderWithDB 供测试注入假执行器与固定时钟（包内可见）。
func newRecorderWithDB(db tenantExecuter, graph graphstore.GraphStore, embed EmbedFn,
	embedModelID string, clk clock.Clock) *Recorder {
	return &Recorder{db: db, Graph: graph, Embed: embed, EmbedModelID: embedModelID, Clock: clk}
}

func (r *Recorder) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return nil
}

func (r *Recorder) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now().UTC()
}

// insertActionSQL 写入动作元数据行。prov_graph_iri 不在此列：
// 它在溯源图真实写入成功后由回填 UPDATE 补上（图写失败时行上不留指向空图的引用）。
const insertActionSQL = `
INSERT INTO action.actions
    (id, tenant_id, run_id, type, title, summary, input_digest, output_digest,
     outcome, actor_kind, actor_name, embed_status, embed_error, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

// backfillProvGraphSQL 把溯源命名图 IRI 回填到行。
const backfillProvGraphSQL = `UPDATE action.actions SET prov_graph_iri = $2 WHERE id = $1`

// upsertEmbeddingSQL 写入嵌入结果（成功与失败共用一条语句，状态显式）。
const upsertEmbeddingSQL = `
UPDATE action.actions
   SET embedding = $2::vector, embedding_model = $3, embed_status = $4, embed_error = $5
 WHERE id = $1`

// Record 记录一次动作（写行 → 写溯源图 → 回填图 IRI → 补算嵌入）。
//
// 返回 error 仅表示"动作本体没有记录成功"（行/溯源图写入失败）；嵌入失败
// 不返回错误，只落 FAILED 状态——调用方的主流程绝不应因嵌入通道故障而中断。
func (r *Recorder) Record(ctx context.Context, in Input) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if r.db == nil {
		return errors.E(errors.CodeInternal, "action: 数据库通道未装配，无法记录动作")
	}
	if r.Graph == nil {
		return errors.E(errors.CodeInternal, "action: 图存储未装配，无法写入 PROV-O 溯源")
	}

	actionID := ids.New()
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = r.now()
	}
	inputDigest := normalizeDigest(in.InputDigest)
	outputDigest := normalizeDigest(in.OutputDigest)
	outcome := in.Outcome
	if outcome == "" {
		outcome = OutcomePending
	}
	actorKind := in.ActorKind
	if actorKind == "" {
		actorKind = ActorUser
	}
	// 嵌入是否计划补算：通道已装配且有可嵌入文本。二者任一不满足 →
	// insert 即显式 SKIPPED（"没配通道/没文本"是有意为之的状态，不是待办）。
	embedPlanned := r.Embed != nil && strings.TrimSpace(in.Summary) != ""
	embedStatus := EmbedSkipped
	if embedPlanned {
		embedStatus = EmbedPending
	}

	// 1. 写元数据行（RLS 租户事务）。
	if _, err := r.db.execTenant(ctx, in.TenantID, insertActionSQL,
		actionID.String(), in.TenantID.String(), nullableID(in.RunID), in.Type,
		in.Title, in.Summary, inputDigest, outputDigest,
		outcome, actorKind, in.ActorName, embedStatus, "", occurred); err != nil {
		return err
	}

	// 2. PROV-O 三元组写入独立命名图。
	graphIRI := ProvenanceGraphIRI(in.TenantID, actionID)
	triples := BuildProvenance(in.TenantID, actionID, actorKind, in.ActorName,
		inputDigest, outputDigest, occurred)
	if _, err := r.Graph.InsertTriples(ctx, in.TenantID, graphIRI, triples); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "action: 写入 PROV-O 溯源图失败")
	}

	// 3. 回填 prov_graph_iri。
	if _, err := r.db.execTenant(ctx, in.TenantID, backfillProvGraphSQL,
		actionID.String(), graphIRI); err != nil {
		return err
	}

	// 4. 嵌入补算（失败只落状态，绝不向上返回错误）。
	if embedPlanned {
		r.embedAfter(ctx, in.TenantID, actionID, in.Summary)
	}
	return nil
}

// embedAfter 同步补算摘要嵌入并落列；任何失败都转为 embed_status='FAILED'
// +embed_error 显式留痕，函数本身不返回错误。
func (r *Recorder) embedAfter(ctx context.Context, tenantID, actionID ids.ID, summary string) {
	text := strings.TrimSpace(summary)
	vectors, err := r.Embed(ctx, tenantID, []string{text})
	if err == nil && (len(vectors) == 0 || len(vectors[0]) == 0) {
		err = errors.E(errors.CodeInternal, "action: 嵌入供应商返回空向量")
	}
	if err == nil && len(vectors[0]) != embeddingDim {
		err = errors.Ef(errors.CodeInternal,
			"action: 嵌入向量维度 %d 与列定义 vector(%d) 不一致，拒绝落列", len(vectors[0]), embeddingDim)
	}
	if err != nil {
		msg := truncateRunes(err.Error(), embedErrorMaxRunes)
		if _, dbErr := r.db.execTenant(ctx, tenantID, upsertEmbeddingSQL,
			actionID.String(), nil, "", EmbedFailed, msg); dbErr != nil {
			// 嵌入失败 + 状态落库也失败：只剩日志一条路，必须喊出来。
			if lg := r.log(); lg != nil {
				lg.Error("action: 嵌入失败且失败状态落库失败", "action_id", actionID.String(), "error", dbErr)
			}
			return
		}
		if lg := r.log(); lg != nil {
			lg.Warn("action: 嵌入补算失败（动作已记录，embed_status=FAILED）",
				"action_id", actionID.String(), "error", msg)
		}
		return
	}
	if _, err := r.db.execTenant(ctx, tenantID, upsertEmbeddingSQL,
		actionID.String(), FormatVector(vectors[0]), r.EmbedModelID, EmbedSucceeded, ""); err != nil {
		// 向量拿到了但落列失败：同样只告警不阻断（Record 已即将成功返回）。
		if lg := r.log(); lg != nil {
			lg.Error("action: 嵌入向量落列失败（embed_status 停留在 PENDING，可由补算任务重试）",
				"action_id", actionID.String(), "error", err)
		}
	}
}

// normalizeDigest 把输入归一为 SHA-256 十六进制（小写）：
//   - 已是 64 位十六进制（大小写不限）→ 原样收下（小写化）；
//   - 其他非空内容 → 视为原始内容，计算其 UTF-8 字节的 SHA-256；
//   - 空串 → 空串（动作没有该侧内容，溯源图不产生对应 Entity）。
//
// 这样调用方既可以传 findingDigest 风格的现成摘要，也可以直接丢原始载荷进来。
func normalizeDigest(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if isSHA256Hex(s) {
		return s
	}
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// FormatVector 把向量格式化为 pgvector 的文本字面量（"[v1,v2,...]"），
// 供 `$n::vector` 显式 cast 使用（与 jsonb 列的 "$n::jsonb + text 参数"同一套路）。
// float32 以最短十进制表示序列化（round-trip 无损）。
func FormatVector(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// truncateRunes 按 rune 截断文本（直接按字节切会把多字节字符拦腰截断）。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// nullableID 把零值 RunID 表示为 SQL NULL（与 store 包同名辅助器同一语义：
// uuid 列上空串会报 invalid input syntax，"不适用"必须是 NULL）。
func nullableID(id ids.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}
