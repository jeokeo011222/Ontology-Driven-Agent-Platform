-- ODAP 平台迁移 000037：工具调用记录（工具管理实做 + 审批门禁）
--
-- 背景：POST /tools/{id}/invoke 此前恒返回 503（「工具执行需经 Agent 执行器
-- 与授权/ERP 网关，未接入本进程」），工具管理页的测试调用从未真实执行。
-- 用户要求工具管理全部按实际业务执行（严禁 mock），且外部写类工具
-- （EXTERNAL_WRITE）必须经**审批确认**后才能执行（平台规则：写操作不走
-- 工具页直调，必须有人为门禁）。
--
-- 本迁移新增 runtime.tool_invocations：每一次工具调用（无论是否需要审批）
-- 都在此留痕，状态机：
--
--   READ / INTERNAL_WRITE（无需审批）：
--     EXECUTING → SUCCEEDED | FAILED            （同步执行，记录真实输出/错误）
--
--   EXTERNAL_WRITE（需审批，fail-closed）：
--     PENDING_APPROVAL → APPROVED → EXECUTING → SUCCEEDED | FAILED
--                     → REJECTED               （驳回终态）
--
-- 状态推进由服务端写（handlers/tool_executors.go），事务纪律与
-- integration.erp_operations 相同：短事务推进状态 → 事务外远端调用 →
-- 短事务落结果。审批通过后的执行阶段受状态机守卫，杜绝重复远端调用。
--
-- 诚实性约束（fail-closed）：
--   - output_payload 只写执行器真实返回；error_code/summary 只写真实错误；
--   - 无真实后端的适配器（ocr / budget 等）返回 UNAVAILABLE 并原样落 FAILED，
--     绝不伪造成功输出。

CREATE TABLE IF NOT EXISTS runtime.tool_invocations (
    id                 UUID PRIMARY KEY,
    tenant_id          UUID NOT NULL,
    tool_id            UUID NOT NULL,
    tool_version_id    UUID NOT NULL,
    tool_key           TEXT NOT NULL,
    effect_class       TEXT NOT NULL,
    status             TEXT NOT NULL
                           CHECK (status IN (
                               'PENDING_APPROVAL', 'APPROVED', 'EXECUTING',
                               'SUCCEEDED', 'FAILED', 'REJECTED', 'CANCELLED')),
    input_payload      JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_payload     JSONB,
    error_code         TEXT,
    error_summary      TEXT,
    requested_by       TEXT NOT NULL,
    approved_by        TEXT,
    approval_decided_at TIMESTAMPTZ,
    started_at         TIMESTAMPTZ,
    finished_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE runtime.tool_invocations IS
    '工具调用留痕：读/内部写同步执行；外部写必须经 PENDING_APPROVAL 审批门禁后执行';

SELECT app.apply_tenant_rls('runtime', 'tool_invocations');

CREATE INDEX IF NOT EXISTS idx_tool_invocations_tool_time
    ON runtime.tool_invocations (tenant_id, tool_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_tool_invocations_status
    ON runtime.tool_invocations (tenant_id, status, created_at DESC)
    WHERE status IN ('PENDING_APPROVAL');
