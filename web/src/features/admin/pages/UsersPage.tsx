import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { apiDelete, apiPatch, apiPost, fetchList } from '@/api/client';
import { PageHeader } from '@/components/ui/PageHeader';
import { Button } from '@/components/ui/Button';
import { DataTable, type DataTableColumn } from '@/components/data-table/DataTable';
import { Input, Field } from '@/components/ui';
import { ListFilterBar, FILTER_ALL_OPTION } from '@/components/list-filter-bar';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { useEffect, useState } from 'react';
import { Dialog } from '@/components/ui/Dialog';

interface UserRow {
  id: string;
  login_name: string;
  display_name: string;
  email?: string;
  status: string;
  subject_version: number;
  created_at: string;
  memberships?: string;
}

const EMPTY_CREATE = {
  login_name: '',
  display_name: '',
  email: '',
  password: '',
  org_code: '',
  membership_type: 'EMPLOYEE',
};

/** 用户状态词表（iam.users.status：创建为 ACTIVE，停用后为 DISABLED）。 */
const STATUS_OPTIONS = [
  FILTER_ALL_OPTION,
  { value: 'ACTIVE', label: '启用' },
  { value: 'DISABLED', label: '停用' },
];

export function UsersPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = useQuery({
    queryKey: ['admin', 'users', { q: search, status, cursor: pager.cursor ?? '' }],
    queryFn: () =>
      fetchList<UserRow>('/admin/users', {
        limit: 10,
        q: search || undefined,
        status: status || undefined,
        cursor: pager.cursor || undefined,
      }),
  });

  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ ...EMPTY_CREATE });

  const [editOpen, setEditOpen] = useState(false);
  const [editUser, setEditUser] = useState<UserRow | null>(null);
  const [editForm, setEditForm] = useState({ display_name: '', email: '', password: '' });

  const create = useMutation({
    mutationFn: (body: typeof form) => apiPost('/admin/users', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] });
      setOpen(false);
      setForm({ ...EMPTY_CREATE });
    },
  });

  const update = useMutation({
    mutationFn: (payload: { id: string; body: Record<string, unknown> }) =>
      apiPatch(`/admin/users/${payload.id}`, payload.body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['admin', 'users'] });
      setEditOpen(false);
      setEditUser(null);
    },
  });

  const disable = useMutation({
    mutationFn: (id: string) => apiDelete(`/admin/users/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['admin', 'users'] }),
  });

  // 筛选条件变化必须回到首页：否则会带着旧页游标去请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [search, status, pager.reset]);

  const openEdit = (row: UserRow) => {
    setEditUser(row);
    setEditForm({ display_name: row.display_name, email: row.email ?? '', password: '' });
    setEditOpen(true);
  };

  const submitEdit = () => {
    if (!editUser) return;
    const body: Record<string, unknown> = {
      display_name: editForm.display_name,
      email: editForm.email,
    };
    // 留空表示不修改密码；填写时才提交，避免误清空凭据。
    if (editForm.password) body.password = editForm.password;
    update.mutate({ id: editUser.id, body });
  };

  const columns: DataTableColumn<UserRow>[] = [
    {
      key: 'id',
      header: '用户编码',
      render: (row) => <span className="mono text-sm copy-id">{row.id}</span>,
    },
    { key: 'login_name', header: '登录名', render: (row) => row.login_name },
    { key: 'display_name', header: '显示名', render: (row) => row.display_name },
    { key: 'email', header: '邮箱', render: (row) => row.email ?? '—' },
    {
      key: 'memberships',
      header: '组织/角色',
      tight: true,
      render: () => (
        <Button variant="ghost" size="sm" trailingIcon="arrowRight" onClick={() => navigate('/admin/identity')}>
          查看
        </Button>
      ),
    },
    {
      key: 'status',
      header: '状态',
      render: (row) => (
        <span className={`status status-${row.status.toLowerCase()}`}>{row.status}</span>
      ),
    },
    {
      key: 'created_at',
      header: '创建时间',
      render: (row) => new Date(row.created_at).toLocaleString(),
    },
    {
      key: 'actions',
      header: '操作',
      render: (row) => (
        <div style={{ display: 'flex', gap: 8 }}>
          <Button variant="ghost" size="sm" icon="pencil" onClick={() => openEdit(row)}>修改</Button>
          {row.status !== 'DISABLED' ? (
            <Button variant="ghost" size="sm" onClick={() => disable.mutate(row.id)}>停用</Button>
          ) : (
            <span className="muted">已停用</span>
          )}
        </div>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        icon="user"
        title="用户管理"
        subtitle="查看、创建、修改、停用租户内用户，并分配组织成员关系。密码由 bcrypt 哈希存储，明文永不落库。"
        actions={
          <Button variant="primary" icon="plus" onClick={() => setOpen(true)}>新建用户</Button>
        }
      />

      <ListFilterBar
        searchValue={search}
        onSearchChange={setSearch}
        searchPlaceholder="搜索登录名 / 显示名 / 邮箱…"
        statusValue={status}
        onStatusChange={setStatus}
        statusOptions={STATUS_OPTIONS}
      />

      <DataTable<UserRow>
        caption="用户列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(row) => row.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无用户"
        emptyDescription="租户下的用户会通过注册或管理员创建自动加入组织成员关系。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      <Dialog open={open} onClose={() => setOpen(false)} title="新建用户">
        <form
          className="dialog-form"
          onSubmit={(e) => { e.preventDefault(); create.mutate(form); }}
        >
          <Field label="组织编码" required>
            <Input value={form.org_code} onChange={(e) => setForm({ ...form, org_code: e.target.value })} required />
          </Field>
          <Field label="登录名" required>
            <Input value={form.login_name} onChange={(e) => setForm({ ...form, login_name: e.target.value })} required />
          </Field>
          <Field label="显示名" required>
            <Input value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} required />
          </Field>
          <Field label="邮箱">
            <Input type="email" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
          </Field>
          <Field label="密码" required>
            <Input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} minLength={6} required />
          </Field>
          <Field label="成员类型">
            <Input value={form.membership_type} onChange={(e) => setForm({ ...form, membership_type: e.target.value })} />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>取消</Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? '保存中…' : '创建'}
            </Button>
          </div>
        </form>
      </Dialog>

      <Dialog open={editOpen} onClose={() => setEditOpen(false)} title="修改用户">
        <form
          className="dialog-form"
          onSubmit={(e) => { e.preventDefault(); submitEdit(); }}
        >
          <Field label="用户编码">
            <Input value={editUser?.id ?? ''} readOnly disabled />
          </Field>
          <Field label="登录名">
            <Input value={editUser?.login_name ?? ''} readOnly disabled />
          </Field>
          <Field label="显示名" required>
            <Input value={editForm.display_name} onChange={(e) => setEditForm({ ...editForm, display_name: e.target.value })} required />
          </Field>
          <Field label="邮箱">
            <Input type="email" value={editForm.email} onChange={(e) => setEditForm({ ...editForm, email: e.target.value })} />
          </Field>
          <Field label="重置密码">
            <Input
              type="password"
              placeholder="留空则不修改密码"
              value={editForm.password}
              onChange={(e) => setEditForm({ ...editForm, password: e.target.value })}
              minLength={6}
            />
          </Field>
          <div className="dialog-actions">
            <Button type="button" variant="ghost" onClick={() => setEditOpen(false)}>取消</Button>
            <Button type="submit" variant="primary" disabled={update.isPending}>
              {update.isPending ? '保存中…' : '保存'}
            </Button>
          </div>
        </form>
      </Dialog>
    </div>
  );
}
