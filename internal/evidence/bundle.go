// Package evidence 实现业务证据包的构建与完整性验证，以及证据记录持久化。
//
// 权威来源：docs/第十一篇 §10（Evidence Service：从运行记录生成业务证据包）、
// §11.1（验证分层第一层：文件/分项完整性）。
//
// 证据包把一次业务结果所依赖的输入、事实、规则、模型、审批与外部回执组合成
// 一份可追溯、可复核、可验证的集合。本包实现分项摘要与包摘要的确定性计算：
// 分项顺序不影响包摘要（按 Type+SourceRef 排序后计算），任一分项被篡改都会
// 使包摘要不匹配。
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// 分隔符：0x1f 分隔同一分项内的字段，0x1e 连接相邻分项的记录。
const (
	itemSep  = 0x1e
	fieldSep = 0x1f
)

// EvidenceItem 是一条证据分项（对应 audit.evidence_records 一行）。
type EvidenceItem struct {
	Type          string
	SourceType    string
	SourceRef     string
	ContentDigest string
	StorageURI    string
	Summary       string
	Metadata      json.RawMessage

	// 时间轴三列（000039）：决策一等节点模型的落点。
	// DecisionType 空表示暂未分类；ToolName 空表示根记录、非空表示步骤记录。
	DecisionType string
	TaskID       string
	ToolName     string
}

// decisionTypes 是决策类型词表（借鉴竞品五分类，fail-closed 收口）。
// 迁移 000039 的 CHECK 约束持有同一份词表——两侧必须同步改。
var decisionTypes = map[string]bool{
	"QUERY":  true, // 问答
	"UPDATE": true, // 知识更新
	"INFER":  true, // 规则推断
	"ACTION": true, // SOP/技能执行
	"TOOL":   true, // 工具调用（步骤记录）
}

// Bundle 是一次运行（Run）的证据包。
type Bundle struct {
	TenantID     ids.ID
	RunID        ids.ID
	Items        []EvidenceItem
	BundleDigest string
	CreatedAt    time.Time
}

// sortItems 按 (Type, SourceRef) 稳定排序，返回新切片，保证包摘要与分项顺序无关。
func sortItems(items []EvidenceItem) []EvidenceItem {
	out := make([]EvidenceItem, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].SourceRef < out[j].SourceRef
	})
	return out
}

// bundleDigest 计算证据包摘要：
//
//	SHA-256( 逐项 Type || 0x1f || SourceRef || 0x1f || ContentDigest，
//	         相邻分项之间以 0x1e 连接 )
//
// 分项先按 (Type, SourceRef) 排序，因此顺序不同但集合相同的分项产生相同摘要。
func bundleDigest(items []EvidenceItem) string {
	ordered := sortItems(items)
	var b strings.Builder
	for i, it := range ordered {
		if i > 0 {
			b.WriteByte(itemSep)
		}
		b.WriteString(it.Type)
		b.WriteByte(fieldSep)
		b.WriteString(it.SourceRef)
		b.WriteByte(fieldSep)
		b.WriteString(it.ContentDigest)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// BuildBundle 构建证据包：排序分项、校验每项 ContentDigest 非空并计算包摘要。
//
// 任一分项 ContentDigest 为空 → CodeEvidenceIntegrityBroken（分项缺少摘要
// 意味着无法证明其完整性，不允许进入证据包）。
func BuildBundle(tenantID, runID ids.ID, items []EvidenceItem, now time.Time) (*Bundle, error) {
	if tenantID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "evidence: tenantID must not be zero")
	}
	if runID.IsZero() {
		return nil, errors.E(errors.CodeValidationFailed, "evidence: runID must not be zero")
	}
	for i, it := range items {
		if it.ContentDigest == "" {
			return nil, errors.Ef(errors.CodeEvidenceIntegrityBroken,
				"evidence: 第 %d 个分项缺少 content_digest（Type=%s SourceRef=%s）", i, it.Type, it.SourceRef)
		}
		if it.DecisionType != "" && !decisionTypes[it.DecisionType] {
			return nil, errors.Ef(errors.CodeValidationFailed,
				"evidence: 第 %d 个分项的 decision_type %q 不在词表内（QUERY/UPDATE/INFER/ACTION/TOOL）", i, it.DecisionType)
		}
		if it.ToolName != "" && it.DecisionType == "" {
			return nil, errors.Ef(errors.CodeValidationFailed,
				"evidence: 第 %d 个分项填写了 tool_name 但未分类 decision_type（步骤记录必须标注 TOOL）", i)
		}
	}
	ordered := sortItems(items)
	return &Bundle{
		TenantID:     tenantID,
		RunID:        runID,
		Items:        ordered,
		BundleDigest: bundleDigest(items),
		CreatedAt:    now,
	}, nil
}

// VerifyBundle 重算并比对包摘要；任一分项 ContentDigest 为空或整体不符 →
// CodeEvidenceIntegrityBroken。
func VerifyBundle(b *Bundle) error {
	if b == nil {
		return errors.E(errors.CodeEvidenceIntegrityBroken, "evidence: nil bundle")
	}
	for i, it := range b.Items {
		if it.ContentDigest == "" {
			return errors.Ef(errors.CodeEvidenceIntegrityBroken,
				"evidence: 第 %d 个分项缺少 content_digest", i)
		}
	}
	got := bundleDigest(b.Items)
	if got != b.BundleDigest {
		return errors.Ef(errors.CodeEvidenceIntegrityBroken,
			"evidence: 包摘要不匹配（存储 %q，重算 %q）", b.BundleDigest, got)
	}
	return nil
}
