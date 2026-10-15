-- ============================================================
-- 000019 让 Model Registry 的能力声明与端点地址真正可用
-- 依据：第九篇 §2.1 原则 3/4（路由由平台策略决定；能力声明必须经验证）、
--       §4.4 步骤 5（“将通过验证的能力写入带版本的 Registry 记录”）、
--       §5.2 数据表清单（llm.capability_profiles「经验证的能力记录」）、
--       §6.1（数据安全与能力要求是硬约束）、§9.1（native schema 与
--       generate-then-validate 必须分开登记）
-- ============================================================
-- 缺陷背景（本轮打通模型调用链时暴露，见自查矩阵缺陷 #13/#14）：
--
-- 缺陷 #13：能力声明无处存放，Route 只能"乐观放行"
--   000010 的 llm.model_revisions 只有：
--       capability_digest TEXT NOT NULL
--   即只保存能力内容的**哈希**，没有任何结构化能力字段；而 §5.2 要求存在的
--   llm.capability_profiles 表在 DDL 中从未创建。后果：
--     · Router.Resolve 依据 ProviderCapabilities{NativeSchema, SupportsSeed,
--       SupportsTools, MaxContextTokens} 做硬约束判定，这些字段无法从哈希反解；
--     · 缺少结构化能力时路由只能"乐观放行"，把 §6.1 的硬约束降级为软约束：
--       需要工具调用的请求可能被路由到不支持工具调用的端点，最终在运行时以
--       供应商错误收场 —— 而这本应在路由阶段以 UNAVAILABLE 明确拒绝（§6.2）。
--
-- 缺陷 #14：端点没有网络地址，真实 HTTP 调用无法发起
--   000010 的 llm.endpoints 只有 endpoint_name/deployment_type/region_code/
--   credential_ref，没有任何地址字段。凭据引用只回答"用哪把钥匙"，
--   不回答"敲哪扇门"，因此除进程内模拟端点外无法真正发起调用。
--
-- 修复要点（第 1 项）：能力档案采用**内容寻址**，主键即能力内容的规范化 SHA-256。
--   这样 llm.model_revisions.capability_digest 可以直接外键指向档案，哈希与内容
--   在结构上不可能漂移；相同能力的多个模型版本自动复用同一条档案（§4.4 的复用意图）。
-- ============================================================

-- ---------- 1. 能力档案（内容寻址） ----------
CREATE TABLE IF NOT EXISTS llm.capability_profiles (
    -- 主键 = 规范化 capabilities 的 SHA-256（由写入方从 capabilities 计算）。
    -- 内容寻址使"摘要"与"内容"恒等，不存在手工填写哈希导致漂移的可能。
    capability_digest TEXT PRIMARY KEY,
    capabilities JSONB NOT NULL,
    -- 能力档案的修订标识（§4.4 步骤 6：端点或模型升级后需重新评估）。
    revision TEXT NOT NULL,
    -- 状态：VERIFIED（已通过能力探测）/ UNVERIFIED（仅声明，未验证）。
    -- 之所以区分，是因为 §4.4 明确"不能仅凭人工填写或服务端自报"。
    status TEXT NOT NULL
        CHECK (status IN ('VERIFIED', 'UNVERIFIED', 'REVOKED')),
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 能力必须是对象：数组/标量无法承载具名能力字段，放行会在读取侧产生
    -- "取不到字段就默认 false/0"的静默降级。
    CONSTRAINT ck_capability_profiles_object
        CHECK (jsonb_typeof(capabilities) = 'object'),
    -- 已声明为 VERIFIED 的档案必须有验证时间，否则"已验证"无法审计。
    CONSTRAINT ck_capability_profiles_verified_at
        CHECK (status <> 'VERIFIED' OR verified_at IS NOT NULL)
);

COMMENT ON TABLE llm.capability_profiles IS
    '经验证的能力档案（第九篇 §4.4/§5.2）。主键为 capabilities 的规范化 SHA-256（内容寻址）。';

-- ---------- 2. 模型版本必须绑定一条能力档案 ----------
-- 先补齐可能已存在的孤立摘要（本迁移在空库/演示库上执行，正常无行）。
INSERT INTO llm.capability_profiles (capability_digest, capabilities, revision, status)
SELECT DISTINCT r.capability_digest, '{}'::jsonb, 'legacy', 'UNVERIFIED'
  FROM llm.model_revisions r
 WHERE NOT EXISTS (
       SELECT 1 FROM llm.capability_profiles p
        WHERE p.capability_digest = r.capability_digest);

ALTER TABLE llm.model_revisions
    DROP CONSTRAINT IF EXISTS fk_model_revisions_capability;
ALTER TABLE llm.model_revisions
    ADD CONSTRAINT fk_model_revisions_capability
    FOREIGN KEY (capability_digest)
    REFERENCES llm.capability_profiles(capability_digest);

COMMENT ON COLUMN llm.model_revisions.capability_digest IS
    '指向 llm.capability_profiles 的内容寻址外键，同时也是能力内容的完整性指纹。';

-- ---------- 3. 端点网络地址 ----------
-- MVP 扩展：第九篇提到 Deployment Profile（云端/私有部署/区域与网络边界）但未建表，
-- 此处先以 base_url 承接"真实 HTTP 端点可被寻址"这一最小必需信息；
-- 待 Deployment Profile 表落地后应归位到该表，避免端点表继续承载部署语义。
ALTER TABLE llm.endpoints
    ADD COLUMN IF NOT EXISTS base_url TEXT;

COMMENT ON COLUMN llm.endpoints.base_url IS
    'HTTP 类端点的基址（MVP 扩展）。deployment_type=MOCK 的进程内端点不使用该字段。';
