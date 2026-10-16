-- 000040: 发布实体表（release_mgmt.releases）
--
-- 背景：GET /releases 此前读的是质量门禁决策记录（quality_gate_decisions），
-- 与前端「发布管理」页期望的 Release 形状（state/gate_state/artifact）完全脱节；
-- promote/rollback 两个前端已接入的写操作没有任何可操作的真源。
-- 本迁移建立发布实体：artifact 的发布生命周期（candidate → promoted → rolled_back）
-- 以本表为单一真源；质量门禁结论在创建快照时落到 gate_state 列。
--
-- 与前端 Release 类型（web/src/types/domain.ts）对齐：
--   state: candidate | promoted | rolled_back（'draft' 仅是前端类型的历史成员，
--          发布候选直接从 candidate 开始，服务器永不返回 draft）
--   gate_state: passed | failed | pending
--
-- 创建方刻意缓行的说明：release 行应由评估运行完成时创建（gate_state 取该 run
-- 的门禁结论快照）。当前评估链路尚无"完成即落发布候选"的写入方，先建表 +
-- seed 演示数据，让 promote/rollback 端到端可用；接线创建方时**不得**引入
-- 第二张发布表（单一真源）。
CREATE TABLE IF NOT EXISTS release_mgmt.releases (
    release_id    UUID PRIMARY KEY,
    tenant_id     UUID NOT NULL REFERENCES iam.tenants(id),
    artifact_type TEXT NOT NULL CHECK (artifact_type IN ('ONTOLOGY', 'AGENT', 'WORKFLOW', 'POLICY')),
    artifact_id   TEXT NOT NULL,
    version       TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'candidate'
                  CHECK (state IN ('candidate', 'promoted', 'rolled_back')),
    gate_state    TEXT NOT NULL DEFAULT 'pending'
                  CHECK (gate_state IN ('passed', 'failed', 'pending')),
    environment   TEXT,
    promoted_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, artifact_type, artifact_id, version)
);

CREATE INDEX IF NOT EXISTS idx_releases_tenant_state
    ON release_mgmt.releases (tenant_id, state);

-- 租户隔离：与 000016 的 quality_gate_decisions 同一标准函数
-- （ENABLE + FORCE ROW LEVEL SECURITY + <table>_tenant_isolation 策略）。
SELECT app.apply_tenant_rls('release_mgmt', 'releases');

-- 新表需要显式授权（000011 的 GRANT ALL TABLES 是当时点的快照，不含后建表）。
GRANT SELECT, INSERT, UPDATE, DELETE ON release_mgmt.releases TO odap_app;

COMMENT ON TABLE release_mgmt.releases IS
    'RLS: release_mgmt.releases_tenant_isolation（000040；发布实体单一真源，promote/rollback 的可操作对象）';
