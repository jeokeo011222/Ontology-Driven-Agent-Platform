# JSON Schema 目录

## 定位

存放**非 HTTP、非事件**类结构化输出的 Schema，主要面向：

- LLM 结构化输出（第九篇：模型结构化输出的形状与校验）；
- 工具（Tool）的受控参数与返回结构（第十四篇：Action Contract 的版本化资产）；
- Policy DSL 与 Plan IR 的 Schema（ADR-001）。

## 收录规范

- 文件名以用途前缀区分：`llm.<name>.schema.json`、`tool.<name>.schema.json`、
  `planir.<name>.schema.json`。
- 必须自包含（可被 `$ref` 解析到同目录内文件），不得引用本仓库之外的 URL。
- 与 Go 类型一一对应时，应在文件头注明对应的包与类型名，便于同步修改。

## 当前状态

**目录已按第十六篇 §4.1 建立，Schema 文件尚未落盘。**

相关内容当前的真源位置：

| 内容 | 当前真源 |
| --- | --- |
| Plan IR | `internal/agent` 的 Plan IR 定义 + ADR-001 |
| 工具受控参数 | `internal/tools` 的参数校验实现 |
| LLM 结构化输出 | `internal/llm` 的结构化输出处理 |
| Action Contract | Registry 的版本化资产（第四篇 + ADR-002） |
