package shacl

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// digestSeparator 是摘要各字段之间的分隔符（ASCII US，0x1F）。
//
// 必须用一个**不可能出现在 IRI 里**的字符做分隔：若改用 "-" 或 ":"，形如
// ("a-b","c") 与 ("a","b-c") 两组取值会拼出同一个字符串，摘要就不再是单射，
// 两条不同的违规会被判成同一个问题——而这类缺陷不会报错，只会让回归对比悄悄少算。
const digestSeparator = "\x1f"

// Digest 返回一条发现项的**跨轮次稳定身份**。
//
// 为什么需要它：每轮校验都会重写 ontology.validation_findings，而每行的 id 是随机
// UUID —— 于是"这条违规是上轮就有的、还是本轮新出现的"无法回答，"违规数从 3 降到 2"
// 也无法区分是真修好了还是漏报了一条。摘要把"同一个问题"压成一个确定性字符串，
// 与运行批次、行 id、落库顺序、消息措辞都无关。
//
// 元组固定为 shape_id ‖ focus_node ‖ path ‖ component ‖ severity，三个刻意的取舍：
//   - 用**完整 IRI**（形状 IRI / 焦点节点 IRI / 谓词 IRI）而不是任何前缀名或局部名：
//     前缀映射与局部名都可能在重构中变化，身份不该随之漂移；
//   - **不含 message**：改一句中文措辞不等于"问题变了"，否则每次润色文案都会让
//     全部历史发现项"消失"再"新增"；
//   - **不含 validation_run_id**：跨轮次稳定正是本函数存在的全部理由。
//
// severity 取的是 SHACL 原生严重级别（Violation / Warning / Info），不是库内的
// ERROR/WARNING/INFO 映射值 —— 后者是持久化层的表达，不应参与身份计算。
//
// 改这个元组等于改变**全部历史发现项**的身份，属于不兼容变更：必须同步更新
// TestResultDigestPinsTuple 里冻结的期望值，让改动是显式的而不是顺手的。
func (r Result) Digest() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		r.ShapeID,
		r.FocusNode,
		r.Path,
		r.Component,
		r.Severity,
	}, digestSeparator)))
	return hex.EncodeToString(sum[:])
}
