-- ODAP 平台迁移 000032：把 graph.triples 的所有权转移给运行期角色 odap_app
--
-- 依据：第十三篇 §6.4（graph.triples 按 tenant LIST 分区）；运行时由应用以
--       CREATE TABLE … PARTITION OF … 为每个租户建分区
--       （internal/ontology/graphstore/partition.go:EnsureTenantPartition）。
--
-- 缺陷链（实测确认，逐层暴露）：
--   ① 000011 只给 odap_app "USAGE ON SCHEMA graph"  → `ERROR: permission denied for schema graph`；
--   ② 补授 CREATE（000031）后仍失败          → `ERROR: must be owner of table triples`。
--   PostgreSQL 要求"建分区者必须是分区父表的所有者"，与模式级 CREATE 权限无关，
--   因此仅靠 GRANT 无法打通，必须转移父表所有权。
--
-- 取舍说明：
--   备选方案是用 SECURITY DEFINER 函数（同 000022），权限更窄；本次按明确决定直接
--   转移所有权。须知：此后 odap_app 成为 graph.triples 的 owner，因而对该表拥有
--   DROP 等 owner 级权限（000031 的 CREATE 仍需保留——建分区同时要求模式 CREATE
--   与父表所有权）。
--   odap 为超级用户，故后续迁移仍可照常 ALTER 本表。
--
-- 只做加法性质的元数据变更：不改任何列、不改任何既有行语义，可重复执行
-- （对已是目标所有者的表重复执行是幂等的）。

ALTER TABLE graph.triples OWNER TO odap_app;

COMMENT ON TABLE graph.triples IS
    '企业本体三元组事实表，按 tenant_id LIST 分区（第十三篇 §6.4）。'
    'owner=odap_app 使运行期可为新租户建分区（000032）；读写的 DML 授权见 000011。';
