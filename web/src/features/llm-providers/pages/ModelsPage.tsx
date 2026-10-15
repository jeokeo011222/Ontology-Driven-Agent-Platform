import { useMemo, useState } from 'react';
import { Badge, Button, PageHeader } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { PermissionGate } from '@/components/permission-gate';
import { MetaBadge, type BadgeMeta } from '@/components/status-badge';
import { formatDateTime } from '@/lib/format';
import { Icon } from '@/components/icon';
import { ConfirmDialog } from '@/components/confirm-dialog';
import type { ModelEndpointSummary, ModelProviderSummary } from '@/types';
import {
  useModelProviders, useUpdateModelProvider, useDeleteModelProvider,
  useAddModelEndpoint, useUpdateModelEndpoint,
  type AddModelEndpointInput,
  type UpdateModelEndpointInput,
} from '../api';
import { CreateProviderDialog } from '../CreateProviderDialog';
import { EditAccessDialog, type EditAccessSubmitPayload } from '../ProviderDialogs';

/**
 * 复合状态词表：一行 = 供应商 + 其主端点，二者同义时不重复展示。
 *
 * 此前供应商与端点各渲染一枚徽标，二者同为 ACTIVE 时并排出现两个一模一样的
 * 「启用」——既是视觉噪音，也让人误以为这是两个不同维度的信息。
 */
const ACCESS_STATUS_META: Record<string, BadgeMeta> = {
  ENABLED: { label: '启用', tone: 'success', title: '供应商与其端点均启用，可参与路由选择' },
  PROVIDER_DISABLED: { label: '供应商已停用', tone: 'neutral', title: '供应商被显式停用，其全部端点不参与路由' },
  ENDPOINT_DISABLED: { label: '端点已停用', tone: 'neutral', title: '该端点被停用（装配期会排除并记录原因）；供应商仍为启用' },
  NO_ENDPOINT: { label: '无端点', tone: 'warning', title: '该供应商名下暂无端点，无法参与路由' },
};

/** 由供应商状态 + 主端点状态推导复合状态（取值见 ACCESS_STATUS_META）。 */
function accessStatus(providerStatus: string, endpointStatus: string | undefined): string {
  if (providerStatus !== 'ACTIVE') return 'PROVIDER_DISABLED';
  if (endpointStatus === undefined) return 'NO_ENDPOINT';
  return endpointStatus === 'ACTIVE' ? 'ENABLED' : 'ENDPOINT_DISABLED';
}

/**
 * 列表行 = 一条「接入条目」：供应商 + 其名下按创建时间最早的端点（主端点）。
 * 供应商的地址与凭据都落在端点上，故二者必须成对展示才有意义。
 */
interface AccessEntry {
  provider: ModelProviderSummary;
  /** 该供应商名下全部端点（created_at 升序） */
  endpoints: ModelEndpointSummary[];
  /** 主端点 = 最早创建的端点；无端点时为 null */
  primary: ModelEndpointSummary | null;
}

/** 操作按钮组组件（表格行内）。 */
function RowActions({ children }: { children: React.ReactNode }) {
  return <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', whiteSpace: 'nowrap' }}>{children}</div>;
}

/** 模型与推理管理控制台：供应商接入条目（供应商 + 主端点）的完整 CRUD。 */
export function ModelsPage() {
  const query = useModelProviders();
  const updateProvider = useUpdateModelProvider();
  const deleteProvider = useDeleteModelProvider();
  const addEndpoint = useAddModelEndpoint();
  const updateEndpoint = useUpdateModelEndpoint();

  // ---- 对话框状态 ----
  const [createOpen, setCreateOpen] = useState(false);
  const [editEntry, setEditEntry] = useState<AccessEntry | null>(null);
  const [editError, setEditError] = useState<unknown>(null);
  const [deleteTarget, setDeleteTarget] = useState<AccessEntry | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const providers = query.data?.providers ?? [];
  const endpoints = query.data?.endpoints ?? [];

  // ---- 行模型：供应商 + 按 created_at 升序的端点 ----
  const entries = useMemo<AccessEntry[]>(() => {
    const byProvider = new Map<string, ModelEndpointSummary[]>();
    for (const e of endpoints) {
      const list = byProvider.get(e.provider_id);
      if (list) list.push(e);
      else byProvider.set(e.provider_id, [e]);
    }
    for (const list of byProvider.values()) {
      list.sort((a, b) => a.created_at.localeCompare(b.created_at));
    }
    return providers.map((p) => {
      const list = byProvider.get(p.provider_id) ?? [];
      return { provider: p, endpoints: list, primary: list[0] ?? null };
    });
  }, [providers, endpoints]);

  const editBusy = updateProvider.isPending || addEndpoint.isPending || updateEndpoint.isPending;

  // ---- 事件处理 ----
  /**
   * 「编辑接入」提交编排：供应商元数据仅在**确有变化**时才 PATCH；
   * 端点则按 endpointID 是否为 null 决定新增（POST）或部分更新（PATCH）。
   * 两步串行，任一步失败即停并在对话框内呈现错误。
   */
  const handleEditAccess = (payload: EditAccessSubmitPayload) => {
    const entry = editEntry;
    if (!entry) return;
    setEditError(null);
    void (async () => {
      try {
        const nameChanged = payload.provider.display_name !== entry.provider.display_name;
        const statusChanged = payload.provider.status !== entry.provider.status;
        if (nameChanged || statusChanged) {
          await updateProvider.mutateAsync({
            providerID: entry.provider.provider_id,
            input: { display_name: payload.provider.display_name, status: payload.provider.status },
          });
        }
        if (payload.endpointID === null) {
          await addEndpoint.mutateAsync({
            providerID: entry.provider.provider_id,
            input: payload.endpoint as AddModelEndpointInput,
          });
        } else {
          await updateEndpoint.mutateAsync({
            endpointID: payload.endpointID,
            input: payload.endpoint as UpdateModelEndpointInput,
          });
        }
        setEditEntry(null);
        setNotice(`供应商 ${entry.provider.provider_id} 的接入配置已保存（列表已刷新）。`);
      } catch (e) {
        setEditError(e);
      }
    })();
  };

  const handleDelete = () => {
    if (!deleteTarget) return;
    const providerId = deleteTarget.provider.provider_id;
    deleteProvider.mutate(providerId, {
      onSuccess: () => {
        setDeleteTarget(null);
        setNotice(`供应商 ${providerId} 及其名下端点已删除。`);
      },
    });
  };

  /** 行内「启用 / 停用」：只切换**供应商**状态；端点状态在「编辑接入」里单独管理。 */
  const handleToggleStatus = (entry: AccessEntry) => {
    const next = entry.provider.status === 'ACTIVE' ? 'DISABLED' : 'ACTIVE';
    updateProvider.mutate(
      { providerID: entry.provider.provider_id, input: { status: next } },
      {
        onSuccess: () =>
          setNotice(`供应商 ${entry.provider.provider_id} 已${next === 'ACTIVE' ? '启用' : '停用'}。`),
      },
    );
  };

  // ---- 表格列 ----
  const columns: DataTableColumn<AccessEntry>[] = [
    {
      key: 'provider', header: '供应商',
      render: ({ provider }) => (
        <div>
          <div style={{ fontWeight: 500 }}>{provider.display_name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="mono">{provider.provider_id}</span> · 类型 <span className="mono">{provider.provider_type}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'endpoint', header: '端点 / 接入地址',
      render: ({ primary, endpoints: eps }) => (
        <div>
          <div className="mono" style={{ fontWeight: 500 }}>
            {primary?.endpoint_name ?? '—'}
            {eps.length > 1 ? <span className="text-xs muted"> 等 {eps.length} 个端点</span> : null}
          </div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            {primary?.base_url ? (
              <span className="mono">{primary.base_url}</span>
            ) : primary ? (
              '该端点未配置接入地址'
            ) : (
              '该供应商名下暂无端点'
            )}
          </div>
        </div>
      ),
    },
    { key: 'status', header: '状态', tight: true,
      render: ({ provider, primary }) => (
        <MetaBadge
          map={ACCESS_STATUS_META}
          value={accessStatus(provider.status, primary?.status)}
          context="模型供应商接入状态"
        />
      ),
    },
    {
      key: 'created_at', header: '创建时间', tight: true,
      render: ({ provider }) => <span className="text-sm">{formatDateTime(provider.created_at)}</span>,
    },
    {
      key: 'actions', header: '操作', tight: true, width: '1%',
      render: (item) => (
        <RowActions>
          <PermissionGate permission="admin.model.write">
            <Button variant="ghost" size="sm"
              onClick={() => { setEditError(null); setEditEntry(item); }}
              title="编辑供应商显示名 / 状态，以及端点的地址与密钥（密钥留空即不改动）">
              <Icon name="pencil" size={14} /> 编辑接入
            </Button>
            <Button variant="ghost" size="sm"
              loading={updateProvider.isPending}
              onClick={() => handleToggleStatus(item)}
              title={item.provider.status === 'ACTIVE'
                ? '停用该供应商：其全部端点不再参与路由'
                : '启用该供应商'}>
              {item.provider.status === 'ACTIVE' ? '停用' : '启用'}
            </Button>
            <Button variant="danger" size="sm" onClick={() => setDeleteTarget(item)}
              title="删除该供应商及其名下全部端点">
              <Icon name="x" size={14} /> 删除
            </Button>
          </PermissionGate>
        </RowActions>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="cpu"
        title="模型与推理"
        subtitle="每行 = 供应商 + 其端点，地址与密钥都落在端点上；密钥加密存储，界面只回显打码值。"
        actions={
          <PermissionGate permission="admin.model.write">
            <Button variant="primary" size="sm" icon="plus" onClick={() => { setNotice(null); setCreateOpen(true); }}>
              新建供应商
            </Button>
          </PermissionGate>
        }
      />

      {notice ? (
        <div className="banner banner--success" style={{ marginBottom: 16 }} role="status">
          <Icon name="check" size={16} />
          {notice}
        </div>
      ) : null}

      <section className="section" aria-labelledby="entries-title">
        <h2 className="section__title" id="entries-title">模型供应商</h2>
        <DataTable
          caption="模型供应商接入条目列表"
          columns={columns}
          rows={entries}
          rowKey={(item) => item.provider.provider_id}
          loading={query.isLoading}
          error={query.error}
          onRetry={() => void query.refetch()}
          emptyTitle="尚未配置模型供应商"
          emptyDescription="库中 llm.providers 暂无记录。点击「新建供应商」录入第一条接入条目（同时创建其首个端点）。"
        />
      </section>

      <p className="text-xs muted" style={{ marginTop: 8 }}>
        <Badge tone="info">读写</Badge> 数据更新时间：
        {formatDateTime(query.dataUpdatedAt ? new Date(query.dataUpdatedAt).toISOString() : undefined)}
      </p>

      {/* ---- 对话框 ---- */}

      <CreateProviderDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(providerId) => {
          setCreateOpen(false);
          setNotice(`供应商 ${providerId} 已创建（含首个端点）。`);
        }}
      />

      <EditAccessDialog
        open={!!editEntry}
        provider={editEntry?.provider ?? null}
        endpoints={editEntry?.endpoints ?? []}
        busy={editBusy}
        error={editError}
        onClose={() => { setEditEntry(null); setEditError(null); }}
        onSubmit={handleEditAccess}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        title="删除模型供应商"
        confirmLabel="删除供应商及其所有端点"
        danger
        busy={deleteProvider.isPending}
        error={deleteProvider.error}
        target={
          <span>
            <strong>{deleteTarget?.provider.display_name}</strong>{' '}
            <span className="mono muted">({deleteTarget?.provider.provider_id})</span>
          </span>
        }
        impact={
          <span>
            <strong>单事务级联删除：</strong>该供应商名下的 {deleteTarget?.endpoints.length ?? 0} 个端点会一并删除。
            若其中任一端点已被 llm.model_revisions 或 llm.usage_ledger 引用（审计链），
            后端会返回 409 拒绝，并提示改为停用供应商。
          </span>
        }
        onConfirm={handleDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  );
}
