-- ODAP 平台迁移 000015：修正审计事件摘要的往返一致性
--
-- 缺陷（由集成测试实测暴露）：audit.audit_events.event_payload 是 JSONB，
-- 而 PostgreSQL 的 jsonb 会对写入值做规范化再序列化：
--   - 对象键按「长度 + 字节序」重排（{"b":1,"aa":2} → {"b": 1, "aa": 2}）；
--   - 数字改写为 numeric 的规范表示（1e2 → 100）；
--   - 键值间补一个空格。
-- 因此「写入时对原始载荷算摘要、校验时对 jsonb 回读文本算摘要」必然不一致，
-- 哈希链在真实数据库上无法通过校验（EVIDENCE_INTEGRITY_BROKEN 误报）。
--
-- 修法（不改变 event_payload 的 JSONB 类型与可查询性）：
--   新增 event_payload_canonical TEXT，保存**写入时确定的规范化字节**，
--   事件摘要改为覆盖这一列。event_payload 保留为可查询的 JSONB 索引，
--   并在校验时要求它与规范化副本语义一致（数值按数值比较，容忍 jsonb 的
--   数字改写与键重排），从而任一列被篡改都能被发现。
--
-- 注意：audit_events 是 append-only（BEFORE UPDATE OR DELETE 触发器），
-- ALTER TABLE ADD COLUMN 属于 DDL、不触发行级触发器，因此可安全执行。

ALTER TABLE audit.audit_events
    ADD COLUMN IF NOT EXISTS event_payload_canonical TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN audit.audit_events.event_payload_canonical IS
    '写入时确定的规范化 JSON 字节（键按字节序排序、无多余空白），event_digest 覆盖它；event_payload 为可查询的 JSONB 索引，校验时要求二者语义一致';
