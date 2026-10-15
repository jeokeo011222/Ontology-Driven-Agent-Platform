import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiDelete, apiGet, apiPatch, apiPost } from '@/api/client';
import { PageHeader } from '@/components/ui/PageHeader';
import { Button } from '@/components/ui/Button';
import { DataTable, type DataTableColumn } from '@/components/data-table/DataTable';
import { Input, Field } from '@/components/ui';
import { ListFilterBar } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { useEffect, useState } from 'react';
import { Dialog } from '@/components/ui/Dialog';

interface OrgRow {
  id: string;
  parent_id?: string;
  org_code: string;
  name: string;
  org_type: string;
  status: string;
  created_at: string;
}

const EMPTY_CREATE = { org_code: '', name: '', org_type: 'DEPARTMENT', parent_id: '' };

export function OrgsPage() {
  const qc = useQueryClient();
  const [search, setSearch] = useState('');
  const pager = useCursorPagination();
  const query = useQuery<{ items: OrgRow[]; next_cursor: string | null }>({
    queryKey: ['admin', 'organizations', { q: search, cursor: pager.cursor ?? '' }],
    queryFn: () =>
      apiGet('/admin/organizations', {
        query: { limit: 10, q: search || undefined, cursor: pager.cursor || undefined },
      }),
  });

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, pager.reset]);

  const [open, setOpen] = useState(false);
  const [form, setForm] = useState(EMPTY_CREATE);

  // 修改：仅名称可改，组织编码为标识字段，不可修改。
  const [editing, setEditing] = useState<OrgRow | null>(null);
  const [editName, setEditName] = useState('');

  const create = useMutation({
    mutationFn: (body: typeof form) => apiPost('/admin/organizations', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'organizations'] });
      setOpen(false);
      setForm(EMPTY_CREATE);
    },
  });

  const update = useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      apiPatch(`/admin/organizations/${id}`, { name }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'organizations'] });
      setEditing(null);
    },
  });

  const disable = useMutation({
    mutationFn: (id: string) => apiDelete(`/admin/organizations/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'organizations'] }),
  });

  const openEdit = (row: OrgRow) => {
    setEditing(row);
    setEditName(row.name);
  };

  const columns: DataTableColumn<OrgRow>[] = [
    { key: 'org_code', header: '编码', render: (row) => row.org_code },
    { key: 'name', header: '名称', render: (row) => row.name },
    { key: 'org_type', header: '类型', render: (row) => row.org_type },
    {
      key: 'status',
      header: '状态',
      render: (row) => <span className={`status status-${row.status.toLowerCase()}`}>{row.status}</span>,
    },
    {
      key: 'created_at',
      header: '创建时间',
      render: (row) => new Date(row.created_at).toLocaleString(),
    },
    {
      key: 'actions',
      header: '操作',
      render: (row) =>
        row.status !== 'INACTIVE' ? (
          <>
            <Button variant="ghost" size="sm" onClick={() => openEdit(row)}>修改</Button>
            <Button variant="ghost" size="sm" onClick={() => disable.mutate(row.id)}>停用</Button>
          </>
        ) : (
          <span className="muted">已停用</span>
        ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        icon="building"
        title="组织管理"
        subtitle="维护租户内的组织树结构。设置 parent_id 自动填充 org_closure 闭包表以支撑多层级权限检查。"
        actions={
          <Button variant="primary" icon="plus" onClick={() => setOpen(true)}>新建组织</Button>
        }
      />

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索组织编码 / 名称…"
      />

      <DataTable<OrgRow>
        caption="组织列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(row) => row.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无组织"
        emptyDescription="组织是用户与权限的挂载点。注册用户需通过 org_code 加入对应组织。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      <Dialog open={open} onClose={() => setOpen(false)} title="新建组织">
        <form
          className="dialog-form"
          onSubmit={(e) => { e.preventDefault(); create.mutate(form); }}
        >
          <Field label="组织编码" required>
            <Input value={form.org_code} onChange={(e) => setForm({ ...form, org_code: e.target.value })} required />
          </Field>
          <Field label="名称" required>
            <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
          </Field>
          <Field label="类型">
            <Input value={form.org_type} onChange={(e) => setForm({ ...form, org_type: e.target.value })} />
          </Field>
          <Field label="父组织 ID（留空=根）">
            <Input value={form.parent_id} onChange={(e) => setForm({ ...form, parent_id: e.target.value })} placeholder="留空创建根组织" />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>取消</Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? '保存中…' : '创建'}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={editing !== null} onClose={() => setEditing(null)} title="修改组织">
        <form
          className="dialog-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (editing) update.mutate({ id: editing.id, name: editName });
          }}
        >
          <Field label="组织编码" hint="编码是组织的唯一标识，创建后不可修改。">
            <Input value={editing?.org_code ?? ''} readOnly disabled />
          </Field>
          <Field label="名称" required>
            <Input value={editName} onChange={(e) => setEditName(e.target.value)} required />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setEditing(null)}>取消</Button>
            <Button type="submit" variant="primary" disabled={update.isPending}>
              {update.isPending ? '保存中…' : '保存'}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  );
}
