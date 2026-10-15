-- ODAP 平台迁移 000025：evidence_records 补 append-only 触发器
--
-- 依据：第十一篇（可观测性、审计证据链）——证据记录与审计事件同为"不可变事实"，
--       证据包一旦落库不得被改写或删除，否则审计链的证明力不成立。
--
-- 缺陷（由「全库非内部触发器清点」+「逐表 privilege 核对」暴露）：
--   audit.evidence_records 的防护**不完整**：
--     ✅ 迁移 000011 已 REVOKE UPDATE, DELETE ... FROM odap_app
--        —— 应用角色确实改不动，实测 has_table_privilege('odap_app', ..., 'UPDATE') = false。
--     ❌ 但没有任何触发器，因此**表 owner**（迁移/运维连接使用的 odap）仍可随意
--        UPDATE/DELETE。同一 schema 下的 audit.audit_events 有
--        trg_audit_events_append_only，registry.plugin_versions 有
--        trg_plugin_versions_immutable——evidence_records 是唯一漏掉的"不可变事实表"。
--
-- 本迁移把防护补成与 000007 的 audit_events 完全同构的双保险：
--   权限层（已有）：odap_app 无 UPDATE/DELETE
--   触发器层（本次）：任何角色对已落库行的 UPDATE/DELETE 一律 RAISE EXCEPTION
--
-- 说明：修正/归档不属于应用路径；如确需受控运维操作，应由运维流程显式
-- ALTER TABLE ... DISABLE TRIGGER 并留痕，而不是让应用拥有静默改写的可能。
--
-- 可重复执行（迁移重放安全）。

CREATE OR REPLACE FUNCTION audit.reject_evidence_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION
        'audit: evidence_records is append-only (attempted % on evidence %)',
        TG_OP, OLD.id;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_evidence_records_append_only ON audit.evidence_records;
CREATE TRIGGER trg_evidence_records_append_only
    BEFORE UPDATE OR DELETE ON audit.evidence_records
    FOR EACH ROW
    EXECUTE FUNCTION audit.reject_evidence_mutation();

COMMENT ON TABLE audit.evidence_records IS
    'append-only：odap_app 无 UPDATE/DELETE 权限（000011）+ '
    'trg_evidence_records_append_only 触发器（000025）双保险；'
    '证据记录为不可变事实（第十一篇）。';
