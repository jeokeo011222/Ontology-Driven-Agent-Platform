package action

// Recorder / Input / PROV-O 投影的单元测试。
//
// 假件模式对齐 graphstore 包现有测试：图存储用 graphstore.NewMemStore()（其语义
// 与 PGStore 对齐的内存实现），行写入用本文件的 fakeTenantExec（记录语句与参数，
// 不依赖真库）。断言落在三件事上：动作行发了什么、溯源图长什么形状、嵌入三态
// （SKIPPED / FAILED / SUCCEEDED）是否都显式落库。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"odap/internal/ontology/graphstore"
	"odap/internal/platform/ids"
)

// fixedTime 是测试基准时间（固定时钟保证 occurred_at 与 prov:atTime 可精确断言）。
var fixedTime = time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC)

// fakeClock 是 clock.Clock 的固定实现。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// execCall 记录一条租户内执行的语句与参数。
type execCall struct {
	sql  string
	args []any
}

// fakeTenantExec 是 tenantExecuter 的内存假实现：不依赖真库，只记录调用。
type fakeTenantExec struct {
	calls []execCall
	err   error
}

func (f *fakeTenantExec) execTenant(_ context.Context, _ ids.ID, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.err != nil {
		return pgconn.CommandTag{}, f.err
	}
	f.calls = append(f.calls, execCall{sql: sql, args: append([]any(nil), args...)})
	// pgconn.NewCommandTag 由命令文本构造 CommandTag（假实现不需要真实行数）。
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

// callsWithSQL 返回 SQL 包含子串的调用（断言辅助）。
func callsWithSQL(t *testing.T, f *fakeTenantExec, substr string) []execCall {
	t.Helper()
	var out []execCall
	for _, c := range f.calls {
		if strings.Contains(c.sql, substr) {
			out = append(out, c)
		}
	}
	return out
}

func argString(t *testing.T, args []any, i int) string {
	t.Helper()
	s, ok := args[i].(string)
	if !ok {
		t.Fatalf("第 %d 个参数不是 string：%T", i, args[i])
	}
	return s
}

// ---------------------------------------------------------------------------
// Input 校验（表驱动）
// ---------------------------------------------------------------------------

func TestInputValidate(t *testing.T) {
	tenant := ids.New()
	valid := Input{
		TenantID: tenant, Type: TypeQuery, Title: "查询客户授信",
		Outcome: OutcomeSuccess, ActorKind: ActorAgent,
	}
	cases := []struct {
		name    string
		mutate  func(*Input)
		wantErr bool
	}{
		{"合法输入", func(*Input) {}, false},
		{"缺租户", func(in *Input) { in.TenantID = ids.ID{} }, true},
		{"缺标题", func(in *Input) { in.Title = "   " }, true},
		{"缺类型", func(in *Input) { in.Type = "" }, true},
		{"类型不在白名单", func(in *Input) { in.Type = "DELETE" }, true},
		{"类型小写不通过", func(in *Input) { in.Type = "query" }, true},
		{"outcome 不在白名单", func(in *Input) { in.Outcome = "OK" }, true},
		{"outcome 允许空串", func(in *Input) { in.Outcome = "" }, false},
		{"actor_kind 不在白名单", func(in *Input) { in.ActorKind = "ROBOT" }, true},
		{"actor_kind 允许空串", func(in *Input) { in.ActorKind = "" }, false},
		{"五类类型全部合法", func(in *Input) {
			for _, ty := range Types {
				in.Type = ty
				if err := in.Validate(); err != nil {
					t.Fatalf("类型 %s 应当合法，得到 %v", ty, err)
				}
			}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.mutate(&in)
			err := in.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Record：PROV-O 三元组形状
// ---------------------------------------------------------------------------

func TestRecordProvenanceShape(t *testing.T) {
	ctx := context.Background()
	graph := graphstore.NewMemStore()
	exec := &fakeTenantExec{}
	rec := newRecorderWithDB(exec, graph, nil, "", fakeClock{fixedTime})

	tenant, runID := ids.New(), ids.New()
	in := Input{
		TenantID: tenant, RunID: runID, Type: TypeInfer,
		Title: "推理客户违约风险", Summary: "   ", // 空白摘要：嵌入应显式 SKIPPED
		InputDigest:  "raw-input-payload",
		OutputDigest: strings.Repeat("ab", 32), // 已是 SHA-256 hex，应原样收下
		Outcome:      OutcomeSuccess, ActorKind: ActorAgent, ActorName: "planner-agent",
	}
	if err := rec.Record(ctx, in); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	// 从假执行器记录的 INSERT 参数取回 action_id（记录器内部生成）。
	inserts := callsWithSQL(t, exec, "INSERT INTO action.actions")
	if len(inserts) != 1 {
		t.Fatalf("应恰好一条 INSERT，得到 %d", len(inserts))
	}
	actionID, err := ids.Parse(argString(t, inserts[0].args, 0))
	if err != nil {
		t.Fatalf("INSERT 首参数不是合法 UUID：%v", err)
	}
	// 行上的嵌入状态：未装配 EmbedFn 且摘要为空白 → 显式 SKIPPED，绝不 PENDING。
	if got := argString(t, inserts[0].args, 11); got != EmbedSkipped {
		t.Fatalf("embed_status = %q, want SKIPPED", got)
	}

	// 从图存储读回该动作命名图的全部三元组。
	graphIRI := ProvenanceGraphIRI(tenant, actionID)
	if graphIRI != "prov:"+tenant.String()+":"+actionID.String() {
		t.Fatalf("溯源图 IRI 形态不符：%s", graphIRI)
	}
	triples, err := graph.Match(ctx, tenant, graphstore.Pattern{GraphIRI: graphIRI})
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	// 谓词集合：恰好 5 种谓词；rdf:type 出现 4 次（Activity/Agent/输入 Entity/输出 Entity）。
	wantPreds := map[string]int{
		RDFType:               4,
		ProvWasAssociatedWith: 1,
		ProvAtTime:            1,
		ProvUsed:              1,
		ProvWasGeneratedBy:    1,
	}
	gotPreds := map[string]int{}
	for _, tr := range triples {
		gotPreds[tr.P.Lexical]++
		if tr.FactKind != graphstore.FactKindAsserted {
			t.Fatalf("溯源事实应为 ASSERTED，得到 %s", tr.FactKind)
		}
		if tr.GraphIRI != graphIRI {
			t.Fatalf("三元组落在错误的命名图：%s", tr.GraphIRI)
		}
	}
	if len(gotPreds) != len(wantPreds) {
		t.Fatalf("谓词种类 = %d, want %d（%v）", len(gotPreds), len(wantPreds), gotPreds)
	}
	for p, n := range wantPreds {
		if gotPreds[p] != n {
			t.Fatalf("谓词 %s 出现 %d 次, want %d", p, gotPreds[p], n)
		}
	}

	actionIRI := ActionIRI(actionID)
	if !strings.HasPrefix(actionIRI, "urn:odap:action:") || !strings.HasSuffix(actionIRI, actionID.String()) {
		t.Fatalf("actionIRI 格式不符：%s", actionIRI)
	}

	// rdf:type 断言：Activity / Agent / 两个 Entity。
	typeSubjects := map[string]string{} // 类型 IRI → 主语 IRI
	for _, tr := range triples {
		if tr.P.Lexical != RDFType {
			continue
		}
		typeSubjects[tr.O.Lexical] = tr.S.Lexical
	}
	if typeSubjects[ProvActivityType] != actionIRI {
		t.Fatalf("Activity 主语不符：%s", typeSubjects[ProvActivityType])
	}
	actorIRI := typeSubjects[ProvAgentType]
	if !strings.HasPrefix(actorIRI, "urn:odap:actor:agent:") {
		t.Fatalf("actorIRI 格式不符（应为 urn:odap:actor:agent:<摘要>）：%s", actorIRI)
	}

	// wasAssociatedWith：动作 → 主体。
	for _, tr := range triples {
		if tr.P.Lexical != ProvWasAssociatedWith {
			continue
		}
		if tr.S.Lexical != actionIRI || tr.O.Lexical != actorIRI {
			t.Fatalf("wasAssociatedWith 形状不符：%s → %s", tr.S.Lexical, tr.O.Lexical)
		}
	}

	// atTime：xsd:dateTime 字面值，等于固定时钟时间。
	for _, tr := range triples {
		if tr.P.Lexical != ProvAtTime {
			continue
		}
		if !tr.O.IsLiteral() || tr.O.Datatype != XSDNamespace+"dateTime" {
			t.Fatalf("atTime 应为 xsd:dateTime 字面值：%+v", tr.O)
		}
		if tr.O.Lexical != fixedTime.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("atTime = %s, want %s", tr.O.Lexical, fixedTime.UTC().Format(time.RFC3339Nano))
		}
	}

	// prov:used：输入 digest（原始内容 → 归一 SHA-256）派生 Entity。
	wantInput := contentIRIRoot + normalizeDigest("raw-input-payload")
	for _, tr := range triples {
		if tr.P.Lexical != ProvUsed {
			continue
		}
		if tr.S.Lexical != actionIRI || tr.O.Lexical != wantInput {
			t.Fatalf("used 形状不符：%s → %s（want 宾语 %s）", tr.S.Lexical, tr.O.Lexical, wantInput)
		}
	}

	// prov:wasGeneratedBy：输出 Entity → 动作。
	for _, tr := range triples {
		if tr.P.Lexical != ProvWasGeneratedBy {
			continue
		}
		if tr.S.Lexical != contentIRIRoot+strings.Repeat("ab", 32) || tr.O.Lexical != actionIRI {
			t.Fatalf("wasGeneratedBy 形状不符：%s → %s", tr.S.Lexical, tr.O.Lexical)
		}
	}

	// prov_graph_iri 回填：一条 UPDATE，参数 (action_id, graphIRI)。
	backfills := callsWithSQL(t, exec, "SET prov_graph_iri")
	if len(backfills) != 1 {
		t.Fatalf("应恰好一条 prov_graph_iri 回填，得到 %d", len(backfills))
	}
	if got := argString(t, backfills[0].args, 1); got != graphIRI {
		t.Fatalf("回填的 prov_graph_iri = %s, want %s", got, graphIRI)
	}

	// 同一主体在不同动作里必须解析到同一 Agent 节点（确定性派生的核心价值）。
	if ActorIRI(ActorAgent, "planner-agent") != actorIRI {
		t.Fatalf("ActorIRI 非确定性：%s vs %s", ActorIRI(ActorAgent, "planner-agent"), actorIRI)
	}
}

// ---------------------------------------------------------------------------
// Record：嵌入三态（SKIPPED / FAILED / SUCCEEDED）
// ---------------------------------------------------------------------------

func TestRecordEmbeddingStates(t *testing.T) {
	ctx := context.Background()
	tenant := ids.New()
	summary := "  客户授信复审摘要  "

	t.Run("未装配EmbedFn_显式SKIPPED", func(t *testing.T) {
		exec := &fakeTenantExec{}
		rec := newRecorderWithDB(exec, graphstore.NewMemStore(), nil, "", fakeClock{fixedTime})
		in := Input{TenantID: tenant, Type: TypeQuery, Title: "查询", Summary: summary}
		if err := rec.Record(ctx, in); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
		inserts := callsWithSQL(t, exec, "INSERT INTO action.actions")
		if got := argString(t, inserts[0].args, 11); got != EmbedSkipped {
			t.Fatalf("embed_status = %q, want SKIPPED", got)
		}
		// 无嵌入 UPDATE：只有 INSERT + prov_graph_iri 回填两条语句。
		if n := len(callsWithSQL(t, exec, "embed_status = $4")); n != 0 {
			t.Fatalf("不应有嵌入 UPDATE，得到 %d 条", n)
		}
	})

	t.Run("嵌入失败_显式FAILED且不阻塞", func(t *testing.T) {
		exec := &fakeTenantExec{}
		rec := newRecorderWithDB(exec, graphstore.NewMemStore(),
			func(context.Context, ids.ID, []string) ([][]float32, error) {
				return nil, context.DeadlineExceeded
			}, "test-model", fakeClock{fixedTime})
		in := Input{TenantID: tenant, Type: TypeQuery, Title: "查询", Summary: summary}
		if err := rec.Record(ctx, in); err != nil {
			t.Fatalf("嵌入失败不应阻塞 Record，得到 %v", err)
		}
		inserts := callsWithSQL(t, exec, "INSERT INTO action.actions")
		if got := argString(t, inserts[0].args, 11); got != EmbedPending {
			t.Fatalf("embed_status（插入时）= %q, want PENDING", got)
		}
		updates := callsWithSQL(t, exec, "embed_status = $4")
		if len(updates) != 1 {
			t.Fatalf("应恰好一条嵌入 UPDATE，得到 %d", len(updates))
		}
		args := updates[0].args
		if got := argString(t, args, 3); got != EmbedFailed {
			t.Fatalf("embed_status = %q, want FAILED", got)
		}
		if msg := argString(t, args, 4); msg == "" {
			t.Fatalf("embed_error 不应为空（必须显式带原因）")
		}
		if args[1] != nil {
			t.Fatalf("失败时 embedding 列应为 NULL，得到 %v", args[1])
		}
	})

	t.Run("嵌入成功_向量落列", func(t *testing.T) {
		exec := &fakeTenantExec{}
		var gotText string
		vec := make([]float32, embeddingDim)
		for i := range vec {
			vec[i] = 0.5
		}
		rec := newRecorderWithDB(exec, graphstore.NewMemStore(),
			func(_ context.Context, _ ids.ID, texts []string) ([][]float32, error) {
				gotText = texts[0]
				return [][]float32{vec}, nil
			}, "test-model", fakeClock{fixedTime})
		in := Input{TenantID: tenant, Type: TypeQuery, Title: "查询", Summary: summary}
		if err := rec.Record(ctx, in); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
		if gotText != strings.TrimSpace(summary) {
			t.Fatalf("嵌入文本应去除首尾空白，得到 %q", gotText)
		}
		updates := callsWithSQL(t, exec, "embed_status = $4")
		if len(updates) != 1 {
			t.Fatalf("应恰好一条嵌入 UPDATE，得到 %d", len(updates))
		}
		args := updates[0].args
		if got := argString(t, args, 1); got != FormatVector(vec) || !strings.HasPrefix(got, "[0.5,") {
			t.Fatalf("embedding 字面量不符：%q", got)
		}
		if got := argString(t, args, 2); got != "test-model" {
			t.Fatalf("embedding_model = %q, want test-model", got)
		}
		if got := argString(t, args, 3); got != EmbedSucceeded {
			t.Fatalf("embed_status = %q, want SUCCEEDED", got)
		}
	})

	t.Run("维度不符_拒绝落列", func(t *testing.T) {
		exec := &fakeTenantExec{}
		rec := newRecorderWithDB(exec, graphstore.NewMemStore(),
			func(context.Context, ids.ID, []string) ([][]float32, error) {
				return [][]float32{make([]float32, 8)}, nil
			}, "test-model", fakeClock{fixedTime})
		in := Input{TenantID: tenant, Type: TypeQuery, Title: "查询", Summary: summary}
		if err := rec.Record(ctx, in); err != nil {
			t.Fatalf("维度不符不应阻塞 Record，得到 %v", err)
		}
		updates := callsWithSQL(t, exec, "embed_status = $4")
		if len(updates) != 1 || argString(t, updates[0].args, 3) != EmbedFailed {
			t.Fatalf("维度不符应落 FAILED，得到 %+v", updates)
		}
		if msg := argString(t, updates[0].args, 4); !strings.Contains(msg, "维度") {
			t.Fatalf("embed_error 应说明维度问题，得到 %q", msg)
		}
	})

	t.Run("空向量_拒绝落列", func(t *testing.T) {
		exec := &fakeTenantExec{}
		rec := newRecorderWithDB(exec, graphstore.NewMemStore(),
			func(context.Context, ids.ID, []string) ([][]float32, error) {
				return [][]float32{{}}, nil
			}, "test-model", fakeClock{fixedTime})
		in := Input{TenantID: tenant, Type: TypeQuery, Title: "查询", Summary: summary}
		if err := rec.Record(ctx, in); err != nil {
			t.Fatalf("空向量不应阻塞 Record，得到 %v", err)
		}
		updates := callsWithSQL(t, exec, "embed_status = $4")
		if len(updates) != 1 || argString(t, updates[0].args, 3) != EmbedFailed {
			t.Fatalf("空向量应落 FAILED，得到 %+v", updates)
		}
	})
}

// ---------------------------------------------------------------------------
// Record：输入校验与摘要归一
// ---------------------------------------------------------------------------

func TestRecordValidation(t *testing.T) {
	exec := &fakeTenantExec{}
	rec := newRecorderWithDB(exec, graphstore.NewMemStore(), nil, "", fakeClock{fixedTime})
	if err := rec.Record(context.Background(), Input{Type: TypeQuery, Title: "无租户"}); err == nil {
		t.Fatal("缺 tenant_id 的 Record 应当失败")
	}
	if len(exec.calls) != 0 {
		t.Fatal("校验失败时不应有任何语句发出")
	}
}

func TestNormalizeDigest(t *testing.T) {
	raw := "客户授信申请书.pdf"
	want := sha256.Sum256([]byte(raw))
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{hex.EncodeToString(want[:]), hex.EncodeToString(want[:])},                  // 现成摘要原样收下
		{strings.ToUpper(hex.EncodeToString(want[:])), hex.EncodeToString(want[:])}, // 大写归一小写
		{raw, hex.EncodeToString(want[:])},                                          // 原始内容 → SHA-256
	}
	for _, tc := range cases {
		if got := normalizeDigest(tc.in); got != tc.want {
			t.Fatalf("normalizeDigest(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Turtle 序列化
// ---------------------------------------------------------------------------

func TestTurtleOutput(t *testing.T) {
	tenant, actionID := ids.New(), ids.New()
	triples := BuildProvenance(tenant, actionID, ActorAgent, "planner",
		normalizeDigest("in"), normalizeDigest("out"), fixedTime)
	ttl := Turtle(ProvenanceGraphIRI(tenant, actionID), triples)

	for _, want := range []string{
		"# ODAP PROV-O 溯源图 prov:" + tenant.String() + ":" + actionID.String(),
		"@prefix prov: <http://www.w3.org/ns/prov#> .",
		"@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .",
		"@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .",
		"<" + ActionIRI(actionID) + "> rdf:type prov:Activity .",
		"<" + ActionIRI(actionID) + "> prov:wasAssociatedWith <" + ActorIRI(ActorAgent, "planner") + "> .",
		"<" + ActionIRI(actionID) + "> prov:atTime \"" + fixedTime.UTC().Format(time.RFC3339Nano) + "\"^^<http://www.w3.org/2001/XMLSchema#dateTime> .",
	} {
		if !strings.Contains(ttl, want) {
			t.Fatalf("Turtle 输出缺少期望行：\n%s\n---\n%s", want, ttl)
		}
	}
	// 每条三元组以 " ." 结尾（Turtle 终止符）。
	for _, line := range strings.Split(ttl, "\n") {
		if strings.HasPrefix(line, "<") && !strings.HasSuffix(line, " .") {
			t.Fatalf("三元组行缺少终止符：%q", line)
		}
	}
}
