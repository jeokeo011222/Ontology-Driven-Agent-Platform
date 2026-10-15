package agent

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// assertCode 断言 err 是指定错误码的 AppError（fail-closed 语义验证用）。
func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got nil error", code)
	}
	var appErr *errors.AppError
	if !stderrors.As(err, &appErr) {
		t.Fatalf("want AppError, got %T: %v", err, err)
	}
	if appErr.Code != code {
		t.Fatalf("want code %s, got %s (%v)", code, appErr.Code, err)
	}
}

// fakeSkillLoader 按 key→snapshot 命中表返回；未命中的键不出现在结果里
// （与生产 resolver 的「缺失即缺行」语义一致，由 CompileSkills fail-closed）。
type fakeSkillLoader struct {
	byKey map[string]SkillSnapshot
	err   error
}

func (f fakeSkillLoader) PublishedSkillVersions(ctx context.Context, tenantID ids.ID, skillKeys []string) ([]SkillSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]SkillSnapshot, 0, len(skillKeys))
	for _, k := range skillKeys {
		if s, ok := f.byKey[k]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func TestCompileSkillsEmptyManifestIsNoop(t *testing.T) {
	exp, err := CompileSkills(context.Background(), fakeSkillLoader{}, ids.New(), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(exp.PromptSections) != 0 || len(exp.ToolKeys) != 0 || exp.Digest != "" {
		t.Fatalf("empty expansion expected, got %+v", exp)
	}
	if got := exp.ApplyToPrompt("sys"); got != "sys" {
		t.Fatalf("prompt must be unchanged, got %q", got)
	}
}

func TestCompileSkillsExpandsPromptAndTools(t *testing.T) {
	loader := fakeSkillLoader{byKey: map[string]SkillSnapshot{
		"invoice_query": {
			SkillKey: "invoice_query", Name: "发票查询", Version: "1.2.0", VersionID: "v2",
			PromptFragment: "查询发票时必须先核对企业税号。", ToolBindings: []string{"erp.invoice.query"},
			ContentDigest: "digest-a",
		},
		"credit_report": {
			SkillKey: "credit_report", Name: "征信报告", Version: "0.3.0", VersionID: "v1",
			PromptFragment: "输出前必须声明数据截止日期。", ToolBindings: []string{"credit.report.pull", "erp.invoice.query"},
			ContentDigest: "digest-b",
		},
	}}
	exp, err := CompileSkills(context.Background(), loader, ids.New(), []string{"invoice_query", "credit_report"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 小节按 skill_key 升序，含「## 技能：{name}」标题与 prompt_fragment。
	if len(exp.PromptSections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(exp.PromptSections))
	}
	if !strings.HasPrefix(exp.PromptSections[0], "## 技能：征信报告") ||
		!strings.Contains(exp.PromptSections[0], "数据截止日期") {
		t.Fatalf("unexpected first section: %q", exp.PromptSections[0])
	}
	if !strings.HasPrefix(exp.PromptSections[1], "## 技能：发票查询") {
		t.Fatalf("unexpected second section: %q", exp.PromptSections[1])
	}
	// 工具键并集去重升序。
	wantTools := []string{"credit.report.pull", "erp.invoice.query"}
	if len(exp.ToolKeys) != len(wantTools) {
		t.Fatalf("tool keys = %v, want %v", exp.ToolKeys, wantTools)
	}
	for i := range wantTools {
		if exp.ToolKeys[i] != wantTools[i] {
			t.Fatalf("tool keys = %v, want %v", exp.ToolKeys, wantTools)
		}
	}
	// 提示词合并：原提示词在前，技能小节追加。
	merged := exp.ApplyToPrompt("你是财务助手。")
	if !strings.HasPrefix(merged, "你是财务助手。") || !strings.Contains(merged, "## 技能：发票查询") {
		t.Fatalf("prompt merge broken: %q", merged)
	}
	// 工具白名单并入。
	got := exp.MergeToolKeys([]string{"erp.customer.get", "erp.invoice.query"})
	if len(got) != 3 || got[0] != "credit.report.pull" || got[1] != "erp.customer.get" || got[2] != "erp.invoice.query" {
		t.Fatalf("merged tools = %v", got)
	}
}

func TestCompileSkillsFailsClosedOnMissingOrUnpublished(t *testing.T) {
	loader := fakeSkillLoader{byKey: map[string]SkillSnapshot{
		"published_only": {SkillKey: "published_only", Name: "已发布", Version: "1.0.0", ContentDigest: "d"},
	}}
	tenantID := ids.New()
	// 不存在的技能。
	_, err := CompileSkills(context.Background(), loader, tenantID, []string{"published_only", "ghost_skill"})
	if err == nil {
		t.Fatal("missing skill must fail compilation")
	}
	assertCode(t, err, errors.CodeNotFound)
	if !strings.Contains(err.Error(), "ghost_skill") {
		t.Fatalf("error must name the offending skill: %v", err)
	}
	// 未发布（resolver 不返回该键）同样拒绝。
	_, err = CompileSkills(context.Background(), loader, tenantID, []string{"draft_skill"})
	assertCode(t, err, errors.CodeNotFound)
	// loader 底层故障不允许静默降级。
	_, err = CompileSkills(context.Background(), fakeSkillLoader{err: context.DeadlineExceeded}, tenantID, []string{"any"})
	if err == nil {
		t.Fatal("loader failure must propagate")
	}
}

func TestSkillDigestInvalidatesVersionLockOnSkillVersionChange(t *testing.T) {
	lock := VersionLock{OntologyVersion: "onto-1", ToolBindingDigest: "tools-digest"}
	// 同一技能版本 → 锁定摘要稳定。
	d1 := lock.BindSkills("skills-digest-1")
	d2 := lock.BindSkills("skills-digest-1")
	if d1.ToolBindingDigest != d2.ToolBindingDigest {
		t.Fatalf("digest must be deterministic for same inputs")
	}
	if d1.ToolBindingDigest == lock.ToolBindingDigest {
		t.Fatal("skill digest must change the lock digest")
	}
	// 技能版本变更 → 摘要变化（计划缓存失效）。
	d3 := lock.BindSkills("skills-digest-2")
	if d3.ToolBindingDigest == d1.ToolBindingDigest {
		t.Fatal("skill version change must invalidate plan cache digest")
	}
	// 无技能依赖 → 原样返回，既有内置路径不受影响。
	same := lock.BindSkills("")
	if same.ToolBindingDigest != lock.ToolBindingDigest || same.OntologyVersion != lock.OntologyVersion {
		t.Fatalf("empty skill digest must not alter lock: %+v", same)
	}
}

func TestCompileSkillsDigestTracksContentAndVersion(t *testing.T) {
	tenantID := ids.New()
	// content_digest 在生产侧覆盖 prompt_fragment（digestBytes 含全部版本内容），
	// 假实现按同样口径构造：内容或版本任一变化 → content_digest 变化。
	mk := func(frag, version string) fakeSkillLoader {
		return fakeSkillLoader{byKey: map[string]SkillSnapshot{
			"invoice_query": {SkillKey: "invoice_query", Name: "发票查询", Version: version,
				PromptFragment: frag, ContentDigest: "d-" + version + "-" + frag},
		}}
	}
	e1, err := CompileSkills(context.Background(), mk("A", "1.0.0"), tenantID, []string{"invoice_query"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	e2, err := CompileSkills(context.Background(), mk("B", "1.0.0"), tenantID, []string{"invoice_query"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	e3, err := CompileSkills(context.Background(), mk("A", "2.0.0"), tenantID, []string{"invoice_query"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if e1.Digest == e2.Digest {
		t.Fatal("prompt_fragment change must change expansion digest")
	}
	if e1.Digest == e3.Digest {
		t.Fatal("version bump must change expansion digest")
	}
}
