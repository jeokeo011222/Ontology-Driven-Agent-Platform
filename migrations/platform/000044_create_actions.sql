-- ODAP 平台迁移 000044：本体 Action 管理（模块 D「完整版：一等节点 + PROV-O」）
-- 依据：实施计划「四大模块」之模块 D。
-- 要点：
--   * 决策/动作一等节点：QUERY/UPDATE/INFER/ACTION/TOOL 五类
--   * 双存储：本表存 PostgreSQL 元数据；PROV-O 溯源三元组写入图存储独立命名图
--     （prov_graph_iri，经 GraphStore.InsertTriples）
--   * 相似决策检索：summary 嵌入 vector(1024) + HNSW（异步补算，失败显式标记不阻塞）

CREATE SCHEMA IF NOT EXISTS action;
-- USAGE 与 CREATE 缺一不可（见 000041 注释）。
GRANT USAGE, CREATE ON SCHEMA action TO odap_app;

CREATE TABLE action.actions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    run_id UUID,
    type TEXT NOT NULL CHECK (type IN ('QUERY', 'UPDATE', 'INFER', 'ACTION', 'TOOL')),
    title TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    input_digest TEXT NOT NULL DEFAULT '',
    output_digest TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (outcome IN ('PENDING', 'SUCCESS', 'DENIED', 'REJECTED', 'ERROR')),
    -- PROV-O 溯源命名图 IRI（prov:{tenant}:{action_id}）
    prov_graph_iri TEXT NOT NULL DEFAULT '',
    actor_kind TEXT NOT NULL DEFAULT 'USER' CHECK (actor_kind IN ('USER', 'AGENT', 'A2A', 'WORKER')),
    actor_name TEXT NOT NULL DEFAULT '',
    -- 相似检索嵌入（summary 异步补算；embed_status 显式标记，绝不静默）
    embedding vector(1024),
    embedding_model TEXT NOT NULL DEFAULT '',
    embed_status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (embed_status IN ('PENDING', 'SUCCEEDED', 'FAILED', 'SKIPPED')),
    embed_error TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_actions_tenant_time ON action.actions (tenant_id, occurred_at DESC);
CREATE INDEX idx_actions_type ON action.actions (tenant_id, type);
CREATE INDEX idx_actions_run ON action.actions (tenant_id, run_id);
CREATE INDEX idx_actions_embedding_hnsw ON action.actions
    USING hnsw (embedding vector_cosine_ops);

SELECT app.apply_tenant_rls('action', 'actions');

GRANT SELECT, INSERT, UPDATE, DELETE ON action.actions TO odap_app;
