# OpenAPI 拆分目录（预留）

## 现状

对外契约当前是**单文件**：`contracts/openapi.yaml`（OpenAPI 3.1，58 个 path / 87 个 operation）。

本目录为**按域拆分**预留。当前不需要拆分，理由：

- 单文件保证 `$ref` 解析与 `tests/contract` 的规范性校验简单可靠；
- 拆分会引入 `$ref` 跨文件解析问题，而收益（可读性）目前不足以抵消成本。

## 何时应启用本目录

满足任一条件时，将 `openapi.yaml` 按域拆到本目录，并在根文件用 `$ref` 聚合：

1. 单文件超过约 3000 行，评审 diff 已难以阅读；
2. 出现多团队并行编辑导致频繁冲突；
3. 需要按域独立发布 API 文档版本。

## 拆分时的硬约束

- 根文件保留 `info` / `servers` / `security` / `tags` 与所有 `$ref` 聚合；
- 拆分后必须仍通过 `tests/contract` 的全部断言（operationId 唯一、
  受保护端点声明 4xx、path 键不重复、`$ref` 不悬空）；
- 必须更新 `internal/api/contract_test.go` 的路径解析逻辑（当前按缩进解析单文件）。

> 注意：`tools/contract-lint` 与 `internal/api/contract_test.go` 都以当前单文件布局为前提，
> 拆分时必须同步修改这两处，否则门禁会静默失效。
