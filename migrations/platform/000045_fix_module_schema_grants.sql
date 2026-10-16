-- ODAP 平台迁移 000045：修复 kb/reason/a2a/action 四 schema 的运行时角色授权。
--
-- 缺陷：000041-000044 对 odap_app 只授予了 CREATE（建表权限），漏授 USAGE。
-- USAGE 是访问 schema 内任何对象的先决条件：缺失时 odap_app 对四模块的全部
-- 读写一律 "permission denied for schema xxx"，表现为列表/详情端点 500
-- （「查询失败」，request_id 可在日志核对）。
--
-- 本迁移幂等：GRANT 本身可重复执行；同时按 000011 的兜底口径补齐表级 DML
-- 与默认权限（防止后续在四 schema 内新建表时再次漏授）。
-- 已在 000041-000044 落过相同授权的全新环境重复执行无副作用。

GRANT USAGE, CREATE ON SCHEMA kb, reason, a2a, action TO odap_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA kb, reason, a2a, action TO odap_app;

ALTER DEFAULT PRIVILEGES IN SCHEMA kb
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odap_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA reason
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odap_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA a2a
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odap_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA action
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odap_app;
