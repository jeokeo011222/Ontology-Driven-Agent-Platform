-- ODAP 平台迁移 000031：允许运行期角色创建 graph 租户分区
--
-- 依据：第十三篇 §6.4（graph.triples 按 tenant LIST 分区）与迁移 000009 的注释
--       "新租户的分区由租户初始化流程建立"。
--
-- 缺陷：
--   graphstore.PGStore 的写入路径会在运行期以应用角色执行
--     CREATE TABLE IF NOT EXISTS graph.triples_t_<uuid> PARTITION OF graph.triples …
--   （internal/ontology/graphstore/partition.go:EnsureTenantPartition），
--   但迁移 000011 只给了 odap_app "USAGE ON SCHEMA graph"，没有 CREATE。
--   实测结果为 `ERROR: permission denied for schema graph`，且**分区已存在时同样被拒**
--   （IF NOT EXISTS 不会跳过权限检查）——于是任何租户的图谱写入都失败，
--   本体校验走不到 SHACL 求值，表现为一个"看起来是数据问题"的 500。
--
-- 取舍说明：
--   备选方案是把建分区收敛到一个 SECURITY DEFINER 函数（同 000022 的做法），
--   权限更窄；本次按明确决定直接授予 CREATE，改动最小。
--   注意 CREATE ON SCHEMA 允许在该模式内建表，属较宽权限，仅授予运行期角色 odap_app。
--
-- 只做加法：不改既有对象、不改既有行语义，可重复执行（GRANT 幂等）。

GRANT CREATE ON SCHEMA graph TO odap_app;

COMMENT ON SCHEMA graph IS
    '企业本体知识图谱存储：named_graphs（RLS）+ triples（按 tenant_id LIST 分区）。'
    'odap_app 持有 USAGE 与 CREATE：前者用于读写既有分区，后者用于运行期为新租户建分区（000031）。';
