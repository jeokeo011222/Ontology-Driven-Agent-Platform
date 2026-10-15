package harness

import (
	"encoding/json"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/runstate"
)

func testRunID() ids.ID { return ids.MustParse("11111111-1111-4111-8111-111111111111") }

// TestBuildCheckpointResumeStep 表驱动验证恢复点计算。
func TestBuildCheckpointResumeStep(t *testing.T) {
	base := RunSnapshot{
		RunID:            testRunID(),
		Status:           runstate.Running,
		DependencyDigest: "dep-1",
		LastCheckpointNo: 4,
	}
	cases := []struct {
		name   string
		steps  []StepSnapshot
		resume int
		no     int64
	}{
		{"空步骤表", nil, 0, 5},
		{"首步未完成", []StepSnapshot{{0, "RUNNING", ""}, {1, "PENDING", ""}}, 0, 5},
		{"前两步完成", []StepSnapshot{{0, "SUCCEEDED", "a"}, {1, "SUCCEEDED", "b"}, {2, "PENDING", ""}}, 2, 5},
		{"跳过也算完成", []StepSnapshot{{0, "SKIPPED", ""}, {1, "PENDING", ""}}, 1, 5},
		{"全部完成推进到末尾", []StepSnapshot{{0, "SUCCEEDED", "a"}, {1, "SKIPPED", ""}}, 2, 5},
		{"乱序输入仍按编号", []StepSnapshot{{2, "PENDING", ""}, {0, "SUCCEEDED", "a"}, {1, "SUCCEEDED", "b"}}, 2, 5},
		{"失败步骤为恢复点", []StepSnapshot{{0, "SUCCEEDED", "a"}, {1, "FAILED", ""}}, 1, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cp, err := BuildCheckpoint(base, c.steps)
			if err != nil {
				t.Fatalf("BuildCheckpoint error: %v", err)
			}
			if cp.ResumeStepNo != c.resume {
				t.Fatalf("ResumeStepNo = %d, want %d", cp.ResumeStepNo, c.resume)
			}
			if cp.No != c.no {
				t.Fatalf("No = %d, want %d", cp.No, c.no)
			}
			if cp.StateDigest == "" || cp.DependencyDigest != "dep-1" {
				t.Fatalf("checkpoint digests not set: %+v", cp)
			}
			if err := VerifyCheckpoint(cp, "dep-1"); err != nil {
				t.Fatalf("VerifyCheckpoint on freshly built checkpoint failed: %v", err)
			}
		})
	}
}

// TestBuildCheckpointRejectsInvalid 必填项缺失必须拒绝。
func TestBuildCheckpointRejectsInvalid(t *testing.T) {
	if _, err := BuildCheckpoint(RunSnapshot{DependencyDigest: "d"}, nil); err == nil {
		t.Fatal("zero run_id accepted, want error")
	}
	if _, err := BuildCheckpoint(RunSnapshot{RunID: testRunID()}, nil); err == nil {
		t.Fatal("empty dependency_digest accepted, want error")
	}
	if _, err := BuildCheckpoint(RunSnapshot{RunID: testRunID(), DependencyDigest: "d"},
		[]StepSnapshot{{-1, "PENDING", ""}}); err == nil {
		t.Fatal("negative step_no accepted, want error")
	}
}

// TestVerifyCheckpointDriftAndTamper 依赖漂移与内容篡改必须判为不可信。
func TestVerifyCheckpointDriftAndTamper(t *testing.T) {
	cp, err := BuildCheckpoint(RunSnapshot{
		RunID:            testRunID(),
		Status:           runstate.Running,
		DependencyDigest: "dep-1",
	}, []StepSnapshot{{0, "SUCCEEDED", "a"}, {1, "PENDING", ""}})
	if err != nil {
		t.Fatalf("BuildCheckpoint: %v", err)
	}

	// 依赖漂移：当前依赖摘要与检查点不一致。
	err = VerifyCheckpoint(cp, "dep-2")
	if err == nil {
		t.Fatal("dependency drift accepted, want error")
	}
	if code := errors.As(err).Code; code != errors.CodeExecCheckpointCorrupt {
		t.Fatalf("drift error code = %s, want %s", code, errors.CodeExecCheckpointCorrupt)
	}

	// 篡改 state_payload：摘要不匹配。
	tampered := *cp
	var state checkpointState
	if err := json.Unmarshal(cp.StatePayload, &state); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	state.OutputDigest = "forged"
	raw, _ := json.Marshal(state)
	tampered.StatePayload = raw // 保持旧 StateDigest
	if err := VerifyCheckpoint(&tampered, "dep-1"); err == nil {
		t.Fatal("tampered payload accepted, want error")
	}
	if code := errors.As(VerifyCheckpoint(&tampered, "dep-1")).Code; code != errors.CodeExecCheckpointCorrupt {
		t.Fatalf("tamper error code = %s, want %s", code, errors.CodeExecCheckpointCorrupt)
	}

	// nil 与空摘要。
	if err := VerifyCheckpoint(nil, "dep-1"); err == nil {
		t.Fatal("nil checkpoint accepted, want error")
	}
	empty := *cp
	empty.DependencyDigest = ""
	if err := VerifyCheckpoint(&empty, "dep-1"); err == nil {
		t.Fatal("empty dependency_digest accepted, want error")
	}

	// 核对 state_payload 内嵌依赖摘要与信封不一致。
	mismatch := *cp
	var st2 checkpointState
	_ = json.Unmarshal(cp.StatePayload, &st2)
	st2.DependencyDig = "dep-9"
	raw2, _ := json.Marshal(st2)
	mismatch.StatePayload = raw2
	mismatch.StateDigest = digestOf(raw2)
	if err := VerifyCheckpoint(&mismatch, "dep-1"); err == nil {
		t.Fatal("envelope/payload dependency disagreement accepted, want error")
	}

	// 正常路径不报错。
	if err := VerifyCheckpoint(cp, "dep-1"); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
}
