-- ODAP 平台迁移 000027：身份与权限内置角色口径修正
--
-- 口径（第十七篇 §8.5 / 第二篇 §11.2）：平台只内置**一个**系统管理员角色，
-- 其余角色一律由管理员在「身份与权限」页面人工重建（is_system_role = FALSE）。
--
-- 背景：
--   1. 早期 dataset 把 finance-operator 也标记为系统内置（is_system_role = TRUE），
--      与"只有系统管理员是内置"不符 → 修正为自定义角色；
--   2. platform-admin 是唯一内置角色，显示名统一为"系统管理员"。
--
-- 幂等：按 role_key 定位，可重复执行。

UPDATE governance.roles
   SET is_system_role = FALSE
 WHERE role_key = 'finance-operator';

UPDATE governance.roles
   SET name = '系统管理员'
 WHERE role_key = 'platform-admin';
