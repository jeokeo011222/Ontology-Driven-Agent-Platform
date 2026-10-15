# 事件 Schema 目录

## 定位

存放**跨模块事件**的机器可读 Schema（JSON Schema），供生产者与消费者在编译期之外
校验兼容性（第十六篇 §10.2「契约测试」：事件 Schema 一致性属于契约层）。

## 收录规范

- 文件名：`<event_type>.schema.json`，例如 `RunCreated.schema.json`。
- 事件类型词表的**权威来源是代码**：`internal/harness` 的 `AllEvents`（31 个），
  由 `tools/contract-lint` 与 `docs/contracts/01` 做逐项一致性校验。
  本目录的 Schema 必须与词表同名，不得引入词表之外的事件类型。
- 每个 Schema 必须声明 `required` 与 `additionalProperties: false`，
  以防生产者静默新增字段而消费者无感。

## 当前状态

**词表已实现，逐事件 Schema 尚未落盘。**

- 已实现：事件词表（31 个）与其一致性门禁。
- 未落地：本目录下的逐事件 JSON Schema 文件。

落地方式建议：由词表与各事件的 Go 结构体导出生成，而非手写——手写必然漂移。
在 Schema 落盘之前，事件形状的真源是 `internal/harness` 中对应的 Go 类型。
