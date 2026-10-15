-- ODAP 平台迁移 000009：graph schema（RDF 三元组存储，PG 适配器）
-- 依据：第十三篇 §6（P1 修复：三索引方案 PK+POS+OSP，按 tenant 声明式分区）
-- 承诺范围：PG 方案 ≤1000 万三元组/租户；更大规模走专用 Store（§5.3 Spike 门禁）

CREATE SCHEMA IF NOT EXISTS graph;

CREATE TABLE graph.terms (
    term_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    term_kind SMALLINT NOT NULL
        CHECK (term_kind IN (1, 2, 3)), -- 1=IRI 2=BlankNode 3=Literal
    lexical_form TEXT NOT NULL,
    datatype_iri TEXT,
    language_tag TEXT,
    canonical_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_terms_digest
    ON graph.terms (canonical_digest);

CREATE INDEX idx_terms_lexical
    ON graph.terms (lexical_form);

CREATE TABLE graph.named_graphs (
    graph_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id UUID NOT NULL,
    graph_iri TEXT NOT NULL,
    graph_type TEXT NOT NULL,
    dataset_id UUID,
    snapshot_id TEXT,
    ontology_version TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, graph_iri)
);

-- 三元组表：三索引方案（PK+POS+OSP），按 tenant_id 声明式分区。
-- idx_spo 与 PK 高度重叠故不建（第十三篇 §6.3 写放大量化对比）。
CREATE TABLE graph.triples (
    tenant_id UUID NOT NULL,
    graph_id BIGINT NOT NULL
        REFERENCES graph.named_graphs(graph_id),
    subject_id BIGINT NOT NULL
        REFERENCES graph.terms(term_id),
    predicate_id BIGINT NOT NULL
        REFERENCES graph.terms(term_id),
    object_id BIGINT NOT NULL
        REFERENCES graph.terms(term_id),
    fact_kind TEXT NOT NULL,
    mapping_version_id UUID,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_from TIMESTAMPTZ,
    valid_to TIMESTAMPTZ,
    PRIMARY KEY (
        tenant_id,
        graph_id,
        subject_id,
        predicate_id,
        object_id
    )
) PARTITION BY LIST (tenant_id);

CREATE INDEX idx_triples_pos
    ON graph.triples (graph_id, predicate_id, object_id, subject_id);

CREATE INDEX idx_triples_osp
    ON graph.triples (graph_id, object_id, subject_id, predicate_id);

-- 说明：POS/OSP 二级索引建在分区表上时自动按分区创建对应本地索引（第十三篇 §6.3）。
-- 新租户的分区（CREATE TABLE graph.triples_t<uuid> PARTITION OF ...）由租户初始化流程建立，
-- 不在本迁移中为未知租户预建（迁移规范见第二篇 §11）。
