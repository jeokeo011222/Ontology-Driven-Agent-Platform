package reasoning

// 后向链测试：规则求证、变量绑定、环剪枝。

import (
	"testing"

	"odap/internal/ontology/graphstore"
)

// TestBackwardChainProveViaRule 验证经规则求证（rdfs-subprop-value）：
// p ⊑ q 且 x p y ⇒ 目标 (x q y) 可证，且证明轨迹包含规则应用。
func TestBackwardChainProveViaRule(t *testing.T) {
	const (
		subPropOf = IRIRDFSSubPropertyOf
		P         = "https://odap.example/ont/demo#p"
		Q         = "https://odap.example/ont/demo#q"
		x         = "https://odap.example/data/x"
		y         = "https://odap.example/data/y"
	)
	facts := []graphstore.Triple{
		t3(P, subPropOf, Q),
		t3(x, P, y),
	}
	res, err := RunBackward(BuiltinRules(), facts, TriplePattern{Subject: x, Predicate: Q, Object: y})
	if err != nil {
		t.Fatalf("RunBackward 失败：%v", err)
	}
	if !res.Proved {
		t.Fatal("目标 (x q y) 应可证")
	}
	found := false
	for _, step := range res.ProofTrace {
		if step.RuleKey == "rdfs-subprop-value" {
			found = true
		}
	}
	if !found {
		t.Errorf("证明轨迹应包含 rdfs-subprop-value，实际 %d 步：%v", len(res.ProofTrace), res.ProofTrace)
	}
}

// TestBackwardChainVariableGoal 验证含变量的目标返回绑定解。
func TestBackwardChainVariableGoal(t *testing.T) {
	const (
		subClassOf = IRIRDFSSubClassOf
		typ        = IRIRDFType
		A          = "https://odap.example/ont/demo#A"
		B          = "https://odap.example/ont/demo#B"
		i          = "https://odap.example/data/i1"
	)
	facts := []graphstore.Triple{
		t3(A, subClassOf, B),
		t3(i, typ, A),
	}
	// 目标 (?x type B)：应给出 ?x = i 的绑定解。
	res, err := RunBackward(BuiltinRules(), facts, TriplePattern{Subject: "?x", Predicate: typ, Object: B})
	if err != nil {
		t.Fatalf("RunBackward 失败：%v", err)
	}
	if !res.Proved || len(res.Bindings) == 0 {
		t.Fatalf("目标应可证且有绑定解，实际 proved=%v bindings=%v", res.Proved, res.Bindings)
	}
	found := false
	for _, b := range res.Bindings {
		if b["x"] == i {
			found = true
		}
	}
	if !found {
		t.Errorf("绑定解应包含 x=%s，实际 %v", i, res.Bindings)
	}
}

// TestBackwardChainCyclePruning 验证环剪枝：sameAs 环状图上的传递规则
// 递归求证必须在同一目标复现时剪枝（测试挂起即失败），且仍能给出证明。
func TestBackwardChainCyclePruning(t *testing.T) {
	const (
		sameAs = IRIOWLSameAs
		a      = "https://odap.example/data/a"
		b      = "https://odap.example/data/b"
	)
	// 环状事实：a sameAs b、b sameAs a（对称规则会自我增殖，配合传递规则
	// 在朴素实现下极易无限递归）。
	facts := []graphstore.Triple{
		t3(a, sameAs, b),
		t3(b, sameAs, a),
	}
	done := make(chan *BackwardResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := RunBackward(BuiltinRules(), facts, TriplePattern{Subject: a, Predicate: sameAs, Object: "?z"})
		if err != nil {
			errCh <- err
			return
		}
		done <- res
	}()
	var res *BackwardResult
	select {
	case res = <-done:
	case err := <-errCh:
		t.Fatalf("RunBackward 失败：%v", err)
	}
	if !res.Proved {
		t.Fatal("目标 (a sameAs ?z) 应可证")
	}
	// 基础事实解（z=b）必须存在。注意：按任务契约"同一目标在当前证明栈
	// 出现即剪枝"，经规则递归推出的解（如 z=a，需 sameAs 传递规则在开放
	// 目标上递归）会被变体环剪枝裁掉——这是环剪枝的既定语义（保终止性
	// 优先于开放目标的解完整性；闭合目标的可证性不受影响）。
	seen := map[string]bool{}
	for _, bd := range res.Bindings {
		seen[bd["z"]] = true
	}
	if !seen[b] {
		t.Errorf("绑定解应包含 z=%s，实际 %v", b, res.Bindings)
	}
}

// TestBackwardChainUnprovable 验证不可证目标返回 proved=false。
func TestBackwardChainUnprovable(t *testing.T) {
	const (
		typ   = IRIRDFType
		Other = "https://odap.example/ont/demo#Other"
		x     = "https://odap.example/data/x"
	)
	facts := []graphstore.Triple{t3(x, typ, "https://odap.example/ont/demo#A")}
	res, err := RunBackward(BuiltinRules(), facts, TriplePattern{Subject: x, Predicate: typ, Object: Other})
	if err != nil {
		t.Fatalf("RunBackward 失败：%v", err)
	}
	if res.Proved {
		t.Fatal("无事实与规则支持时目标不应可证")
	}
	if len(res.Bindings) != 0 {
		t.Errorf("不可证目标不应有绑定解，实际 %v", res.Bindings)
	}
}
