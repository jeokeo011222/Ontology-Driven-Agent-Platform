import { useEffect, useState } from 'react';
import { Badge, Button, Card, CardBody, CardHeader, Dialog, Field, Input, PageHeader, Select, Textarea } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ListFilterBar } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDate, formatDateTime } from '@/lib/format';
import type { Role, RoleBinding, RoleScopeType } from '@/types';
import { useCreateRole, useDeleteRole, usePermissions, useRoleBindings, useRoles } from '../api';
import { Icon } from '@/components/icon';

/**
 * 角色绑定的范围类型 → 中文标签。
 * 权威：contracts/openapi.yaml#/components/schemas/RoleBinding.scope_type 枚举。
 */
const SCOPE_TYPE_LABEL: Record<RoleScopeType, string> = {
  TENANT: '租户级',
  ORG: '组织级',
  ORG_SUBTREE: '组织子树',
  SELF: '本人',
  ASSIGNED: '指派范围',
  EXPLICIT: '显式清单',
};

/**
 * MVP 页面 5d：身份与权限 `/admin/identity`（§8.5 / §14.3）
 *
 * 说明：第十七篇 §10.1 的 feature 目录清单未单设 identity 目录，
 * 而 §11.1 要求登记 `/admin/identity` 路由。角色、权限与授权策略同属「治理域」
 * （§8.4 执行策略 + §8.5 用户、角色与权限），因此本页归入 features/policies。
 *
 * 字段口径：contracts/openapi.yaml 的 `Role` / `RoleBinding`（即
 * `/api/v1/admin/roles`、`/api/v1/admin/role-bindings` 的真实返回）。
 *
 * 已实现字段：角色 = role_key/name/description/is_system_role/created_at；
 *   绑定 = id/role_key/role_name/scope_type/scope_org_id/login_name/
 *   display_name/valid_from/valid_until（**扁平结构**）。
 *
 * 缺陷 #21 修复说明（原文见自查矩阵）：本页此前按
 *   `item.subject.display_name`（绑定）与 `SCOPE_LABEL[item.scope]`（角色）渲染，
 *   而后端返回的是扁平字段且角色无 `scope` → 读取时抛 TypeError，**整页白屏**。
 *   现按契约字段逐项渲染，并移除"角色权限清单""绑定启用状态"这两个
 *   **列表接口未提供**的列（不用占位假数据填充，缺口在页脚显式声明）。
 */
const EMPTY_ROLE = { role_key: '', name: '', description: '' };

export function IdentityPage() {
  const [roleSearch, setRoleSearch] = useState('');
  const rolesPager = useCursorPagination();
  const rolesQuery = useRoles({ q: roleSearch || undefined, cursor: rolesPager.cursor ?? undefined });

  const [bindingSearch, setBindingSearch] = useState('');
  const bindingsPager = useCursorPagination();
  const bindingsQuery = useRoleBindings({
    q: bindingSearch || undefined,
    cursor: bindingsPager.cursor ?? undefined,
  });

  const permissionsQuery = usePermissions();
  const createRole = useCreateRole();
  const deleteRole = useDeleteRole();

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    rolesPager.reset();
  }, [roleSearch, rolesPager.reset]);
  useEffect(() => {
    bindingsPager.reset();
  }, [bindingSearch, bindingsPager.reset]);

  const [createOpen, setCreateOpen] = useState(false);
  const [form, setForm] = useState({ ...EMPTY_ROLE });
  const [selectedPerms, setSelectedPerms] = useState<string[]>([]);

  const closeCreate = () => {
    setCreateOpen(false);
    setForm({ ...EMPTY_ROLE });
    setSelectedPerms([]);
  };

  const togglePerm = (key: string) => {
    setSelectedPerms((prev) => (prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]));
  };

  const submitCreate = () => {
    createRole.mutate(
      {
        role_key: form.role_key,
        name: form.name,
        description: form.description,
        permissions: selectedPerms,
      },
      { onSuccess: () => closeCreate() },
    );
  };

  const roleColumns: DataTableColumn<Role>[] = [
    {
      key: 'name',
      header: '角色',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="mono">{item.role_key}</span> · <span className="copy-id">{item.id}</span>
          </div>
          {item.description ? (
            <div className="text-xs muted" style={{ marginTop: 4 }}>
              {item.description}
            </div>
          ) : null}
        </div>
      ),
    },
    {
      key: 'is_system_role',
      header: '角色类型',
      tight: true,
      render: (item) => (
        <Badge tone={item.is_system_role ? 'info' : 'neutral'}>
          {item.is_system_role ? '系统内置' : '自定义'}
        </Badge>
      ),
    },
    {
      key: 'created_at',
      header: '创建时间',
      tight: true,
      render: (item) => <span className="text-sm">{formatDateTime(item.created_at)}</span>,
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) =>
        item.is_system_role ? (
          <span className="text-xs muted" title="系统内置角色不可删除">
            内置不可删除
          </span>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            icon="ban"
            disabled={deleteRole.isPending}
            onClick={() => deleteRole.mutate(item.id)}>
            删除
          </Button>
        ),
    },
  ];

  const bindingColumns: DataTableColumn<RoleBinding>[] = [
    {
      key: 'subject',
      header: '主体',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.display_name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.login_name}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'role',
      header: '角色',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {item.role_name}
          <br />
          <span className="text-xs muted mono">{item.role_key}</span>
        </span>
      ),
    },
    {
      key: 'scope',
      header: '授权范围',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {SCOPE_TYPE_LABEL[item.scope_type] ?? item.scope_type}
          <br />
          <span className="text-xs muted mono">{item.scope_org_id ?? '—'}</span>
        </span>
      ),
    },
    {
      key: 'validity',
      header: '授权有效期',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDate(item.valid_from)} — {item.valid_until ? formatDate(item.valid_until) : '长期有效'}
        </span>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="shieldCheck"
        title="身份与权限"
        subtitle="用户列表、组织归属、身份提供方、角色绑定、业务范围授权、字段级敏感数据访问、授权有效期与权限撤销记录。"
        actions={
          <Button variant="primary" icon="plus" onClick={() => setCreateOpen(true)}>
            新建角色
          </Button>
        }
      />

      <section className="section" aria-labelledby="roles-title">
        <h2 className="section__title" id="roles-title">
          角色
        </h2>
        <ListFilterBar
          searchValue={roleSearch}
          onSearchChange={setRoleSearch}
          searchPlaceholder="搜索 role_key / 名称…"
        />
        <DataTable
          caption="角色列表"
          columns={roleColumns}
          rows={rolesQuery.data?.items ?? []}
          rowKey={(item) => item.id}
          loading={rolesQuery.isLoading}
          error={rolesQuery.error}
          onRetry={() => void rolesQuery.refetch()}
          emptyTitle="暂无角色"
          nextCursor={rolesQuery.data?.next_cursor ?? null}
          hasPrevPage={rolesPager.hasPrevPage}
          onPrevPage={rolesPager.goPrev}
          onNextPage={() => rolesPager.goNext(rolesQuery.data?.next_cursor ?? null)}
        />
      </section>

      <section className="section" aria-labelledby="bindings-title">
        <h2 className="section__title" id="bindings-title">
          角色绑定与授权有效期
        </h2>
        <ListFilterBar
          searchValue={bindingSearch}
          onSearchChange={setBindingSearch}
          searchPlaceholder="搜索 role_key / 角色名 / 登录名…"
        />
        <DataTable
          caption="角色绑定列表"
          columns={bindingColumns}
          rows={bindingsQuery.data?.items ?? []}
          rowKey={(item) => item.id}
          loading={bindingsQuery.isLoading}
          error={bindingsQuery.error}
          onRetry={() => void bindingsQuery.refetch()}
          emptyTitle="暂无角色绑定"
          nextCursor={bindingsQuery.data?.next_cursor ?? null}
          hasPrevPage={bindingsPager.hasPrevPage}
          onPrevPage={bindingsPager.goPrev}
          onNextPage={() => bindingsPager.goNext(bindingsQuery.data?.next_cursor ?? null)}
        />
      </section>

      <Card className="section">
        <CardHeader
          title="模拟授权决策"
          description="输入用户、资源、动作与上下文，返回允许/拒绝与经过权限过滤的决策理由（§8.5）。"
        />
        <CardBody>
          <div className="banner banner--warning" style={{ marginBottom: 16 }}>
            <span className="banner__icon" aria-hidden="true">
              <Icon name="alertTriangle" size={16} />
            </span>
            <span>
              模拟决策只用于解释和验证，<strong>不能让管理员通过前端参数直接伪造正式授权上下文</strong>。
              该能力需在授权模拟端点登记进 <span className="mono">contracts/openapi.yaml</span> 后方可启用；
              在登记前，界面保持只读说明状态（前端不得自行创建未登记的业务端点）。
            </span>
          </div>

          <fieldset disabled style={{ border: 0, padding: 0, margin: 0, opacity: 0.65 }}>
            <div className="grid grid--three">
              <Field label="用户" htmlFor="sim-subject">
                <Input id="sim-subject" placeholder="如 u_li" />
              </Field>
              <Field label="资源类型" htmlFor="sim-resource">
                <Select
                  id="sim-resource"
                  options={[
                    { value: 'ExpenseClaim', label: 'ExpenseClaim' },
                    { value: 'Payment', label: 'Payment' },
                    { value: 'Contract', label: 'Contract' },
                  ]}
                />
              </Field>
              <Field label="动作" htmlFor="sim-action">
                <Select
                  id="sim-action"
                  options={[
                    { value: 'READ', label: 'READ' },
                    { value: 'SUBMIT_FOR_APPROVAL', label: 'SUBMIT_FOR_APPROVAL' },
                    { value: 'CREATE_PAYMENT', label: 'CREATE_PAYMENT' },
                  ]}
                />
              </Field>
            </div>
            <button type="button" className="btn btn--secondary" disabled>
              模拟决策（待后端端点登记后启用）
            </button>
          </fieldset>
        </CardBody>
      </Card>

      <div className="banner banner--warning section" role="status">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="flag" size={16} />
        </span>
        <span>
          <strong>部分功能暂缺：</strong>角色↔权限清单与绑定的启用/停用状态尚未由后端接口返回，
          故本页不展示「权限」「状态」列；「停用主体」缺少后端端点与主体 ID，暂不可执行。
          一律以服务端为准，前端不自行推断。
        </span>
      </div>

      <p className="text-xs muted" style={{ marginTop: 8 }}>
        数据更新时间：
        {formatDateTime(bindingsQuery.dataUpdatedAt ? new Date(bindingsQuery.dataUpdatedAt).toISOString() : undefined)}
      </p>

      <Dialog open={createOpen} onClose={closeCreate} title="新建角色" wide>
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            submitCreate();
          }}>
          <Field label="角色键（role_key）" required hint="租户内唯一，建议小写点号或短横线命名，如 cost-reviewer。">
            <Input
              value={form.role_key}
              onChange={(e) => setForm({ ...form, role_key: e.target.value })}
              required
            />
          </Field>
          <Field label="角色名称" required>
            <Input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              required
            />
          </Field>
          <Field label="角色说明">
            <Textarea
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
            />
          </Field>
          <Field
            label="权限（来自服务端权限目录）"
            hint="仅可选择服务端已登记的权限键；提交时后端会再次校验目录内是否存在。">
            {permissionsQuery.isLoading ? (
              <span className="text-sm muted">加载权限目录…</span>
            ) : permissionsQuery.error ? (
              <span className="text-sm muted">权限目录加载失败，暂不能选择权限。</span>
            ) : (permissionsQuery.data?.items.length ?? 0) === 0 ? (
              <span className="text-sm muted">权限目录为空。</span>
            ) : (
              <div
                className="stack"
                style={{ maxHeight: 220, overflowY: 'auto', border: '1px solid var(--border)', borderRadius: 8, padding: 8 }}>
                {(permissionsQuery.data?.items ?? []).map((perm) => (
                  <label
                    key={perm.permission_key}
                    className="row"
                    style={{ alignItems: 'flex-start', gap: 8, cursor: 'pointer' }}>
                    <input
                      type="checkbox"
                      checked={selectedPerms.includes(perm.permission_key)}
                      onChange={() => togglePerm(perm.permission_key)}
                      style={{ marginTop: 3 }}
                    />
                    <span>
                      <span className="mono text-sm">{perm.permission_key}</span>
                      {perm.description ? (
                        <span className="text-xs muted" style={{ display: 'block' }}>
                          {perm.description}
                        </span>
                      ) : null}
                    </span>
                  </label>
                ))}
              </div>
            )}
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={closeCreate}>
              取消
            </Button>
            <Button type="submit" variant="primary" disabled={createRole.isPending}>
              {createRole.isPending ? '保存中…' : '创建'}
            </Button>
          </div>
          {createRole.error ? (
            <p className="text-sm" style={{ color: 'var(--color-error)' }}>
              创建失败：{(createRole.error as Error).message}
            </p>
          ) : null}
        </form>
      </Dialog>
    </div>
  );
}
