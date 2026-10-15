-- ============================================================
-- 000017 补齐应用角色 odap_app 的登录密码
-- 依据：第二篇 §9.2（运行时应用角色必须非超级用户、NOBYPASSRLS）
--       第十六篇 §8.2/§8.3（compose 最小启动路径与 .env 约定）
-- ============================================================
-- 缺陷背景（真实启动阻断，见自查矩阵缺陷 #7）：
--   000011 以
--       CREATE ROLE odap_app LOGIN NOBYPASSRLS NOSUPERUSER;
--   创建了应用角色，但**从未设置密码**。而 postgres 官方镜像对 TCP 连接默认
--   追加 `host all all all scram-sha-256`，运行时（api/worker）按 .env.example
--   以 `postgres://odap_app:<pw>@postgres:5432/odap` 连接时，必然报
--       FATAL: password authentication failed for user "odap_app"
--   使 `docker compose up -d --build` 无法拉起 api/worker。
--
-- 密码来源（单一真值来源 = 运行时连接串 ODAP_DATABASE_URL）：
--   cmd/migrate 在执行迁移前解析 ODAP_DATABASE_URL；仅当其用户名为 odap_app 时，
--   把连接串中的密码通过
--       SELECT set_config('odap.app_password', $1, false)
--   注入本次会话的 GUC；本迁移读取该 GUC 并 ALTER ROLE 落库。
--   · 未注入（例如直接 psql 执行迁移、或运行时用 owner 账号连接）时，
--     回退为本地开发默认值 'apppw'，与 .env.example 保持一致，保证开箱即用。
--   · 生产环境必须通过 ODAP_DATABASE_URL 提供强密码；§8.3 明确“正式环境不得
--     照搬开发环境的凭据与 sslmode=disable”。
-- 幂等性：对同一角色重设同一密码，重复执行结果等价。
-- 说明：本迁移只在角色存在时执行；若 000011 缺失（不应发生）则显式报错，
--       而非静默跳过，避免“看似成功但运行时仍连不上”的隐性失败。
-- ============================================================

DO $$
DECLARE
    pw text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'odap_app') THEN
        RAISE EXCEPTION 'odap_app role not found; migration 000011 must be applied first';
    END IF;

    -- current_setting(..., true) 对未设置的会话 GUC 返回 NULL（不报错）
    pw := current_setting('odap.app_password', true);
    IF pw IS NULL OR pw = '' THEN
        pw := 'apppw';
        RAISE NOTICE 'odap.app_password not injected; using local-dev default for odap_app';
    END IF;

    -- %L 做字面量转义，避免注入；ALTER ROLE 需超级用户/CREATEROLE（odap owner 满足）
    EXECUTE format('ALTER ROLE odap_app WITH LOGIN PASSWORD %L', pw);
END $$;

COMMENT ON ROLE odap_app IS
    'ODAP 运行时应用角色：非超级用户、NOBYPASSRLS，RLS 对其真实生效（第二篇 §9.2）。'
    '密码由迁移 000017 依据 ODAP_DATABASE_URL 设置。';
