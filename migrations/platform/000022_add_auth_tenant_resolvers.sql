-- ODAP 平台迁移 000022：预认证阶段的租户解析函数
-- 依据：第二篇 §9（RLS 租户隔离）、第三篇 §4.2（身份认证与凭据安全）
--
-- 修复的缺陷：
--   登录与注册都发生在"租户已知之前"，而承载 用户—组织—租户 关系的
--   iam.memberships / iam.organizations 都启用了 FORCE ROW LEVEL SECURITY，
--   策略是 tenant_id = app.current_tenant_id()。预认证阶段 app.current_tenant_id()
--   为空，任何查询都被判成 0 行，于是登录恒返回"用户未加入任何组织，无法登录"、
--   注册恒返回"org_code 对应的组织不存在" —— 平台实际上没有任何可用入口。
--
-- 方案（已与项目作者确认）：
--   不改动任何 RLS 策略、也不给应用角色 BYPASSRLS，只提供两个**窄接口的
--   SECURITY DEFINER 函数**。函数属主是迁移账号（超级用户，可绕过 FORCE RLS，
--   FORCE 只对表属主生效、对超级用户无效），且只回答一个问题：
--   "给定登录名 / 组织码，它属于哪个租户"，不返回任何其他字段，也没有副作用。
--   再 REVOKE ... FROM PUBLIC 并只 GRANT 给 odap_app，把这份解析能力收敛到应用角色。
--
-- 已知的信息面（有意接受，不是疏漏）：能执行该函数的角色可以问出"某登录名/组织码
--   归属哪个租户"。登录流程本就必须做出这个判定才能签发会话，因此这一点无法消除；
--   关键是把它限制在两个只读、单值、无副作用的函数内，而不是放宽整张表的策略。

CREATE OR REPLACE FUNCTION app.resolve_login_tenant(p_login_name text)
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT m.tenant_id
      FROM iam.users u
      JOIN iam.memberships m
        ON m.user_id = u.id
       AND m.status = 'ACTIVE'
     WHERE u.login_name = p_login_name
     LIMIT 1
$$;

CREATE OR REPLACE FUNCTION app.resolve_org_tenant(p_org_code text)
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT tenant_id
      FROM iam.organizations
     WHERE org_code = p_org_code
     LIMIT 1
$$;

-- 函数默认对 PUBLIC 可执行，这里先整体回收，再单独授予运行时应用角色。
REVOKE ALL ON FUNCTION app.resolve_login_tenant(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.resolve_org_tenant(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.resolve_login_tenant(text) TO odap_app;
GRANT EXECUTE ON FUNCTION app.resolve_org_tenant(text) TO odap_app;
