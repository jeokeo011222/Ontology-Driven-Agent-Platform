-- ============================================================
-- 000029 模型端点凭据：支持「界面录入 + 密文落库」
-- 依据：需求「模型与推理」改造设计要求 3（api-key 加密存储与显示）。
--
-- 背景：000010 的 llm.endpoints 只有 credential_ref（环境变量引用名），
--   实际密钥必须预先注入进程环境。用户在界面上无法录入密钥，只能"先去改
--   部署环境再回来"。本迁移新增密文凭据列，使密钥可在界面录入并被加密存储。
--
-- 两条路径并存（CHECK 保证二选一）：
--   ① credential_ref          —— 环境变量引用名（存量路径，向后兼容）
--   ② credential_ciphertext   —— AES-256-GCM 信封密文
--      （version(1B)||nonce(12B)||ct+tag，AAD 绑定 endpoint_id；
--       见 internal/platform/secretbox）
--
-- 明文绝不进入 SQL 参数：加密发生在 store 层，进入本表的只有密文。
-- （PostgreSQL 会记录语句参数 —— log_statement / pg_stat_statements /
--   pg_stat_activity 都能看到，故明文必须先于 SQL 消失。）
-- ============================================================

ALTER TABLE llm.endpoints
    ADD COLUMN IF NOT EXISTS credential_ciphertext BYTEA,
    ADD COLUMN IF NOT EXISTS credential_masked     TEXT,
    ADD COLUMN IF NOT EXISTS credential_updated_at TIMESTAMPTZ;

-- ---------- 约束 1：MOCK 端点不得有任何形式的凭据 ----------
-- 进程内模拟端点不触达网络，保留凭据会制造"看起来会走网络"的错觉。
ALTER TABLE llm.endpoints DROP CONSTRAINT IF EXISTS ck_endpoints_mock_no_credential;
ALTER TABLE llm.endpoints ADD CONSTRAINT ck_endpoints_mock_no_credential
    CHECK (deployment_type <> 'MOCK'
           OR (credential_ref IS NULL
               AND credential_ciphertext IS NULL
               AND credential_masked IS NULL));

-- ---------- 约束 2：HTTP 端点必须有且仅有一个凭据来源 ----------
-- 同时存在会被 <>（异或）判为假 → 拒绝；两者皆无 → 也被拒绝。
-- 这样"两个来源并存时以谁为准"这种歧义在数据层就不可能存在。
ALTER TABLE llm.endpoints DROP CONSTRAINT IF EXISTS ck_endpoints_single_credential_source;
ALTER TABLE llm.endpoints ADD CONSTRAINT ck_endpoints_single_credential_source
    CHECK (deployment_type <> 'HTTP'
           OR ((credential_ref IS NOT NULL) <> (credential_ciphertext IS NOT NULL)));

-- ---------- 约束 3：打码值必须与密文同生共死 ----------
-- 打码值只由服务端从明文密钥计算写入，不能单独存在（否则可伪造展示值）。
ALTER TABLE llm.endpoints DROP CONSTRAINT IF EXISTS ck_endpoints_masked_requires_ciphertext;
ALTER TABLE llm.endpoints ADD CONSTRAINT ck_endpoints_masked_requires_ciphertext
    CHECK (credential_masked IS NULL OR credential_ciphertext IS NOT NULL);

COMMENT ON COLUMN llm.endpoints.credential_ciphertext IS
    'AES-256-GCM 信封密文（version(1B)||nonce(12B)||ct+tag，AAD 绑定 endpoint_id）。'
    '明文密钥绝不进入此列之外的任何位置（SQL 参数 / 审计事件 / 日志 / SSE payload）。';
COMMENT ON COLUMN llm.endpoints.credential_masked IS
    '供界面展示的打码值（如 sk-****6e22）。由服务端从明文计算写入，客户端永远不能提交该字段。';
COMMENT ON COLUMN llm.endpoints.credential_updated_at IS
    '密文凭据最近一次写入/更换时间。credential_ref 路径不更新该列。';
