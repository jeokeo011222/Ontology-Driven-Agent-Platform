import { Link, useNavigate } from 'react-router-dom';
import { Button, Card, CardBody, CardHeader, MetricCard, PageHeader } from '@/components/ui';
import { Icon } from '@/components/icon';
import { ErrorState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { EmptyState } from '@/components/empty-state';
import { RunStatusBadge } from '@/components/status-badge';
import { formatDateTime, formatRelative } from '@/lib/format';
import { usePermissions } from '@/hooks/usePermissions';
import { useRecentRuns, useWorkspaceSummary } from '../api';
import { toWorkspaceView } from '../view-model';

/**
 * MVP 页面 1：业务工作台 `/workspace`（§4.1 / §14.3）
 *
 * 目标：用户登录后第一眼知道——有什么要处理、哪些在运行、哪些需要自己确认、最近完成了什么。
 * 约束：
 *   - 指标只统计当前用户有权访问的任务（由服务端聚合保证）；
 *   - 指标口径来自契约结构，换算集中在 features/workspace/view-model.ts，
 *     不在 JSX 里做字段取值（避免契约字段演进直接把页面打崩）；
 *   - "最近任务"来自 `/api/v1/runs`（契约的 workspace summary 不含该列表）。
 */
export function WorkspacePage() {
  const summary = useWorkspaceSummary();
  const recent = useRecentRuns(5);
  const navigate = useNavigate();
  const { can } = usePermissions();

  if (summary.isLoading) return <LoadingState label="正在加载工作台…" />;
  if (summary.error) return <ErrorState error={summary.error} onRetry={() => void summary.refetch()} />;

  const data = summary.data;
  if (!data) return <EmptyState title="暂无工作台数据" inline />;

  const view = toWorkspaceView(data);

  return (
    <div className="page">
      <PageHeader
        icon="dashboard"
        title="我的工作台"
        subtitle="业务任务与执行进度。所有状态由服务端驱动，指标仅统计当前用户有权访问的任务。"
      />

      <section className="section" aria-labelledby="metrics-title">
        <div className="section__heading">
          <h2 className="section__title" id="metrics-title">
            概览
          </h2>
          <span className="section__meta">{view.scopeLabel}</span>
        </div>
        <div className="grid grid--metrics">
          {view.metrics.map((m) => (
            <MetricCard
              key={m.key}
              label={m.label}
              value={m.value}
              hint={m.hint}
              icon={m.icon}
              tone={m.tone}
              className={m.className}
              onClick={() => navigate(m.to)}
            />
          ))}
        </div>
      </section>

      <section className="section" aria-labelledby="quick-title">
        <h2 className="section__title" id="quick-title">
          快捷操作
        </h2>
        <div className="grid grid--two">
          <Card>
            <CardHeader
              icon="sparkles"
              title="发起业务任务"
              description="描述目标，由 ODAP 生成受控执行计划；需要审批的动作将进入审批流程。"
            />
            <CardBody>
              <div className="row">
                <Link className="btn btn--primary" to="/assistant">
                  <Icon name="arrowRight" size={16} />
                  发起业务任务
                </Link>
              </div>
            </CardBody>
          </Card>
          <Card>
            <CardHeader
              icon="award"
              title="进入审批中心"
              description="处理需要人工确认的业务动作。审批通过不等于外部操作已成功。"
            />
            <CardBody>
              <div className="row">
                <Link className="btn btn--secondary" to="/approvals">
                  <Icon name="arrowRight" size={16} />
                  进入审批中心
                </Link>
                {can('approval.decide') ? null : <span className="text-xs muted">（当前身份仅可查看）</span>}
              </div>
            </CardBody>
          </Card>
        </div>
      </section>

      <section className="section" aria-labelledby="recent-title">
        <Card>
          <CardHeader
            title="最近任务"
            description="按更新时间倒序展示最近 5 条；运行中任务支持刷新和继续查看，失败任务提供明确的下一步操作。"
            actions={
              <>
                <Button
                  variant="ghost"
                  size="sm"
                  icon="refresh"
                  loading={recent.isFetching}
                  onClick={() => {
                    void summary.refetch();
                    void recent.refetch();
                  }}
                >
                  刷新
                </Button>
                <Link className="btn btn--secondary btn--sm" to="/tasks">
                  查看全部
                </Link>
              </>
            }
          />
          <CardBody flush>
            {recent.isLoading ? (
              <div style={{ padding: 16 }}>
                <LoadingState label="正在加载最近任务…" inline />
              </div>
            ) : recent.error ? (
              <div style={{ padding: 16 }}>
                <ErrorState error={recent.error} compact onRetry={() => void recent.refetch()} />
              </div>
            ) : (recent.data?.items.length ?? 0) === 0 ? (
              <div style={{ padding: 16 }}>
                <EmptyState title="暂无任务" description="你可以从「发起业务任务」开始。" inline />
              </div>
            ) : (
              <ul>
                {recent.data?.items.map((run) => (
                  <li className="list-item" key={run.id}>
                    <div className="list-item__main">
                      <div className="list-item__title">
                        <Link to={`/tasks/${run.id}`}>{run.objective}</Link>
                      </div>
                      <div className="list-item__meta">
                        <span className="copy-id">{run.id}</span>
                        {run.resource_refs?.length ? <> · {run.resource_refs.join('、')}</> : null}
                        {' · '}
                        {formatDateTime(run.updated_at)}（{formatRelative(run.updated_at)}）
                      </div>
                      {run.waiting_reason ? (
                        <div className="list-item__meta">等待原因：{run.waiting_reason}</div>
                      ) : null}
                    </div>
                    <div className="list-item__aside">
                      <RunStatusBadge status={run.status} />
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </CardBody>
        </Card>
      </section>
    </div>
  );
}
