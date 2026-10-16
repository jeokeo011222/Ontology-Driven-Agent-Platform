-- ODAP 平台迁移 000046：聊天会话（Chat Sessions）
--
-- 背景：AssistantPage 此前只有单次 Run 提交，没有会话概念。
-- 本迁移引入 chat_sessions / chat_messages 两张租户级表，
-- 支撑"多轮对话持久化 + 会话列表 + 归档/删除"全生命周期。
--
-- 要点：
--   * 两张表均属于 app schema（已在 000011 初始化），不新建 schema；
--   * RLS 通过 app.apply_tenant_rls 注入标准租户策略；
--   * chat_messages.session_id CASCADE 删除（会话删除 = 消息全清）；
--   * chat_messages.run_id 软引用 runtime.agent_runs（可为 NULL，on delete set null）；
--   * chat_sessions.ontology_id 软引用 ontology.ontologies（可为 NULL，on delete set null）。

-- app schema 的 USAGE 与 CREATE 在 000011 已授予，此处显式 GRANT 兜底（防止
-- 全新环境或 GRANT 被误删），幂等可重复执行。
GRANT USAGE ON SCHEMA app TO odap_app;

CREATE TABLE app.chat_sessions (
    id UUID PRIMARY KEY,
    title TEXT NOT NULL,
    ontology_id UUID REFERENCES ontology.ontologies(id) ON DELETE SET NULL,
    tenant_id UUID NOT NULL,
    subject_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'ARCHIVED'))
);

CREATE TABLE app.chat_messages (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES app.chat_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    content TEXT NOT NULL,
    run_id UUID REFERENCES runtime.agent_runs(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 索引：会话列表按 (tenant, subject, status) 筛选 + created_at 排序；
-- 消息列表按 session 内 created_at 升序翻页。
CREATE INDEX IF NOT EXISTS idx_chat_sessions_tenant_subject_status
    ON app.chat_sessions (tenant_id, subject_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_chat_messages_session_created
    ON app.chat_messages (session_id, created_at ASC);

-- 启用 RLS 并注入标准租户策略（tenant_id = app.current_tenant_id()）。
ALTER TABLE app.chat_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.chat_messages ENABLE ROW LEVEL SECURITY;
SELECT app.apply_tenant_rls('app', 'chat_sessions');
SELECT app.apply_tenant_rls('app', 'chat_messages');

-- 授权。
GRANT SELECT, INSERT, UPDATE, DELETE ON app.chat_sessions TO odap_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.chat_messages TO odap_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA app
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO odap_app;
