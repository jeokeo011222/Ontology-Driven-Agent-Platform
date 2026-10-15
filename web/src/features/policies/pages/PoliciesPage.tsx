import { useEffect, useState } from 'react';
import { Button, Card, CardBody, CardHeader, Field, PageHeader, Select } from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { ErrorState } from '@/components/error-state';
import { PermissionGate } from '@/components/permission-gate';
import { MetaBadge, RiskBadge } from '@/components/status-badge';
import { useCursorPagination } from '@/hooks/use-cursor-pagination';
import { formatDateTime } from '@/lib/format';
import type { BadgeTone } from '@/components/ui';
import type { Policy, PolicyExecutionMode } from '@/types';
import { useCreatePolicyVersion, usePolicies } from '../api';
import { Icon } from '@/components/icon';

/**
 * 策略状态词表（真源：migrations/platform/000002_create_governance.sql
 *   governance.policy_versions.status CHECK 的真实取值集合）。
 */
const STATUS_OPTIONS = [
  { value: 'DRAFT', label: '草稿' },
  { value: 'VALIDATING', label: '校验中' },
  { value: 'PUBLISHED', label: '已发布' },
  { value: 'DISABLED', label: '已停用' },
  { value: 'RETIRED', label: '已下线' },
];

const MODE_META: Record<PolicyExecutionMode, { label: string; tone: BadgeTone }> = {
  auto: { label: '自动执行', tone: 'info' },
  user_confirm: { label: '用户确认', tone: 'warning' },
  approval_required: { label: '需审批', tone: 'warning' },
  dual_approval: { label: '双人审批', tone: 'error' },
  forbidden: { label: '禁止执行', tone: 'error' },
};

const STATE_META: Record<Policy['state'], { label: string; tone: BadgeTone }> = {
  draft: { label: '草稿', tone: 'neutral' },
  validated: { label: '已验证', tone: 'info' },
  published: { label: '已发布', tone: 'success' },
  deprecated: { label: '已废弃', tone: 'neutral' },
};

/**
 * MVP 页面 5c：策略与审批 `/admin/policies`（§8.4 / §14.3）
 */
export function PoliciesPage() {
  const [status, setStatus] = useState('');
  const pager = useCursorPagination();
  const query = usePolicies({ status, cursor: pager.cursor ?? undefined });
  const createVersion = useCreatePolicyVersion();
  const [target, setTarget] = useState<Policy | null>(null);

  // 筛选变化必须回到首页：否则会带着旧页游标请求新筛选结果（假结果）。
  useEffect(() => {
    pager.reset();
  }, [status, pager.reset]);

  const columns: DataTableColumn<Policy>[] = [
    {
      key: 'name',
      header: '策略',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 版本 <span className="mono">{item.version}</span>
          </div>
        </div>
      ),
    },
    {
      key: 'target',
      header: '资源 / 动作',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {item.resource_type}
          <br />
          <span className="mono">{item.action}</span>
        </span>
      ),
    },
    { key: 'risk', header: '风险', tight: true, render: (item) => <RiskBadge level={item.risk_level} /> },
    {
      key: 'mode',
      header: '执行要求',
      tight: true,
      render: (item) => <MetaBadge map={MODE_META} value={item.execution_mode} context="策略执行模式" />,
    },
    {
      key: 'state',
      header: '状态',
      tight: true,
      render: (item) => <MetaBadge map={STATE_META} value={item.state} context="策略状态" />,
    },
    {
      key: 'updated',
      header: '最近更新',
      tight: true,
      render: (item) => <span className="text-sm">{formatDateTime(item.updated_at)}</span>,
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <PermissionGate permission="admin.policy.write">
          <Button
            variant={item.state === 'published' ? 'secondary' : 'primary'}
            size="sm"
            onClick={() => setTarget(item)}
          >
            {item.state === 'published' ? '创建新版本' : '编辑草稿'}
          </Button>
        </PermissionGate>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="scale"
        title="策略与审批"
        subtitle="执行策略分为操作分类、授权范围、风险分级与执行要求四层。已发布策略不可原地修改，修改须创建新版本并经过校验与门禁。"
      />

      <div className="banner banner--neutral" style={{ marginBottom: 16 }}>
        <span className="banner__icon" aria-hidden="true">
          <Icon name="flag" size={16} />
        </span>
        <span>
          需特别区分：<strong>审批策略</strong>决定何时需要人类授权；<strong>权限策略</strong>决定主体是否有资格执行；
          <strong>业务规则</strong>决定业务对象是否满足条件；<strong>系统执行策略</strong>决定超时、重试、对账与恢复。
          四者不能合并为一个 LLM 判断。
        </span>
      </div>

      {createVersion.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={createVersion.error} compact />
        </div>
      ) : null}

      <div className="filters">
        <Field label="策略状态" htmlFor="policy-status">
          <Select
            id="policy-status"
            value={status}
            options={STATUS_OPTIONS}
            placeholder="全部"
            onChange={(e) => setStatus(e.target.value)}
          />
        </Field>
      </div>

      <DataTable
        caption="策略列表"
        columns={columns}
        rows={query.data?.items ?? []}
        rowKey={(item) => item.id}
        loading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        emptyTitle="暂无策略"
        emptyDescription="策略应由企业业务与安全负责人确认后创建。"
        nextCursor={query.data?.next_cursor ?? null}
        hasPrevPage={pager.hasPrevPage}
        onPrevPage={pager.goPrev}
        onNextPage={() => pager.goNext(query.data?.next_cursor ?? null)}
      />

      <Card className="section" >
        <CardHeader title="策略四层结构说明" description="示例结构（实际政策必须经类型校验、策略测试与发布审批）" />
        <CardBody>
          <pre className="code-block">{`policy_id: finance.expense.submit
version: "1.0.0"

scope:            # 授权范围
  tenant: current
  resource_type: ExpenseClaim

operation:        # 操作分类与风险分级
  action: SUBMIT_FOR_APPROVAL
  risk_level: medium

execution:        # 执行要求
  mode: approval_required
  require_authorization: true
  require_fresh_resource_state: true
  bind_approval_to_payload: true
  idempotency_required: true

failure:          # 系统执行策略
  unknown_external_result: reconcile
  reconciliation_failure: manual_review`}</pre>
        </CardBody>
      </Card>

      <ConfirmDialog
        open={target !== null}
        title={target?.state === 'published' ? '创建策略新版本' : '编辑策略草稿'}
        confirmLabel={target?.state === 'published' ? '创建新版本' : '进入编辑'}
        danger={target?.state === 'published'}
        target={
          target ? (
            <>
              {target.name} <span className="copy-id">{target.id}</span> 当前版本 <span className="mono">{target.version}</span>
            </>
          ) : null
        }
        impact={
          target?.state === 'published'
            ? '已发布策略正在被生产运行使用。创建新版本不会修改运行中版本；新版本需经校验、策略测试与发布门禁后才能生效。'
            : '草稿不影响生产运行。'
        }
        scope="租户 current · 环境由顶部栏标识"
        reversible
        requiresApproval={target?.state === 'published'}
        verification="新版本创建后，在校验通过的版本列表确认状态并执行发布门禁；发布前核对受影响的资源类型与动作。"
        confirmPhrase={target?.state === 'published' ? (target?.id ?? '') : undefined}
        busy={createVersion.isPending}
        error={createVersion.error}
        onCancel={() => setTarget(null)}
        onConfirm={() => {
          if (target) {
            createVersion.mutate(target.id, { onSuccess: () => setTarget(null) });
          }
        }}
      />
    </div>
  );
}
