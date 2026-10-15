-- ODAP 平台迁移 000020：iam.credentials（本地密码认证 + 用户查询增强）
-- 依据：第三篇 §4.2 凭据安全；第二篇 §2（组织键查询租户）

-- 本地凭据：bcrypt 哈希存储，永不明文落库。
CREATE TABLE IF NOT EXISTS iam.credentials (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL UNIQUE
        REFERENCES iam.users(id) ON DELETE CASCADE,
    password_hash TEXT NOT NULL,
    password_salt TEXT NOT NULL,
    last_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_credentials_user
    ON iam.credentials(user_id);

-- 租户查询补充：允许按 org_code 反查 tenant（注册流程：用户提供 org_key → 查 tenant）。
-- organizations 表已有 UNIQUE (tenant_id, org_code)，跨租户非唯一；
-- 但 org_code 在平台级通常也被设计为组织的业务唯一标识，这里加一个平台级唯一索引
-- 以支持跨租户直接反查（注册场景用户不需要先知道 tenant_key）。
-- 注意：如果已有数据中存在跨租户重复 org_code，下面语句会失败——此时把
-- 唯一约束降级为普通索引即可（允许 org_code 在同平台内重复）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_organizations_org_code_platform
    ON iam.organizations(org_code);
