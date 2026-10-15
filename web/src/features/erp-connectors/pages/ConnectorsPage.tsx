import { useEffect, useState } from 'react';
import {
  Badge,
  Button,
  Dialog,
  Field,
  Input,
  PageHeader,
  Select,
  type SelectOption,
} from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { PermissionGate } from '@/components/permission-gate';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { BadgeTone } from '@/components/ui';
import { MetaBadge } from '@/components/status-badge';
import type { Connector } from '@/types';
import { Icon } from '@/components/icon';
import {
  useConnectors,
  useCreateErpConnection,
  useDeleteErpConnection,
  useErpConnections,
  useErpSystems,
  useTestConnector,
  useTestErpConnection,
  useUpdateErpConnection,
  type ErpConnection,
  type ErpConnectionCredential,
} from '../api';

const HEALTH_META: Record<Connector['health'], { label: string; tone: BadgeTone }> = {
  healthy: { label: '健康', tone: 'success' },
  degraded: { label: '降级', tone: 'warning' },
  down: { label: '不可用', tone: 'error' },
  unknown: { label: '未知', tone: 'neutral' },
};

const STATUS_META: Record<ErpConnection['status'], { label: string; tone: BadgeTone }> = {
  ACTIVE: { label: '启用', tone: 'success' },
  DISABLED: { label: '停用', tone: 'warning' },
  DELETED: { label: '已删除', tone: 'error' },
};

const STATUS_OPTIONS: SelectOption[] = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'DISABLED', label: '停用' },
  { value: 'DELETED', label: '已删除' },
];

const SOURCE_LABEL: Record<ErpConnection['credential_source'], string> = {
  CIPHERTEXT: '加密密文',
  ENV_REF: '环境变量引用',
  NONE: '未配置',
};

interface ConnectionForm {
  system_code: string;
  name: string;
  base_url: string;
  usercode: string;
  password: string;
  credential_ref: string;
}

const EMPTY_FORM: ConnectionForm = {
  system_code: 'nc65',
  name: '',
  base_url: '',
  usercode: '',
  password: '',
  credential_ref: '',
};

/**
 * MVP 页面 5b：与ERP集成 `/admin/connectors`（§8.3 / §14.3 + ERP 字典化）
 *
 * 页面结构：
 *   1. ERP 系统类型字典卡片——默认用友NC65；未实现适配器的类型明确标注并禁用创建；
 *   2. ERP 连接列表——完整创建/编辑/删除/测试流程（10 行/页 + 筛选查询）；
 *   3. 已安装连接器插件清单（原有能力，保留）。
 *
 * 安全约束：测试连接用录入参数直连真实探针（nc65 Health），不落库；凭据
 * 明文只在弹窗内存中存活，创建后只回显打码值，绝不回显明文。
 */
export function ConnectorsPage() {
  const systemsQuery = useErpSystems();
  const [filters, setFilters] = useState<{ status: string; q: string; system_code: string }>({
    status: '',
    q: '',
    system_code: '',
  });
  const connectionsQuery = useErpConnections(filters);
  const [pluginSearch, setPluginSearch] = useState('');
  const pluginPager = useCursorPagination();
  const connectorPluginsQuery = useConnectors({
    q: pluginSearch || undefined,
    cursor: pluginPager.cursor ?? undefined,
  });
  const testConnectorMutation = useTestConnector();

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pluginPager.reset();
  }, [pluginSearch, pluginPager.reset]);

  const createMutation = useCreateErpConnection();
  const updateMutation = useUpdateErpConnection();
  const deleteMutation = useDeleteErpConnection();
  const testMutation = useTestErpConnection();
  const [lastTested, setLastTested] = useState<string | null>(null);

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<ErpConnection | null>(null);
  const [form, setForm] = useState<ConnectionForm>(EMPTY_FORM);
  const [deleting, setDeleting] = useState<ErpConnection | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const systems = systemsQuery.data?.items ?? [];
  const defaultSystem = systems.find((s) => s.is_default) ?? systems[0];

  const openCreate = () => {
    setEditing(null);
    setFormError(null);
    setForm({ ...EMPTY_FORM, system_code: defaultSystem?.system_code ?? 'nc65' });
    setFormOpen(true);
  };

  const openEdit = (conn: ErpConnection) => {
    setEditing(conn);
    setFormError(null);
    setForm({
      system_code: conn.system_code,
      name: conn.name,
      base_url: conn.base_url,
      usercode: '',
      password: '',
      credential_ref: conn.credential_ref ?? '',
    });
    setFormOpen(true);
  };

  const buildCredential = (): ErpConnectionCredential | null | undefined => {
    if (!editing) {
      if (form.usercode || form.password) {
        return { usercode: form.usercode, password: form.password };
      }
      return null;
    }
    // 编辑时三态：录入新凭据 = 替换；两者全空 = 省略（不改）。
    if (form.usercode || form.password) {
      return { usercode: form.usercode, password: form.password };
    }
    return undefined;
  };

  const submitForm = () => {
    setFormError(null);
    if (!form.name.trim()) {
      setFormError('连接名称必填');
      return;
    }
    if (!form.base_url.trim()) {
      setFormError('Base URL 必填');
      return;
    }
    if (editing) {
      updateMutation.mutate(
        {
          id: editing.id,
          name: form.name,
          base_url: form.base_url,
          credential: buildCredential(),
        },
        {
          onSuccess: () => {
            setFormOpen(false);
            void connectionsQuery.refetch();
          },
          onError: (e) => setFormError(e instanceof Error ? e.message : String(e)),
        },
      );
      return;
    }
    const hasCredential = Boolean(form.usercode || form.password);
    if (!hasCredential && !form.credential_ref.trim()) {
      setFormError('请录入登录凭据，或提供环境变量引用（credential_ref）——二者必选其一');
      return;
    }
    createMutation.mutate(
      {
        system_code: form.system_code,
        name: form.name,
        base_url: form.base_url,
        credential: hasCredential
          ? { usercode: form.usercode, password: form.password }
          : null,
        credential_ref: form.credential_ref,
      },
      {
        onSuccess: () => {
          setFormOpen(false);
          void connectionsQuery.refetch();
        },
        onError: (e) => setFormError(e instanceof Error ? e.message : String(e)),
      },
    );
  };

  const testFormConnection = () => {
    if (!form.base_url.trim()) {
      setFormError('测试连接前请先填写 Base URL');
      return;
    }
    setFormError(null);
    testMutation.mutate({
      system_code: form.system_code,
      base_url: form.base_url,
      credential: form.usercode || form.password
        ? { usercode: form.usercode, password: form.password }
        : undefined,
    });
  };

  const connectionColumns: DataTableColumn<ErpConnection>[] = [
    {
      key: 'name',
      header: '连接',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · {item.system_name}
          </div>
        </div>
      ),
    },
    {
      key: 'endpoint',
      header: '端点与凭据',
      render: (item) => (
        <span className="text-sm mono">
          {item.base_url}
          <br />
          <span className="text-xs muted">
            凭据：{SOURCE_LABEL[item.credential_source]}
            {item.credential_masked ? `（${item.credential_masked}）` : ''}
            {item.credential_ref ? ` · 引用 ${item.credential_ref}` : ''}
          </span>
        </span>
      ),
    },
    {
      key: 'status',
      header: '状态',
      tight: true,
      render: (item) => <MetaBadge map={STATUS_META} value={item.status} context="ERP 连接状态" />,
    },
    {
      key: 'updated',
      header: '更新时间',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDateTime(item.updated_at)}
          <br />
          <span className="text-xs muted">{formatRelative(item.updated_at)}</span>
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <div style={{ display: 'flex', gap: 6 }}>
          <PermissionGate permission="admin.connector.write">
            <Button variant="secondary" size="sm" onClick={() => openEdit(item)}>
              编辑
            </Button>
            {item.status !== 'DELETED' ? (
              <Button variant="secondary" size="sm" onClick={() => setDeleting(item)}>
                删除
              </Button>
            ) : null}
          </PermissionGate>
        </div>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="link"
        title="与ERP集成"
        subtitle="ERP 系统类型字典（默认用友NC65）+ 租户级连接配置：地址、凭据（密文存储，只回显打码值）、状态与连接测试。"
      />

      {/* ERP 系统类型字典 */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="card__header">
          <h2 className="card__title">ERP 系统类型</h2>
          <PermissionGate permission="admin.connector.write">
            <Button variant="primary" size="sm" onClick={openCreate} icon="plus">
              新增连接
            </Button>
          </PermissionGate>
        </div>
        <div className="card__body">
          {systemsQuery.isLoading ? (
            <p className="text-sm muted">正在加载 ERP 系统字典…</p>
          ) : systemsQuery.error ? (
            <ErrorState error={systemsQuery.error} onRetry={() => void systemsQuery.refetch()} />
          ) : systems.length === 0 ? (
            <p className="text-sm" style={{ color: 'var(--color-danger)' }}>
              ERP 系统字典为空：迁移 000036 未执行或字典数据缺失，请检查部署。
            </p>
          ) : (
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              {systems.map((s) => (
                <span
                  key={s.system_code}
                  className="badge"
                  style={{ fontSize: 13, padding: '6px 10px' }}
                  title={s.description}
                >
                  {s.display_name}
                  {s.is_default ? <strong style={{ marginLeft: 6 }}>（默认）</strong> : null}
                  {!s.implemented ? (
                    <span className="text-xs muted" style={{ marginLeft: 6 }}>
                      · 适配器未实现
                    </span>
                  ) : null}
                </span>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* ERP 连接列表 */}
      <ListFilterBar
        searchValue={filters.q}
        onSearchChange={(q) => setFilters((f) => ({ ...f, q }))}
        searchPlaceholder="搜索连接名称 / 地址…"
        statusValue={filters.status}
        onStatusChange={(status) => setFilters((f) => ({ ...f, status }))}
        statusOptions={STATUS_OPTIONS}
      />

      {testMutation.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={testMutation.error} compact />
        </div>
      ) : null}
      {testMutation.isSuccess && testMutation.data ? (
        <div
          className={`banner ${testMutation.data.reachable ? 'banner--success' : 'banner--warning'}`}
          style={{ marginBottom: 16 }}
          role="status"
        >
          <span className="banner__icon" aria-hidden="true">
            <Icon name={testMutation.data.reachable ? 'circleCheck' : 'alertTriangle'} size={16} />
          </span>
          <span>连接测试结果：{testMutation.data.message || JSON.stringify(testMutation.data)}</span>
        </div>
      ) : null}

      <DataTable
        caption="ERP 连接列表"
        columns={connectionColumns}
        rows={connectionsQuery.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={connectionsQuery.isLoading}
        error={connectionsQuery.error}
        onRetry={() => void connectionsQuery.refetch()}
        emptyTitle="尚未创建 ERP 连接"
        emptyDescription="点击「新增连接」配置 ERP 系统类型、地址与凭据；连接创建后将作为该 ERP 类型下工具执行的首选装配。"
        nextCursor={connectionsQuery.data?.next_cursor ?? null}
      />

      {/* 新增 / 编辑连接弹窗 */}
      <Dialog
        open={formOpen}
        onClose={() => setFormOpen(false)}
        title={editing ? `编辑连接：${editing.name}` : '新增 ERP 连接'}
        wide>
        <Field label="ERP 系统类型" hint={editing ? '类型创建后不可变更' : '字典内置：默认用友NC65；未实现适配器的类型不可创建'}>
          <Select
            value={form.system_code}
            disabled={Boolean(editing)}
            options={systems.map((s) => ({
              value: s.system_code,
              label: `${s.display_name}${s.implemented ? '' : '（适配器未实现）'}`,
              disabled: !s.implemented,
            }))}
            onChange={(e) => setForm({ ...form, system_code: e.target.value })}
          />
        </Field>
        <Field label="连接名称" hint="用于列表展示与审计记录">
          <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </Field>
        <Field label="Base URL" hint="ERP 系统服务地址，如 http://nc65.example.com">
          <Input value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} />
        </Field>
        {form.system_code === 'nc65' ? (
          <>
            <Field
              label={editing ? '用户编码（留空不修改）' : '用户编码'}
              hint="NC65 登录用户编码；凭据将以密文存储，仅回显打码值">
              <Input value={form.usercode} onChange={(e) => setForm({ ...form, usercode: e.target.value })} />
            </Field>
            <Field label={editing ? '密码（留空不修改）' : '密码'}>
              <Input
                type="password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
              />
            </Field>
          </>
        ) : null}
        {!editing ? (
          <Field label="凭据环境变量引用（可选）" hint="与直接录入凭据二选一：填环境变量名，运行时从进程环境解析">
            <Input
              value={form.credential_ref}
              onChange={(e) => setForm({ ...form, credential_ref: e.target.value })}
            />
          </Field>
        ) : null}
        {formError ? (
          <p className="text-sm" style={{ color: 'var(--color-danger)' }} role="alert">
            {formError}
          </p>
        ) : null}
        {testMutation.data ? (
          <p
            className="text-sm"
            style={{ color: testMutation.data.reachable ? 'var(--color-success)' : 'var(--color-warning)' }}
            role="status">
            最近一次测试：{testMutation.data.message}
          </p>
        ) : null}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 12 }}>
          <Button variant="secondary" loading={testMutation.isPending} onClick={testFormConnection} icon="plug">
            测试连接
          </Button>
          <Button
            variant="primary"
            loading={createMutation.isPending || updateMutation.isPending}
            onClick={submitForm}>
            {editing ? '保存修改' : '创建连接'}
          </Button>
        </div>
      </Dialog>

      <ConfirmDialog
        open={deleting !== null}
        title="删除 ERP 连接"
        confirmLabel="删除"
        target={deleting?.name ?? ''}
        impact="删除后该 ERP 类型将回落到环境变量装配（如已配置）；连接记录保留 DELETED 状态供审计追溯。"
        onCancel={() => setDeleting(null)}
        onConfirm={() => {
          if (!deleting) return;
          deleteMutation.mutate(deleting.id, {
            onSuccess: () => {
              setDeleting(null);
              void connectionsQuery.refetch();
            },
          });
        }}
      />

      {/* 已安装连接器插件清单（原有能力） */}
      <h2 style={{ marginTop: 28, marginBottom: 12, fontSize: 18 }}>已安装连接器插件</h2>
      {testConnectorMutation.isSuccess && lastTested ? (
        <div className="banner banner--success" style={{ marginBottom: 16 }} role="status">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="circleCheck" size={16} />
          </span>
          <span>
            连接器 <span className="copy-id">{lastTested}</span> 的安全探测完成：
            {testConnectorMutation.data?.detail ?? '无异常'}（探测结果仅代表连通性，不代表远端业务状态）。
          </span>
        </div>
      ) : null}
      <ListFilterBar
        searchValue={pluginSearch}
        onSearchChange={setPluginSearch}
        searchPlaceholder="搜索 plugin_key / 名称…"
      />
      <DataTable
        caption="已安装连接器插件列表"
        columns={buildPluginColumns(testConnectorMutation, setLastTested)}
        rows={connectorPluginsQuery.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={connectorPluginsQuery.isLoading}
        error={connectorPluginsQuery.error}
        onRetry={() => void connectorPluginsQuery.refetch()}
        emptyTitle="尚未安装连接器插件"
        emptyDescription="连接器插件来自插件发布流程（插件与版本发布页安装）。"
        nextCursor={connectorPluginsQuery.data?.next_cursor ?? null}
        hasPrevPage={pluginPager.hasPrevPage}
        onPrevPage={pluginPager.goPrev}
        onNextPage={() => pluginPager.goNext(connectorPluginsQuery.data?.next_cursor ?? null)}
      />
    </div>
  );
}

type TestMutation = ReturnType<typeof useTestConnector>;

function buildPluginColumns(
  testMutation: TestMutation,
  setLastTested: (id: string) => void,
): DataTableColumn<Connector>[] {
  return [
    {
      key: 'name',
      header: '连接器',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 类型 {item.type} · 认证 {item.auth_method}
          </div>
        </div>
      ),
    },
    {
      key: 'endpoint',
      header: 'Endpoint',
      render: (item) => (
        <span className="text-sm mono">
          {item.endpoint}
          <br />
          <span className="text-xs muted">
            凭据：{item.credential_configured ? '已配置（不回显明文）' : '未配置'}
          </span>
        </span>
      ),
    },
    {
      key: 'capability',
      header: '能力',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {item.read_only ? <Badge tone="info">只读</Badge> : <Badge tone="warning">可写</Badge>}
          <br />
          <span className="text-xs muted">{item.resource_scope.join('、')}</span>
        </span>
      ),
    },
    {
      key: 'mapping',
      header: '数据映射版本',
      tight: true,
      render: (item) => <span className="mono text-sm">{item.mapping_version ?? '—'}</span>,
    },
    {
      key: 'health',
      header: '健康',
      tight: true,
      render: (item) => <MetaBadge map={HEALTH_META} value={item.health} context="连接器健康度" />,
    },
    {
      key: 'sync',
      header: '最近同步',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDateTime(item.last_sync_at)}
          <br />
          <span className="text-xs muted">{formatRelative(item.last_sync_at)}</span>
          {item.last_error ? (
            <>
              <br />
              <span className="text-xs" style={{ color: 'var(--color-warning)' }}>
                {item.last_error}
              </span>
            </>
          ) : null}
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <PermissionGate permission="admin.connector.write">
          <Button
            variant="secondary"
            size="sm"
            loading={testMutation.isPending && testMutation.variables === item.id}
            onClick={() =>
              testMutation.mutate(item.id, {
                onSuccess: () => setLastTested(item.id),
              })
            }
            icon="plug">
            测试连接
          </Button>
        </PermissionGate>
      ),
    },
  ];
}
