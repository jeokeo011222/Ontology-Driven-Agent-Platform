package agent

import (
	"strings"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

func node(id string, kind NodeKind, deps ...string) Node {
	return Node{ID: id, Kind: kind, Deps: deps}
}

func planOf(nodes ...Node) Plan {
	return Plan{PlanID: ids.New(), TenantID: ids.New(), IntentID: ids.New(), Domain: "FINANCE", Nodes: nodes}
}

func TestValidatePlan(t *testing.T) {
	cases := []struct {
		name     string
		plan     Plan
		wantCode string // "" 表示期望通过
		contains string // 错误信息需包含
	}{
		{
			name: "无环通过",
			plan: planOf(
				node("s", NodeStart),
				node("q", NodeReadTool, "s"),
				node("e", NodeEnd, "q"),
			),
		},
		{
			name: "自环被拒",
			plan: planOf(
				node("s", NodeStart),
				node("a", NodeNoop, "a"),
				node("e", NodeEnd, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "自环",
		},
		{
			name: "2 环被拒并报出环上节点",
			plan: planOf(
				node("s", NodeStart),
				node("a", NodeNoop, "b"),
				node("b", NodeNoop, "a"),
				node("e", NodeEnd, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "环",
		},
		{
			name: "3 环被拒",
			plan: planOf(
				node("s", NodeStart),
				node("a", NodeNoop, "c"),
				node("b", NodeNoop, "a"),
				node("c", NodeNoop, "b"),
				node("e", NodeEnd, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "环",
		},
		{
			name: "悬空依赖被拒",
			plan: planOf(
				node("s", NodeStart),
				node("a", NodeNoop, "missing"),
				node("e", NodeEnd, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "不存在",
		},
		{
			name: "重复节点 ID 被拒",
			plan: planOf(
				node("s", NodeStart),
				node("s", NodeNoop),
				node("e", NodeEnd, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "重复",
		},
		{
			name: "缺少 START 被拒",
			plan: planOf(
				node("a", NodeNoop),
				node("e", NodeEnd, "a"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "START",
		},
		{
			name: "缺少 END 被拒",
			plan: planOf(
				node("s", NodeStart),
				node("a", NodeNoop, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "END",
		},
		{
			name: "未登记节点类型被拒",
			plan: planOf(
				node("s", NodeStart),
				node("x", NodeKind("TELEPORT")),
				node("e", NodeEnd, "s"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "未登记",
		},
		{
			name: "写节点缺少审批门禁被拒",
			plan: planOf(
				node("s", NodeStart),
				func() Node { n := node("w", NodeWriteTool, "s"); n.EffectClass = EffectExternalWrite; return n }(),
				node("e", NodeEnd, "w"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "APPROVAL_GATE",
		},
		{
			name: "写节点未声明 EXTERNAL_WRITE 被拒",
			plan: planOf(
				node("s", NodeStart),
				node("g", NodeApprovalGate, "s"),
				node("w", NodeWriteTool, "g"),
				node("e", NodeEnd, "w"),
			),
			wantCode: errors.CodeValidationFailed,
			contains: "EXTERNAL_WRITE",
		},
		{
			name: "写节点具备审批前驱时通过",
			plan: planOf(
				node("s", NodeStart),
				node("g", NodeApprovalGate, "s"),
				func() Node { n := node("w", NodeWriteTool, "g"); n.EffectClass = EffectExternalWrite; return n }(),
				node("e", NodeEnd, "w"),
			),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePlan(c.plan)
			if c.wantCode == "" {
				if err != nil {
					t.Fatalf("期望通过，got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望错误 %s，got nil", c.wantCode)
			}
			ae := errors.As(err)
			if ae.Code != c.wantCode {
				t.Fatalf("错误码=%s, want %s", ae.Code, c.wantCode)
			}
			if c.contains != "" && !strings.Contains(ae.Message, c.contains) {
				t.Fatalf("错误信息 %q 未包含 %q", ae.Message, c.contains)
			}
		})
	}
}

func TestAllNodeKindsCount(t *testing.T) {
	if len(AllNodeKinds) != 13 {
		t.Fatalf("节点类型数量=%d, want 13（ADR-001 §6 基线）", len(AllNodeKinds))
	}
	seen := map[NodeKind]bool{}
	for _, k := range AllNodeKinds {
		if seen[k] {
			t.Fatalf("节点类型重复：%s", k)
		}
		seen[k] = true
	}
}

func TestCheckDelegationDepth(t *testing.T) {
	for d := 0; d <= MaxDelegationDepth; d++ {
		if err := CheckDelegationDepth(d); err != nil {
			t.Fatalf("深度 %d 应放行，got %v", d, err)
		}
	}
	if err := CheckDelegationDepth(MaxDelegationDepth + 1); errors.As(err).Code != errors.CodePermissionDenied {
		t.Fatalf("超限应返回 PERMISSION_DENIED，got %v", err)
	}
	if err := CheckDelegationDepth(-1); errors.As(err).Code != errors.CodeValidationFailed {
		t.Fatalf("负深度应返回 VALIDATION_FAILED，got %v", err)
	}
}
