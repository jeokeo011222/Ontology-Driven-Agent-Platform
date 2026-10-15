package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 步骤成功态集合：只有处于这些状态的步骤才被视为"已完成、可跳过重放"。
// 与第二篇 runtime.run_steps.status 词表（contracts/01 §2）一致。
var stepCompletedStatuses = map[string]bool{
	"SUCCEEDED": true,
	"SKIPPED":   true,
}

// StepSnapshot 是构建检查点所需的单步快照（来自 runtime.run_steps）。
type StepSnapshot struct {
	StepNo       int
	Status       string
	OutputDigest string
}

// RunSnapshot 是构建检查点所需的 Run 快照（来自 runtime.agent_runs +
// runtime.agent_run_dependencies 汇总的依赖摘要）。
type RunSnapshot struct {
	RunID            ids.ID
	Status           RunState
	OutputDigest     string
	DependencyDigest string
	// LastCheckpointNo 是该 Run 已有检查点的最大编号；新检查点编号为 +1。
	LastCheckpointNo int64
}

// Checkpoint 是一次可恢复的执行快照（第二篇 runtime.checkpoints）。
type Checkpoint struct {
	No               int64
	ResumeStepNo     int
	StatePayload     json.RawMessage
	StateDigest      string
	DependencyDigest string
}

// checkpointState 是 StatePayload 的结构化视图（便于审计与回放）。
type checkpointState struct {
	RunID         string   `json:"run_id"`
	Status        string   `json:"status"`
	CompletedNos  []int    `json:"completed_step_nos"`
	OutputDigest  string   `json:"output_digest"`
	StepOutputs   []stepIO `json:"step_outputs"`
	DependencyDig string   `json:"dependency_digest"`
}

// stepIO 记录已完成步骤的编号与输出摘要，用于恢复时校验重放一致性。
type stepIO struct {
	StepNo int    `json:"step_no"`
	Digest string `json:"output_digest"`
}

// BuildCheckpoint 由 Run 快照与步骤快照构建检查点（纯函数，不触库、不调外部服务）。
//
// 恢复语义（第六篇 §16 检查点与恢复）：
//   - resume_step_no = 第一个未成功完成的步骤编号（SUCCEEDED/SKIPPED 视为已完成）；
//     若全部完成，则为"最大步骤编号 + 1"，表示可推进到收尾阶段；
//   - state_digest = sha256(state_payload)，落库后用于篡改/截断检测；
//   - dependency_digest 固化本次执行所绑定的本体/工具/策略/适配器版本摘要，
//     依赖发生漂移时恢复必须被判为不可信（见 VerifyCheckpoint）。
//
// 本函数对步骤输入顺序不敏感：内部按 step_no 排序后计算。
func BuildCheckpoint(run RunSnapshot, steps []StepSnapshot) (*Checkpoint, error) {
	if run.RunID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "harness: checkpoint run_id must not be zero")
	}
	if run.DependencyDigest == "" {
		return nil, errors.E(errors.CodeValidationFailed,
			"harness: dependency_digest is required (版本锁定是恢复可信度的前提)")
	}

	ordered := make([]StepSnapshot, len(steps))
	copy(ordered, steps)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].StepNo < ordered[j].StepNo })

	var (
		completed = make([]int, 0, len(ordered))
		outputs   = make([]stepIO, 0, len(ordered))
		resume    = 0
		seenFirst = false
		maxNo     = -1
	)
	for _, s := range ordered {
		if s.StepNo < 0 {
			return nil, errors.Ef(errors.CodeValidationFailed,
				"harness: negative step_no %d", s.StepNo)
		}
		if s.StepNo > maxNo {
			maxNo = s.StepNo
		}
		if stepCompletedStatuses[s.Status] {
			completed = append(completed, s.StepNo)
			outputs = append(outputs, stepIO{StepNo: s.StepNo, Digest: s.OutputDigest})
			continue
		}
		if !seenFirst {
			resume = s.StepNo
			seenFirst = true
		}
	}
	if !seenFirst {
		// 全部步骤已完成：恢复点推进到末尾之后。
		resume = maxNo + 1
	}

	state := checkpointState{
		RunID:         run.RunID.String(),
		Status:        run.Status,
		CompletedNos:  completed,
		OutputDigest:  run.OutputDigest,
		StepOutputs:   outputs,
		DependencyDig: run.DependencyDigest,
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "harness: marshal checkpoint state")
	}
	return &Checkpoint{
		No:               run.LastCheckpointNo + 1,
		ResumeStepNo:     resume,
		StatePayload:     payload,
		StateDigest:      digestOf(payload),
		DependencyDigest: run.DependencyDigest,
	}, nil
}

// VerifyCheckpoint 校验检查点在当前依赖版本下是否可信（恢复前置检查）。
//
// 判为不可信（CodeExecCheckpointCorrupt）的情形：
//   - 检查点为空；
//   - state_payload 无法解析，或 state_digest 与重算结果不符（被篡改/截断）；
//   - dependency_digest 与当前运行依赖摘要不一致（本体/工具/策略/适配器版本
//     已漂移，按旧检查点继续执行会用错误的语义与版本推进，必须拒绝）。
func VerifyCheckpoint(cp *Checkpoint, currentDependencyDigest string) error {
	if cp == nil {
		return errors.E(errors.CodeExecCheckpointCorrupt, "harness: nil checkpoint")
	}
	if cp.DependencyDigest == "" {
		return errors.E(errors.CodeExecCheckpointCorrupt, "harness: checkpoint missing dependency_digest")
	}
	if cp.DependencyDigest != currentDependencyDigest {
		return errors.Ef(errors.CodeExecCheckpointCorrupt,
			"harness: dependency drift: checkpoint=%s current=%s",
			cp.DependencyDigest, currentDependencyDigest)
	}
	if len(cp.StatePayload) == 0 {
		return errors.E(errors.CodeExecCheckpointCorrupt, "harness: empty checkpoint state payload")
	}
	if got := digestOf(cp.StatePayload); got != cp.StateDigest {
		return errors.Ef(errors.CodeExecCheckpointCorrupt,
			"harness: state digest mismatch: stored=%s recomputed=%s", cp.StateDigest, got)
	}
	var state checkpointState
	if err := json.Unmarshal(cp.StatePayload, &state); err != nil {
		return errors.Wrap(err, errors.CodeExecCheckpointCorrupt, "harness: unparsable checkpoint state")
	}
	if state.DependencyDig != cp.DependencyDigest {
		return errors.E(errors.CodeExecCheckpointCorrupt,
			"harness: checkpoint payload and envelope dependency digests disagree")
	}
	return nil
}

// digestOf 返回内容的 sha256 十六进制摘要（state_digest 的计算方式，
// 与第二篇 runtime.checkpoints.state_digest 约定一致）。
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
