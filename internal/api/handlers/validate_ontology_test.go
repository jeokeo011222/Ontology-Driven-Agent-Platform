package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"odap/internal/api/store"
	"odap/internal/ontology/graphstore"
	"odap/internal/ontology/shacl"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
	"odap/internal/tools"
)

// validateRequest 构造一个带可信身份的请求（路径参数不设置：本文件测的
// 三条门禁都在解析路径参数之前，正是为了让"未装配依赖"能先于"本体不存在"被报出来）。
func validateRequest(tenantID ids.ID) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ontologies/00000000-0000-0000-0000-000000000000/validate", nil)
	ctx := tenantctx.WithIdentity(req.Context(), tenantctx.Identity{
		TenantID: tenantID, SubjectID: ids.New(),
	})
	return req.WithContext(ctx)
}

// errorCode 解出错误响应里的域码。
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n原始: %s", err, rec.Body.String())
	}
	return body.Error.Code
}

// TestValidateOntology_HonestGates 证明"依赖没装配时给出的是明确的不可用，
// 而不是任何形式的假通过"。
//
// 这三条门禁的顺序本身也是被测对象：身份 → 持久化 → 图存储 → 路径参数。
// 若把图存储检查放到后面，未装配图存储时会先撞上"本体不存在"而返回 404，
// 运维看到的原因就完全错了方向。
func TestValidateOntology_HonestGates(t *testing.T) {
	t.Run("无身份 → 401 UNAUTHENTICATED", func(t *testing.T) {
		h := New(Deps{Store: store.New(nil), GraphStore: nil})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ontologies/x/validate", nil)
		h.ValidateOntology(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("期望 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
		}
	})

	t.Run("持久化未装配 → 503 UNAVAILABLE", func(t *testing.T) {
		h := New(Deps{Store: nil})
		rec := httptest.NewRecorder()
		h.ValidateOntology(rec, validateRequest(ids.New()))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec); code != "UNAVAILABLE" {
			t.Fatalf("期望码 UNAVAILABLE，实际 %s", code)
		}
	})

	t.Run("图存储未装配 → 503 UNAVAILABLE（且必须先于 404 报出）", func(t *testing.T) {
		h := New(Deps{Store: store.New(nil), GraphStore: nil})
		rec := httptest.NewRecorder()
		h.ValidateOntology(rec, validateRequest(ids.New()))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec); code != "UNAVAILABLE" {
			t.Fatalf("期望码 UNAVAILABLE，实际 %s", code)
		}
	})

	t.Run("装配了图存储但无数据库 → 不会返回 200（无假成功）", func(t *testing.T) {
		// Pool 为 nil 的 Store 在真正访问数据库时会失败；这里只断言"绝不返回 2xx"，
		// 以此锁住"校验结论必须来自真实读取"这一性质。
		h := New(Deps{Store: store.New(nil), GraphStore: graphstore.NewMemStore()})
		rec := httptest.NewRecorder()
		h.ValidateOntology(rec, validateRequest(ids.New()))
		if rec.Code >= 200 && rec.Code < 300 {
			t.Fatalf("依赖未就绪时不得返回成功状态，实际 %d（body=%s）", rec.Code, rec.Body.String())
		}
	})
}

// TestOntologyManifestSchemaWithinPlatformSubset 证明平台自己下发给模型的本体清单
// Schema 落在平台支持的 JSON Schema 子集内。
//
// 为什么必须有这条测试：生成回路用 tools.ValidateArgs(req.ResponseSchema, obj) 校验
// 模型输出，而该函数在解析阶段就拒绝未登记关键字（fail-closed）。若平台自己的
// ResponseSchema 用了子集外的关键字（如 minItems），校验会在解析 Schema 时立刻失败，
// 且错误信息指向"模型的输出不合法"——把平台的缺陷伪装成模型的错误。
// 这类问题在没有任何可用模型端点时不会被触发（请求先以 503 挡在网关），
// 因此只在真实端点接通后才暴露，排查成本极高。
func TestOntologyManifestSchemaWithinPlatformSubset(t *testing.T) {
	if err := tools.ValidateSchema([]byte(ontologyManifestSchema)); err != nil {
		t.Fatalf("本体清单 Schema 不在平台支持子集内：%v", err)
	}
}

// TestFindingsFromReport_EveryViolationIsPersisted 证明报告里的每一条违规都能落到库里，
// 且"不合规"必然伴随至少一条阻断级发现项。
//
// 这个不变式是发布门禁的全部意义所在：前端用 blocking 计算能否发布，
// 若 Violation 被映射成非阻断级别，界面会显示"可发布"而 PUT 会被后端拒绝，
// 或者更糟——两边都放行。
func TestFindingsFromReport_EveryViolationIsPersisted(t *testing.T) {
	t.Run("Violation → 阻断级，逐条不丢", func(t *testing.T) {
		report := shacl.Report{
			Conforms: false,
			Results: []shacl.Result{
				{Component: "MinCount", FocusNode: "https://ex/c1", Path: "https://ex/p1",
					Message: "取值数不足", Severity: string(shacl.SeverityViolation)},
				{Component: "Pattern", FocusNode: "https://ex/c1", Path: "https://ex/p2",
					Message: "不匹配正则", Severity: string(shacl.SeverityViolation)},
				{Component: "MaxLength", FocusNode: "https://ex/c1", Path: "https://ex/p3",
					Message: "过长", Severity: string(shacl.SeverityWarning)},
			},
		}
		findings := findingsFromReport(report)
		if len(findings) != len(report.Results) {
			t.Fatalf("发现项必须逐条落库：报告 %d 条，投影出 %d 条", len(report.Results), len(findings))
		}
		var blocking int
		for i, f := range findings {
			if f.Severity == "ERROR" || f.Severity == "CRITICAL" {
				blocking++
			}
			// finding_code 必须是稳定的机器可读标识（Component），不是文案。
			if f.Code != report.Results[i].Component {
				t.Fatalf("第 %d 条 finding_code 应为 %s，实际 %s",
					i, report.Results[i].Component, f.Code)
			}
			// 定位信息必须原样保留：这是前端唯一能展示"哪条数据、哪个字段"的依据。
			if f.FocusNode != report.Results[i].FocusNode || f.Path != report.Results[i].Path {
				t.Fatalf("第 %d 条定位信息丢失：focus=%q path=%q", i, f.FocusNode, f.Path)
			}
			// 跨轮次稳定身份必须随之下发：投影层丢掉摘要，回归对比就无从谈起。
			if f.Digest == "" || f.Digest != report.Results[i].Digest() {
				t.Fatalf("第 %d 条摘要应等于 shacl.Result.Digest()，实际 %q", i, f.Digest)
			}
			var details map[string]any
			if err := json.Unmarshal(f.DetailsJSON, &details); err != nil {
				t.Fatalf("第 %d 条 details_json 非法: %v", i, err)
			}
			if details["component"] != report.Results[i].Component {
				t.Fatalf("第 %d 条 details.component 不符: %v", i, details["component"])
			}
		}
		if report.Conforms == false && blocking == 0 {
			t.Fatal("不合规的报告必须至少产出一条阻断级发现项，否则发布门禁形同虚设")
		}
		if blocking != 2 {
			t.Fatalf("期望 2 条阻断项（Violation 映射为 WARNING 或 Info 都是错的），实际 %d", blocking)
		}
	})

	t.Run("仅 Warning/Info → 无阻断项", func(t *testing.T) {
		report := shacl.Report{
			Conforms: true,
			Results: []shacl.Result{
				{Component: "MinCount", Severity: string(shacl.SeverityWarning), Message: "w"},
				{Component: "Info", Severity: string(shacl.SeverityInfo), Message: "i"},
			},
		}
		for _, f := range findingsFromReport(report) {
			if f.Severity == "ERROR" || f.Severity == "CRITICAL" {
				t.Fatalf("警告/提示级不应产生阻断项，实际 %s", f.Severity)
			}
		}
	})

	t.Run("空报告 → 空切片（且不是 nil，落库循环不会 panic）", func(t *testing.T) {
		got := findingsFromReport(shacl.Report{Conforms: true})
		if got == nil {
			t.Fatal("应返回空切片而非 nil")
		}
		if len(got) != 0 {
			t.Fatalf("空报告不应产生发现项，实际 %d 条", len(got))
		}
	})
}

// TestDbSeverityOf_IsTotal 锁住"未知取值按阻断处理"这一 fail-closed 方向。
func TestDbSeverityOf_IsTotal(t *testing.T) {
	cases := map[string]string{
		string(shacl.SeverityViolation): "ERROR",
		string(shacl.SeverityWarning):   "WARNING",
		string(shacl.SeverityInfo):      "INFO",
		"":                              "ERROR",
		"Unrecognized":                  "ERROR",
	}
	for in, want := range cases {
		if got := dbSeverityOf(in); got != want {
			t.Fatalf("dbSeverityOf(%q) = %q，期望 %q", in, got, want)
		}
	}
}
