-- ============================================================
-- 000018 修正 plugin_versions 不可变守卫的“静默取消”缺陷
-- 依据：第二篇 §4.3（版本不可变性）、第十六篇 §14.1（可重复执行的演示数据）
-- ============================================================
-- 缺陷背景（本轮数据注入时暴露，见自查矩阵缺陷 #12）：
--   000003 定义了
--       CREATE TRIGGER trg_plugin_versions_immutable
--           BEFORE UPDATE OR DELETE ON registry.plugin_versions
--           FOR EACH ROW EXECUTE FUNCTION registry.reject_published_version_mutation();
--   而该函数在“非已发布”分支返回 NULL：
--       IF OLD.status IN ('PUBLISHED','DEPRECATED','DISABLED','REJECTED')
--           THEN RAISE EXCEPTION ... ; END IF;
--       RETURN NULL;
--   在 BEFORE 触发器中，RETURN NULL 的语义是“**取消本行操作**”。于是
--   任何对 registry.plugin_versions 的 DELETE/UPDATE 都被静默取消：
--   不报任何错误、影响 0 行。实测：
--       SELECT count(*) FROM registry.plugin_versions;          -- 2
--       DELETE FROM registry.plugin_versions RETURNING id;      -- 0 行，无错误
--   后果（两类都会发生）：
--     · 演示数据无法重置：草稿状态的插件版本删不掉，seed 的“先清后建”路径失效；
--     · 版本状态流转静默失效：若将来实现 CREATED→APPROVED→PUBLISHED 的 UPDATE，
--       会表面成功而实际未改——典型的静默降级，比报错更危险。
--
-- 正确语义：已发布/终态版本 → RAISE EXCEPTION 明确拒绝；
--           其余状态 → 放行（DELETE 返回 OLD，UPDATE 返回 NEW）。
-- ============================================================

CREATE OR REPLACE FUNCTION registry.reject_published_version_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('PUBLISHED', 'DEPRECATED', 'DISABLED', 'REJECTED') THEN
        RAISE EXCEPTION
            'registry: published plugin_version % is immutable (status=%)',
            OLD.id, OLD.status;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;   -- 非终态版本允许删除
    END IF;
    RETURN NEW;       -- 非终态版本允许更新
END;
$$ LANGUAGE plpgsql;
