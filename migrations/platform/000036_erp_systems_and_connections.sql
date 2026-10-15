-- ODAP 平台迁移 000036：ERP 系统类型字典 + 租户级 ERP 连接配置（与ERP集成）
--
-- 背景：ERP 适配器此前只能通过环境变量在装配期静态配置（单一连接），
-- 管理控制台「与ERP集成」页只能只读展示已安装的连接器插件，无法配置
-- 「对接哪个 ERP、用什么地址与凭据」。用户要求：
--   1. ERP 系统类型定义为**字典**（用友NC65 / 金蝶星空云 / SAP，默认用友NC65）；
--   2. 提供完整的连接器配置流程（选类型 → 填参数 → 测试 → 创建）。
--
-- 本迁移做两件事：
--   1. integration.erp_systems：全局只读字典。`implemented=false` 的类型
--      （金蝶星空云 / SAP）当前没有真实适配器，创建连接时服务端会直接拒绝
--      （422，fail-closed）——字典先行建模，适配器实现后无需再改表。
--   2. integration.erp_connections：租户级连接配置。凭据采用与 llm.endpoints
--      完全相同的「双路径 + 密文优先」规则：
--        - credential_ciphertext：界面录入的凭据以 AES-256-GCM 信封加密落库
--          （secretbox，AAD 绑定 connection_id）；明文绝不作为 SQL 参数、
--          绝不进审计/日志/SSE/HTTP 响应，读取只回打码值 credential_masked。
--        - credential_ref：环境变量引用名，作为回落路径。
--      状态 ACTIVE / DISABLED / DELETED：删除为软删（置 DELETED），
--      保留审计链可追溯。
--
-- 权限：复用既有目录键（不新增）——admin.connector.read / admin.connector.write
-- （「与ERP集成」页既有权限），凭据治理规则沿用模型端点的既定硬约束。

-- 全局 ERP 系统类型字典（无 tenant_id，全局共享，类似 governance.permissions 的定位）
CREATE TABLE IF NOT EXISTS integration.erp_systems (
    system_code  TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    adapter_key  TEXT NOT NULL,
    implemented  BOOLEAN NOT NULL DEFAULT FALSE,
    is_default   BOOLEAN NOT NULL DEFAULT FALSE,
    sort_no      INTEGER NOT NULL DEFAULT 0,
    description  TEXT NOT NULL DEFAULT ''
);

INSERT INTO integration.erp_systems (system_code, display_name, adapter_key, implemented, is_default, sort_no, description)
VALUES
    ('nc65',          '用友NC65',   'nc65',    TRUE,  TRUE,  1, '默认对接类型；适配器经环境变量或连接配置装配'),
    ('kingdee_cloud', '金蝶星空云', 'kingdee', FALSE, FALSE, 2, '字典已建模，适配器尚未实现（创建连接会被拒绝）'),
    ('sap',           'SAP',        'sap',     FALSE, FALSE, 3, '字典已建模，适配器尚未实现（创建连接会被拒绝）')
ON CONFLICT (system_code) DO NOTHING;

-- 租户级 ERP 连接配置（RLS + FORCE RLS，与 registry/llm 等租户表同一套纪律）
CREATE TABLE IF NOT EXISTS integration.erp_connections (
    id                    UUID PRIMARY KEY,
    tenant_id             UUID NOT NULL,
    system_code           TEXT NOT NULL REFERENCES integration.erp_systems(system_code),
    name                  TEXT NOT NULL,
    base_url              TEXT NOT NULL,
    credential_ref        TEXT,
    credential_ciphertext BYTEA,
    credential_masked     TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'ACTIVE'
                              CHECK (status IN ('ACTIVE', 'DISABLED', 'DELETED')),
    created_by            TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE integration.erp_connections IS
    '租户级 ERP 连接配置；凭据密文优先（AES-256-GCM 信封，AAD 绑定 id），明文绝不落库/出站';
COMMENT ON COLUMN integration.erp_connections.credential_masked IS
    '打码凭据（如 ****6e22）仅供界面回显；读取路径绝不 SELECT credential_ciphertext 本体';

SELECT app.apply_tenant_rls('integration', 'erp_connections');

CREATE INDEX IF NOT EXISTS idx_erp_connections_tenant_status
    ON integration.erp_connections (tenant_id, status, created_at DESC);
