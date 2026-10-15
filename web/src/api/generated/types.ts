/**
 * api/generated/types.ts —— 契约类型生成占位
 *
 * 权威来源：contracts/openapi.yaml（contracts/03 §1.10 唯一机器可读契约）
 *
 * 该目录用于放置由 OpenAPI 契约生成的 TypeScript 类型与客户端 SDK。
 * 生成流程（§10.4）：
 *   contracts/openapi.yaml ──(openapi-typescript / openapi-fetch)──▶ src/api/generated/types.ts
 * 前端不得自行假设后端枚举、字段和错误代码；新增状态须同步更新
 * contracts/01 → Schema → 后端 → 前端 → 测试集（§10.4 结尾）。
 *
 * 在 openapi.yaml 冻结并接入代码生成前，前端使用的稳定领域模型位于
 * src/types/domain.ts（人工维护，逐项对齐 contracts/01 状态词表）。
 * 本文件刻意保持为空导出，避免出现两份不一致的状态/字段定义。
 */

export {};
