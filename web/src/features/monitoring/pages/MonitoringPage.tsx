import { Button, Card, CardBody, CardHeader, PageHeader } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { useOpsAlerts } from '../api';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { BadgeTone } from '@/components/ui';
import { MetaBadge } from '@/components/status-badge';
import type { OpsAlert } from '@/types';

const SEVERITY_META: Record<OpsAlert['severity'], { label: string; tone: BadgeTone }> = {
  CRITICAL: { label: '严重', tone: 'error' },
  IMPORTANT: { label: '重要', tone: 'warning' },
  NORMAL: { label: '一般', tone: 'info' },
};

const STATE_META: Record<OpsAlert['state'], { label: string; tone: BadgeTone }> = {
  open: { label: '未处理', tone: 'error' },
  acknowledged: { label: '已确认', tone: 'warning' },
  resolved: { label: '已恢复', tone: 'success' },
};

/**
 * 运行监控 `/admin/monitoring`（§8.6 / 第十一篇）。
 * 展示待运行任务、Worker、队列等待、重试与失败率、租约过期、死信、工具并发与模型预算等。
 * MVP 阶段给出可渲染骨架 + 告警列表。
 */
export function MonitoringPage() {
  const query = useOpsAlerts();

  const columns: DataTableColumn<OpsAlert>[] = [
    {
      key: 'title',
      header: '告警',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.title}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 来源 {item.source}
          </div>
        </div>
      ),
    },
    { key: 'severity', header: '级别', tight: true, render: (item) => <MetaBadge map={SEVERITY_META} value={item.severity} context="告警级别" /> },
    { key: 'state', header: '状态', tight: true, render: (item) => <MetaBadge map={STATE_META} value={item.state} context="告警状态" /> },
    {
      key: 'created_at',
      header: '触发时间',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDateTime(item.created_at)}
          <br />
          <span className="text-xs muted">{formatRelative(item.created_at)}</span>
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      tight: true,
      render: (item) => (
        <Button variant="secondary" size="sm" disabled={item.state === 'resolved'} icon="userCheck">
          认领
        </Button>
      ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="activity"
        title="运行监控"
        subtitle="观察队列、Worker、租约、死信与模型预算的运行时告警。"
      />

      <Card className="section">
        <CardHeader
          title="队列与执行资源"
          description="运行时聚合端点尚未接入，以下指标以「—」占位，不填充假数据。"
        />
        <CardBody>
          <div className="grid grid--metrics">
            <div className="metric">
              <span className="metric__label">待运行任务</span>
              <span className="metric__value">—</span>
            </div>
            <div className="metric">
              <span className="metric__label">运行中 Worker</span>
              <span className="metric__value">—</span>
            </div>
            <div className="metric">
              <span className="metric__label">队列等待时间</span>
              <span className="metric__value">—</span>
            </div>
            <div className="metric metric--attention">
              <span className="metric__label">租约过期 / 死信</span>
              <span className="metric__value">—</span>
            </div>
          </div>
        </CardBody>
      </Card>

      <section className="section" aria-labelledby="alerts-title">
        <h2 className="section__title" id="alerts-title">
          告警
        </h2>
        <DataTable
          caption="告警列表"
          columns={columns}
          rows={query.data?.items ?? []}
          rowKey={(item) => item.id}
          loading={query.isLoading}
          error={query.error}
          onRetry={() => void query.refetch()}
          emptyTitle="暂无告警"
          nextCursor={query.data?.next_cursor ?? null}
        />
      </section>
    </div>
  );
}
