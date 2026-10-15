-- ODAP 平台迁移 000030：给校验发现项补「跨轮次稳定身份」
--
-- 依据：本体校验借鉴清单第 1 条（确定性 finding id）。
--
-- 缺陷：
--   ontology.validation_findings 的 id 是随机 UUID、validation_run_id 每轮都变，
--   于是"这条违规是上轮就有的、还是本轮新出现的"无法回答，"违规数从 3 降到 2"
--   也无法区分是真修好了还是漏报了一条。回归对比缺一个不随轮次漂移的身份。
--
-- 本迁移新增 finding_digest 列（shacl.Result.Digest() 的落点），
-- 元组为 shape_id ‖ focus_node ‖ path ‖ component ‖ severity（SHA-256 hex）：
--   * 用完整 IRI，前缀映射与局部名的重构不会让身份漂移；
--   * 不含 message —— 改一句措辞不等于"问题变了"；
--   * 不含 validation_run_id —— 跨轮次稳定正是它存在的全部理由。
--
-- 本迁移**只做加法**：不改既有列、不改既有行语义，可重复执行（迁移重放安全）。
-- ============================================================

ALTER TABLE ontology.validation_findings
    ADD COLUMN IF NOT EXISTS finding_digest TEXT;

-- ---------- 为何**不加** UNIQUE ----------
-- 同一个 (shape, focus, path, component, severity) 可能合法地对应**多条**发现项：
-- 例如一个 sh:hasValue 属性有 3 个取值都不在允许集合内，三条违规除值本身外
-- 元组完全相同，摘要因此相同。加 UNIQUE 会把这类真实数据误判为重复而拒绝落库。
-- 摘要的用途是"跨轮次比对身份"，不是"在表内去重"。
CREATE INDEX IF NOT EXISTS idx_ontology_findings_digest
    ON ontology.validation_findings (tenant_id, finding_digest);

-- ---------- 为何不回溯回填 ----------
-- 摘要的元组含 shape_id（形状 IRI），而迁移前的旧行没有记录形状身份，
-- 用 focus_node ‖ path ‖ component ‖ severity 复算只能得到一个**不同**的身份，
-- 会让旧行长出一个与新版校验产出的身份不可比、却被当成可比的值。
-- 旧行保持 NULL；读取侧对空摘要**不写** finding_digest 键（见 queryLatestFindings），
-- 不伪造"已知但为空"的身份。

COMMENT ON COLUMN ontology.validation_findings.finding_digest IS
    '发现项的跨轮次稳定身份（SHA-256 hex，元组 shape_id‖focus_node‖path‖component‖severity）。'
    '迁移前写入的旧行无法复算（缺形状身份）故保持 NULL；不加 UNIQUE（同元组多条违规是合法的）。';
