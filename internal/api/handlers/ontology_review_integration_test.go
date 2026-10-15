//go:build integration

package handlers

// 本体驱动审查链路的后端集成测试（真实 PostgreSQL + 真实图存储 + 真实状态机）。
//
// 为什么需要它：链路的最后一环是「调用真实模型端点」。当端点因上游容量问题不可用时
// （DeepSeek 返回 503 service_unavailable），整条链路就没有任何自动化保护了。而链路里
// 真正决定正确性的部分——清单闭包复核、数据图装载、反假绿探针、SHACL 求值、结论合成、
// Run+步骤同事务落库、状态机推进——**与模型是谁无关**。
//
// 因此本测试用可注入的 LLMCompleter（Deps.LLM）替换模型调用，其返回内容由**真实本体
// 清单**派生（不是编造的业务数据）：取第一个形状的目标类，为其属性填示例字面量、为其
// 关系指向的目标类创建实例——其余全部走生产代码路径。
//
// 库中表带 FORCE RLS，所有直连查询都必须复用生产的租户事务原语
// （pgxutil.WithTenantTx），否则什么都读不到——这本身也是对「租户上下文是唯一入口」的验证。
//
// 需要 ODAP_DATABASE_URL（odap_app 角色）。未设置时跳过。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"odap/internal/api/store"
	"odap/internal/harness"
	"odap/internal/llm"
	"odap/internal/ontology/graphload"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/clock"
	"odap/internal/platform/ids"
	"odap/internal/platform/pgxutil"
	"odap/internal/platform/tenantctx"
)

// stubCompleter 是一个确定性的 LLMCompleter：原样返回预设内容，
// 不访问网络、不产生用量。仅用于测试装配（生产装配不接受此类实现）。
type stubCompleter struct{ content string }

func (s stubCompleter) Complete(_ context.Context, _ ids.ID, _ llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{Content: s.content}, nil
}

// withTenantTx 在指定租户的事务中执行 fn（复用生产的租户上下文注入原语），并在
// fn 成功后提交。
//
// 必须提交而不是回滚：清理探针数据也要走这个事务，若回滚，DELETE 会被一并撤销，
// 于是每次跑测试都会在共享开发库里留下一条假任务——正是用户明确要求清理的那种残留。
func withTenantTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID ids.ID,
	fn func(tx pgx.Tx) error) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := pgxutil.WithTenantTx(ctx, tx, tenantID); err != nil {
		t.Fatalf("注入租户上下文失败: %v", err)
	}
	if err := fn(tx); err != nil {
		t.Fatalf("租户事务内操作失败: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("提交租户事务失败: %v", err)
	}
}

// TestReviewByOntology_PersistsRunWithProcess 走完整链路并断言落库结果。
func TestReviewByOntology_PersistsRunWithProcess(t *testing.T) {
	dsn := os.Getenv("ODAP_DATABASE_URL")
	if dsn == "" {
		t.Skip("ODAP_DATABASE_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxutil.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接数据库失败: %v", err)
	}
	defer pool.Close()

	// ---- 定位一个真实已发布且带形状约束的本体（iam.tenants 无 RLS，可先列租户）----
	tenantRows, err := pool.Query(ctx, `SELECT id::text FROM iam.tenants ORDER BY created_at`)
	if err != nil {
		t.Fatalf("读取租户失败: %v", err)
	}
	var tenantStrs []string
	for tenantRows.Next() {
		var s string
		if err := tenantRows.Scan(&s); err != nil {
			t.Fatalf("扫描租户失败: %v", err)
		}
		tenantStrs = append(tenantStrs, s)
	}
	tenantRows.Close()
	if err := tenantRows.Err(); err != nil {
		t.Fatalf("遍历租户失败: %v", err)
	}

	var (
		tenantID, ontologyID, manifestText string
	)
	for _, ts := range tenantStrs {
		tid, perr := ids.Parse(ts)
		if perr != nil {
			continue
		}
		var found bool
		withTenantTx(t, ctx, pool, tid, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `
                SELECT o.id::text, v.manifest::text
                FROM ontology.ontologies o
                JOIN LATERAL (
                    SELECT manifest FROM ontology.ontology_versions
                    WHERE tenant_id = o.tenant_id AND ontology_id = o.id AND status = 'PUBLISHED'
                    ORDER BY created_at DESC LIMIT 1
                ) v ON true
                WHERE o.tenant_id = $1::uuid AND o.status = 'ACTIVE'
                ORDER BY o.created_at LIMIT 1`, ts).Scan(&ontologyID, &manifestText)
			if err == nil {
				found = true
				return nil
			}
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		})
		if found {
			tenantID = ts
			break
		}
	}
	if tenantID == "" {
		t.Skip("库中没有「已发布且带清单」的本体，跳过")
	}
	tid, err := ids.Parse(tenantID)
	if err != nil {
		t.Fatalf("解析租户失败: %v", err)
	}

	manifest := json.RawMessage(manifestText)
	parsed, err := graphload.ParseManifest(manifest)
	if err != nil {
		t.Fatalf("已发布本体的清单不合法: %v", err)
	}
	if len(parsed.Shapes) == 0 {
		t.Skip("该本体未定义形状约束，链路会 fail-closed，跳过")
	}

	// Run 的 requested_by 有外键约束（iam.users 是全局表，不带租户列）。
	var userID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM iam.users WHERE status = 'ACTIVE' ORDER BY created_at LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("没有可用用户作为 requested_by，跳过: %v", err)
	}
	uid, err := ids.Parse(userID)
	if err != nil {
		t.Fatalf("解析用户失败: %v", err)
	}

	h := New(Deps{
		Store:      store.New(pool),
		Machine:    &harness.Machine{Pool: pool, Clock: clock.RealClock{}},
		GraphStore: graphstore.NewMemStore(),
		Clock:      clock.RealClock{},
		LLM:        stubCompleter{content: probeReviewPayload(t, parsed)},
	})

	body := `{"ontology_id":"` + ontologyID + `","objective":"集成探针：审查一笔 1250 元的报销单","business_type":"费用报销"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "integration-probe-"+ontologyID)
	req = req.WithContext(tenantctx.WithIdentity(req.Context(), tenantctx.Identity{
		TenantID: tid, SubjectID: uid,
	}))
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var view struct {
		ID               string          `json:"id"`
		Status           string          `json:"status"`
		Objective        string          `json:"objective"`
		AgentVersionID   *string         `json:"agent_version_id"`
		OntologyVersion  *string         `json:"ontology_version"`
		Plan             json.RawMessage `json:"plan"`
		ReviewConclusion json.RawMessage `json:"review_conclusion"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	createdRunID := view.ID
	// 无论断言是否通过都要清理探针数据：本测试写入的是共享开发库。
	defer func() {
		withTenantTx(t, ctx, pool, tid, func(tx pgx.Tx) error {
			for _, sql := range []string{
				`DELETE FROM runtime.run_steps WHERE run_id = $1::uuid`,
				`DELETE FROM runtime.agent_run_dependencies WHERE run_id = $1::uuid`,
				`DELETE FROM runtime.agent_runs WHERE id = $1::uuid`,
			} {
				if _, err := tx.Exec(ctx, sql, createdRunID); err != nil {
					t.Logf("清理探针数据失败（%s）: %v", sql, err)
				}
			}
			return nil
		})
	}()

	// ---- 视图层：本体绑定字段齐全 ----
	if view.Objective == "" {
		t.Fatal("视图缺少 objective")
	}
	if view.AgentVersionID != nil {
		t.Fatalf("本体驱动 Run 不得带 agent_version_id，实际 %v", *view.AgentVersionID)
	}
	if view.OntologyVersion == nil || *view.OntologyVersion == "" {
		t.Fatal("视图缺少 ontology_version（版本号未解析出来）")
	}
	if len(view.Plan) == 0 || len(view.ReviewConclusion) == 0 {
		t.Fatal("视图缺少 plan / review_conclusion：任务详情页将无处理过程可展示")
	}

	var conclusion struct {
		Decision      string   `json:"decision"`
		Conforms      bool     `json:"conforms"`
		ModelDecision string   `json:"model_decision"`
		Reasons       []string `json:"reasons"`
		GraphIRI      string   `json:"graph_iri"`
	}
	if err := json.Unmarshal(view.ReviewConclusion, &conclusion); err != nil {
		t.Fatalf("审查结论不是合法 JSON: %v", err)
	}
	if conclusion.GraphIRI == "" {
		t.Fatal("审查结论缺少 graph_iri：无法从任务反查到数据图")
	}

	// ---- 核心不变式：结论与引擎判定一致，模型不得覆盖 ----
	if conclusion.Conforms && conclusion.Decision != decisionApprove {
		t.Fatalf("引擎合规但结论不是 APPROVE（实际 %s）", conclusion.Decision)
	}
	if !conclusion.Conforms && conclusion.Decision == decisionApprove {
		t.Fatal("引擎不合规却给出 APPROVE——这正是被禁止的「伪造合规」")
	}
	if conclusion.ModelDecision == decisionApprove && !conclusion.Conforms && conclusion.Decision == decisionApprove {
		t.Fatal("模型 APPROVE 覆盖了 SHACL 的不合规判定")
	}
	wantStatus := "MANUAL_REVIEW"
	if conclusion.Decision == decisionApprove {
		wantStatus = "COMPLETED"
	}
	if view.Status != wantStatus {
		t.Fatalf("结论 %s 应落到状态 %s，实际 %s", conclusion.Decision, wantStatus, view.Status)
	}

	// ---- 落库层：Run + 六个阶段步骤同事务存在 ----
	withTenantTx(t, ctx, pool, tid, func(tx pgx.Tx) error {
		var status string
		var ontologyVersionID *string
		if err := tx.QueryRow(ctx,
			`SELECT status, ontology_version_id::text FROM runtime.agent_runs WHERE id = $1::uuid`,
			createdRunID).Scan(&status, &ontologyVersionID); err != nil {
			return err
		}
		if status != wantStatus {
			t.Errorf("库中状态 %s，期望 %s", status, wantStatus)
		}
		if ontologyVersionID == nil {
			t.Error("库中 ontology_version_id 为空：未建立「任务 → 本体版本」的可追溯绑定")
		}

		var stepCount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM runtime.run_steps WHERE run_id = $1::uuid`, createdRunID).Scan(&stepCount); err != nil {
			return err
		}
		if stepCount != 6 {
			t.Errorf("期望 6 条阶段步骤，实际 %d", stepCount)
		}

		rows, err := tx.Query(ctx,
			`SELECT step_no, step_type, status, result_payload->>'name', result_payload->>'rationale'
             FROM runtime.run_steps WHERE run_id = $1::uuid ORDER BY step_no`, createdRunID)
		if err != nil {
			return err
		}
		defer rows.Close()
		stepNo := 0
		for rows.Next() {
			stepNo++
			var no int
			var stepType, stepStatus string
			var name, rationale *string
			if err := rows.Scan(&no, &stepType, &stepStatus, &name, &rationale); err != nil {
				return err
			}
			if no != stepNo {
				t.Errorf("步骤序号应为 %d，实际 %d", stepNo, no)
			}
			if stepStatus != "SUCCEEDED" {
				t.Errorf("第 %d 步状态 %s，期望 SUCCEEDED", no, stepStatus)
			}
			if name == nil || *name == "" {
				t.Errorf("第 %d 步缺少可读名称（%s）", no, stepType)
			}
			if rationale == nil || *rationale == "" {
				t.Errorf("第 %d 步缺少处理依据（%s）", no, stepType)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}

		var depCount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM runtime.agent_run_dependencies
             WHERE run_id = $1::uuid AND dep_type = 'ONTOLOGY_VERSION'`, createdRunID).Scan(&depCount); err != nil {
			return err
		}
		if depCount != 1 {
			t.Errorf("期望 1 条本体版本锁依赖，实际 %d", depCount)
		}
		return nil
	})
}

// probeReviewPayload 由**真实本体清单**派生一份模型输出骨架。
//
// 取第一个形状的目标类作为焦点实例的类，为其属性按声明类型填示例字面量，为其关系指向
// 的目标类补建实例——这样生成的事实一定落在形状的作用范围内，SHACL 有真实对象可校验
// （反假绿探针也就被真正触发过）。
func probeReviewPayload(t *testing.T, parsed *graphload.Manifest) string {
	t.Helper()
	shape := parsed.Shapes[0]
	focus := shape.TargetClass

	facts := make([]map[string]any, 0, 4)
	props := map[string]any{}
	for _, c := range parsed.Classes {
		if c.Name != focus {
			continue
		}
		for _, a := range c.Attributes {
			props[a.Name] = sampleLiteral(a.Type, parsed)
		}
	}
	// 关系路径：形状里的关系路径必须指向目标类的实例，否则闭包校验会拒绝。
	for _, p := range shape.Properties {
		ref, err := parsed.ResolveTerm(focus, p.Path)
		if err != nil {
			t.Fatalf("形状 %s 的属性路径 %s 无法解析: %v", shape.ID, p.Path, err)
		}
		if !ref.IsRelation {
			continue
		}
		var targetClass string
		for _, r := range parsed.Relations {
			if r.Name == ref.Name {
				targetClass = r.ToClass
			}
		}
		if targetClass == "" {
			t.Fatalf("关系 %s 的 to_class 未找到", ref.Name)
		}
		instID := "probe" + ref.Name
		props[ref.Name] = instID
		facts = append(facts, map[string]any{"class": targetClass, "id": instID, "properties": map[string]any{}})
	}
	// 焦点实例放最前，便于人读。
	facts = append([]map[string]any{{"class": focus, "id": "probeFocus", "properties": props}}, facts...)

	ruleChecks := make([]map[string]any, 0, len(parsed.Shapes))
	for _, s := range parsed.Shapes {
		ruleChecks = append(ruleChecks, map[string]any{
			"shape_id": s.ID, "outcome": ruleSatisfied, "evidence": "集成探针：按声明填满足约束的取值",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"facts":       facts,
		"decision":    decisionApprove,
		"reasons":     []string{"集成探针：业务事实满足该本体的形状约束"},
		"rule_checks": ruleChecks,
	})
	if err != nil {
		t.Fatalf("构造探针模型输出失败: %v", err)
	}
	return string(payload)
}

// sampleLiteral 按属性声明类型给出一个合法的示例字面量。
//
// 数值取 1250（大于常见 min_inclusive 下限），日期取固定日历日——都是确定值，
// 使断言不依赖当前时间。
func sampleLiteral(declaredType string, parsed *graphload.Manifest) string {
	switch strings.ToLower(strings.TrimSpace(declaredType)) {
	case "number", "decimal", "float":
		return "1250.00"
	case "integer", "int":
		return "2"
	case "boolean", "bool":
		return "true"
	case "date":
		return "2026-01-01"
	case "datetime":
		return "2026-01-01T00:00:00Z"
	case "enum":
		if len(parsed.Enumerations) > 0 && len(parsed.Enumerations[0].Values) > 0 {
			return parsed.Enumerations[0].Values[0]
		}
		return "NEW"
	default:
		return "probe"
	}
}
