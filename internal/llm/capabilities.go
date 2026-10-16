package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// 本文件是能力档案（llm.capability_profiles）的**唯一**规范化来源。
//
// 能力档案以 capabilities 的规范化 SHA-256 做主键（内容寻址，见迁移 000019）。
// 也就是说"摘要"必须与"内容"逐字节对应，而内容有两条写入路径 ——
// seed 数据集（internal/seed）与界面「模型版本登记」（internal/api/store）。
// 两条路径各写一份 Marshal 逻辑，迟早会漂移：同一份能力算出两个摘要，
// 于是分裂成两条档案，且**谁都不会报错**。因此这里只此一处定义，
// seed 与 store 均复用它。

// CapabilityJSON 返回能力档案的规范化 JSON 字节（与摘要同源，落库即此内容）。
//
// 复用 registry.go 的 capabilitiesJSON（字段名与顺序一致），
// 保证编码与解码两侧共用同一份形状定义。
func CapabilityJSON(c ProviderCapabilities) ([]byte, error) {
	return json.Marshal(capabilitiesJSON{
		NativeSchema:     c.NativeSchema,
		SupportsSeed:     c.SupportsSeed,
		SupportsTools:    c.SupportsTools,
		Embeddings:       c.Embeddings,
		MaxContextTokens: c.MaxContextTokens,
	})
}

// CapabilityDigest 返回能力声明的规范化 SHA-256（内容寻址主键）。
//
// 仅含 bool/int 的结构体 Marshal 不可能失败；真失败说明代码被改坏，
// 此处返回空串让上层立刻报错，而不是写入一个错误的摘要
// （空串不会是任何真实档案的键，因此冲突检测会立刻暴露问题）。
func CapabilityDigest(c ProviderCapabilities) string {
	b, err := CapabilityJSON(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
