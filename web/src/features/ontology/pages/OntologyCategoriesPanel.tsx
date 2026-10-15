import type { ReactNode } from 'react';
import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiGet, apiPatch, apiPost } from '@/api/client';
import { Button, Dialog, Field, Input } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { PermissionGate } from '@/components/permission-gate';

interface CategoryRow {
  id: string;
  parent_id: string;
  category_code: string;
  name: string;
  description: string;
  sort_order: number;
  status: string;
  ontology_count: number;
  created_at: string;
}

/**
 * 业务领域分类（原「本体分类」）管理面板。
 *
 * 作为「企业本体」页面的页内分区渲染（`/admin/ontologies`），不再占用独立路由。
 * 分类下有本体时不允许删除，只能停用（服务端 409，见 store.DeleteOntologyCategory）。
 *
 * 与 `OntologyListPage` 的 AI 生成弹窗共用 query key `['admin','ontology-categories']`：
 * 在此新增/停用分类后，弹窗的业务领域下拉会一并刷新。
 */
export function OntologyCategoriesPanel() {
  const qc = useQueryClient();
  const query = useQuery<{ items: CategoryRow[] }>({
    queryKey: ['admin', 'ontology-categories'],
    queryFn: () => apiGet('/admin/ontology-categories'),
  });

  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ category_code: '', name: '', description: '', parent_id: '', sort_order: 0 });

  const create = useMutation({
    mutationFn: (body: typeof form) => apiPost('/admin/ontology-categories', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'ontology-categories'] });
      setOpen(false);
      setForm({ category_code: '', name: '', description: '', parent_id: '', sort_order: 0 });
    },
  });

  const disable = useMutation({
    mutationFn: (id: string) =>
      apiPatch(`/admin/ontology-categories/${id}`, { status: 'INACTIVE' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'ontology-categories'] }),
  });

  const del = useMutation({
    mutationFn: (id: string) => apiDelete(`/admin/ontology-categories/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'ontology-categories'] }),
  });

  const columns: DataTableColumn<CategoryRow>[] = [
    { key: 'category_code', header: '编码', render: (row) => row.category_code },
    { key: 'name', header: '名称', render: (row) => row.name },
    { key: 'sort_order', header: '排序', render: (row) => row.sort_order },
    {
      key: 'ontology_count',
      header: '本体数',
      render: (row) => (
        <span className={`mono ${row.ontology_count > 0 ? 'muted' : ''}`}>{row.ontology_count}</span>
      ),
    },
    {
      key: 'status',
      header: '状态',
      render: (row) => <span className={`status status-${row.status.toLowerCase()}`}>{row.status}</span>,
    },
    {
      key: 'actions',
      header: '操作',
      render: (row) => {
        const actions: ReactNode[] = [];
        if (row.status !== 'INACTIVE') {
          actions.push(
            <Button
              key="disable"
              variant="ghost"
              size="sm"
              onClick={() => disable.mutate(row.id)}
            >
              停用
            </Button>,
          );
        }
        actions.push(
          <Button
            key="delete"
            variant="ghost"
            size="sm"
            onClick={() => del.mutate(row.id)}
            title={
              row.ontology_count > 0
                ? `该分类下有 ${row.ontology_count} 个本体，删除将返回 409 CONFLICT`
                : '删除分类（若无本体引用）'
            }
          >
            删除
          </Button>,
        );
        return (
          <PermissionGate permission="admin.ontology.write">
            <div className="row-actions">{actions}</div>
          </PermissionGate>
        );
      },
    },
  ];

  return (
    <div>
      <div className="row-actions" style={{ justifyContent: 'space-between', marginBottom: 12 }}>
        <span className="text-sm muted">
          业务领域分类是企业本体的分组维度，如「财务域」「合同域」「税务域」。分类下有本体时只能停用。
        </span>
        <PermissionGate permission="admin.ontology.write">
          <Button variant="primary" icon="plus" onClick={() => setOpen(true)}>
            新建业务领域分类
          </Button>
        </PermissionGate>
      </div>

      <DataTable<CategoryRow>
        caption="业务领域分类列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(row) => row.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无业务领域分类"
        emptyDescription="业务领域分类是企业本体的分组维度，如「财务域」「合同域」「税务域」。"
        nextCursor={null}
      />

      <Dialog open={open} onClose={() => setOpen(false)} title="新建业务领域分类">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate(form);
          }}
        >
          <Field label="分类编码" required>
            <Input
              value={form.category_code}
              onChange={(e) => setForm({ ...form, category_code: e.target.value })}
              placeholder="租户内业务唯一"
              required
            />
          </Field>
          <Field label="名称" required>
            <Input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              required
            />
          </Field>
          <Field label="描述">
            <Input
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
            />
          </Field>
          <Field label="父分类 ID（留空=根）">
            <Input
              value={form.parent_id}
              onChange={(e) => setForm({ ...form, parent_id: e.target.value })}
              placeholder="UUID 或留空"
            />
          </Field>
          <Field label="排序">
            <Input
              type="number"
              value={form.sort_order}
              onChange={(e) => setForm({ ...form, sort_order: parseInt(e.target.value) || 0 })}
            />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>取消</Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? '保存中…' : '创建'}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  );
}
