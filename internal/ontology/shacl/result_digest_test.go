package shacl

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestResultDigestPinsTuple 冻结发现项身份的**输入元组**。
//
// 期望值是就地按字面量重算出来的，而不是抄一份常量：这样元组里任何一个字段的顺序、
// 分隔符或取舍发生变化，本测试都会立刻失败并指出"身份元组被改了"。
// 注意这里刻意手写 "\x1f"，不引用 digestSeparator —— 若引用它，改分隔符会连期望值
// 一起改掉，测试就不再守卫任何东西。
func TestResultDigestPinsTuple(t *testing.T) {
	res := Result{
		ShapeID:   "https://odap.example/ont/contract#shape/ContractShape",
		FocusNode: "https://odap.example/ont/contract#inst/C-0001",
		Path:      "https://odap.example/ont/contract#Contract.contract_no",
		Component: "MinCount",
		Message:   "属性取值数为 0，少于 sh:minCount=1",
		Severity:  "Violation",
	}
	sum := sha256.Sum256([]byte(
		"https://odap.example/ont/contract#shape/ContractShape\x1f" +
			"https://odap.example/ont/contract#inst/C-0001\x1f" +
			"https://odap.example/ont/contract#Contract.contract_no\x1f" +
			"MinCount\x1f" +
			"Violation"))
	want := hex.EncodeToString(sum[:])

	if got := res.Digest(); got != want {
		t.Fatalf("发现项身份元组被改动\n got: %s\nwant: %s\n"+
			"若这是有意变更，请同步更新本测试的期望字面量（历史发现项身份将整体失效）", got, want)
	}
}

// TestResultDigestIgnoresMessage 断言措辞不属于身份：润色文案不应被当成"问题变了"。
func TestResultDigestIgnoresMessage(t *testing.T) {
	base := Result{ShapeID: "s", FocusNode: "f", Path: "p", Component: "Pattern", Severity: "Violation"}
	reworded := base
	reworded.Message = "完全换一种说法"
	if base.Digest() != reworded.Digest() {
		t.Fatal("消息措辞不应影响发现项身份（否则每次改文案都会让历史发现项全部变更）")
	}
}

// TestResultDigestSeparatesFields 断言分隔符保证单射：字段边界的挪动必须换一个身份。
//
// 这正是选 ASCII US 而不是 '-' 的原因：后者会让 ("a-b","c") 与 ("a","b-c") 撞车。
func TestResultDigestSeparatesFields(t *testing.T) {
	a := Result{ShapeID: "a-b", FocusNode: "c", Path: "p", Component: "MinCount", Severity: "Violation"}
	b := Result{ShapeID: "a", FocusNode: "b-c", Path: "p", Component: "MinCount", Severity: "Violation"}
	if a.Digest() == b.Digest() {
		t.Fatal("字段边界不同却产出相同身份：分隔符不具备单射性")
	}
}

// TestResultDigestFieldSensitivity 逐个字段确认它参与身份计算（漏掉任何一个都会
// 让两类不同的违规被判成同一个问题）。
func TestResultDigestFieldSensitivity(t *testing.T) {
	base := Result{ShapeID: "s", FocusNode: "f", Path: "p", Component: "MinCount", Severity: "Violation"}
	cases := []struct {
		name   string
		mutate func(Result) Result
	}{
		{"ShapeID", func(r Result) Result { r.ShapeID = "s2"; return r }},
		{"FocusNode", func(r Result) Result { r.FocusNode = "f2"; return r }},
		{"Path", func(r Result) Result { r.Path = "p2"; return r }},
		{"Component", func(r Result) Result { r.Component = "MaxCount"; return r }},
		{"Severity", func(r Result) Result { r.Severity = "Warning"; return r }},
	}
	for _, c := range cases {
		if got := c.mutate(base).Digest(); got == base.Digest() {
			t.Fatalf("字段 %s 未参与身份计算", c.name)
		}
	}
}

// TestResultDigestUsesNativeSeverity 断言参与计算的是 SHACL 原生严重级别，
// 而不是库内映射值（Violation→ERROR）。若两者混用，持久化层换个映射口径
// 就会让全部历史发现项身份失效。
func TestResultDigestUsesNativeSeverity(t *testing.T) {
	native := Result{ShapeID: "s", FocusNode: "f", Path: "p", Component: "MinCount", Severity: "Violation"}
	mapped := native
	mapped.Severity = "ERROR"
	if native.Digest() == mapped.Digest() {
		t.Fatal("身份计算不应取决于库内严重级别映射值")
	}
	// 与字面量一致：确认用的是原生取值本身。
	sum := sha256.Sum256([]byte("s\x1ff\x1fp\x1fMinCount\x1fViolation"))
	if want := hex.EncodeToString(sum[:]); native.Digest() != want {
		t.Fatalf("严重级别未按原生取值参与计算\n got: %s\nwant: %s", native.Digest(), want)
	}
}
