-- ODAP 平台迁移 000041：知识库（模块 A「企业知识问答本体」）
-- 依据：实施计划「四大模块」之模块 A。
-- 要点：
--   * pgvector 扩展（compose 已换 pgvector/pgvector:pg16 镜像）+ pg_trgm（中文词项检索兜底）
--   * 嵌入维度固定 1024（bge-m3 / qwen text-embedding-v3 等主流中文嵌入模型）：
--     HNSW 索引要求固定维度；模型维度不符时任务 fail-closed 报错，绝不截断/映射
--   * 全部租户级表 RLS FORCE；写经 app.current_tenant_id()

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE SCHEMA IF NOT EXISTS kb;
-- USAGE 与 CREATE 缺一不可：USAGE 是访问 schema 内任何对象的先决条件，
-- 只授 CREATE 会让运行时角色（odap_app）对 kb.* 一律 permission denied。
GRANT USAGE, CREATE ON SCHEMA kb TO odap_app;

-- 知识文档（软删除：status=DELETED 保留审计追溯，不出现在列表）
CREATE TABLE kb.documents (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    title TEXT NOT NULL,
    -- 上传根（ODAP_UPLOAD_ROOT）内的相对路径；worker 按此读取原始文件
    source_path TEXT NOT NULL,
    file_kind TEXT NOT NULL CHECK (file_kind IN ('pdf', 'docx', 'xlsx', 'md', 'txt')),
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'READY'
        CHECK (status IN ('READY', 'FAILED', 'DELETED')),
    error TEXT NOT NULL DEFAULT '',
    uploaded_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, sha256)
);

CREATE INDEX idx_documents_tenant_created ON kb.documents (tenant_id, created_at DESC);

-- 文档分块：向量（余弦 HNSW）+ 词项（trigram GIN，中文友好）双索引
CREATE TABLE kb.chunks (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    document_id UUID NOT NULL REFERENCES kb.documents(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
    content TEXT NOT NULL,
    -- 嵌入维度固定 1024；未嵌入时为 NULL（嵌入任务完成后回填）
    embedding vector(1024),
    -- 产出该嵌入的 ModelID（点名词由网关路由；用于维度一致性审计）
    embedding_model TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, document_id, chunk_index)
);

CREATE INDEX idx_chunks_embedding_hnsw ON kb.chunks
    USING hnsw (embedding vector_cosine_ops);
CREATE INDEX idx_chunks_content_trgm ON kb.chunks
    USING gin (content gin_trgm_ops);
CREATE INDEX idx_chunks_document ON kb.chunks (tenant_id, document_id, chunk_index);

-- 摄取任务（后台 worker 领取；FOR UPDATE SKIP LOCKED + 租约续期）
CREATE TABLE kb.ingest_jobs (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    document_id UUID NOT NULL REFERENCES kb.documents(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('EMBED', 'EXTRACT')),
    -- EXTRACT 任务的目标本体版本（抽取实例写入该版本图 IRI）
    target_ontology_version_id UUID,
    -- 嵌入点名的 ModelID（快照自创建时配置，重试不漂移）
    embedding_model TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED')),
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    progress_done INTEGER NOT NULL DEFAULT 0,
    progress_total INTEGER NOT NULL DEFAULT 0,
    stage TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_epoch BIGINT NOT NULL DEFAULT 0,
    lease_expires_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, document_id, kind)
);

CREATE INDEX idx_ingest_jobs_claim ON kb.ingest_jobs (status, next_run_at);
CREATE INDEX idx_ingest_jobs_document ON kb.ingest_jobs (tenant_id, document_id);

-- 抽取发现：闭包校验/SHACL 校验的确定性诊断（finding_digest 跨轮次去重）
CREATE TABLE kb.extraction_findings (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    job_id UUID NOT NULL REFERENCES kb.ingest_jobs(id) ON DELETE CASCADE,
    chunk_id UUID,
    finding_digest TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('INFO', 'WARNING', 'ERROR')),
    message TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (job_id, finding_digest)
);

SELECT app.apply_tenant_rls('kb', 'documents');
SELECT app.apply_tenant_rls('kb', 'chunks');
SELECT app.apply_tenant_rls('kb', 'ingest_jobs');
SELECT app.apply_tenant_rls('kb', 'extraction_findings');

GRANT SELECT, INSERT, UPDATE, DELETE ON kb.documents, kb.chunks, kb.ingest_jobs, kb.extraction_findings TO odap_app;
