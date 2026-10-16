-- ODAP 平台迁移 000043：A2A 多智能体协同（模块 C「Server 发布 + 注册表」）
-- 依据：实施计划「四大模块」之模块 C。
-- 要点：
--   * Agent Card 与 tasks/* 全部运行期动态装配，不落静态数据，本表只存远程注册
--   * 凭据只允许环境变量引用（auth_header_ref），明文绝不落库/日志

CREATE SCHEMA IF NOT EXISTS a2a;
-- USAGE 与 CREATE 缺一不可（见 000041 注释）。
GRANT USAGE, CREATE ON SCHEMA a2a TO odap_app;

CREATE TABLE a2a.remote_agents (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    agent_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL,
    -- 出站认证头名（如 Authorization / X-API-Key）；空表示无认证头
    auth_header_name TEXT NOT NULL DEFAULT '',
    -- 环境变量引用（如 ODAP_A2A_AGENT_X_KEY）；明文绝不落库
    auth_header_ref TEXT NOT NULL DEFAULT '',
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 允许调用的本平台工具键白名单；空数组 = 不允许任何工具
    tool_whitelist TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED', 'DELETED')),
    last_probe_at TIMESTAMPTZ,
    last_probe_ok BOOLEAN,
    last_probe_error TEXT NOT NULL DEFAULT '',
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (tenant_id, agent_key)
);

SELECT app.apply_tenant_rls('a2a', 'remote_agents');

GRANT SELECT, INSERT, UPDATE, DELETE ON a2a.remote_agents TO odap_app;
