# 本体数据集

## 定位

存放用于本体解析、SHACL 验证与推理回归的**固定本体与实例数据**（Turtle / JSON-LD）。

与 `ontology-packages/` 的分工：

- `ontology-packages/` —— 随产品交付的**本体包**（正式资产，带版本与摘要）。
- `datasets/ontology/` —— 仅用于**测试与评估**的本体样例，包含边界用例与反例。

## 收录规范

- 每个数据集一个子目录，含本体文件与实例数据，以及说明预期结果的 `README.md`。
- **反例必须与正例分开存放**：SHACL 回归的价值主要来自"应当校验失败"的样例，
  它们混在正例里会让失败信号失真。
- 标注所用规范版本（RDFS / OWL 子集 / SHACL Core 等）。

## 当前状态

**目录已建立，数据集尚未落盘。**

相关测试当前使用的样例位于各包内（`internal/ontology/shacl/validate_test.go`、
`internal/ontology/rdfs/infer_test.go`、`internal/ontology/semantic/query_test.go`）。
第十六篇 §11.4 的 P2 阶段验收要求"给定固定报销单数据，系统可返回员工、部门、
费用类别与凭证关系；缺少必需关系时产生明确验证发现项"——该验收所需的固定数据集
应落在本目录。
