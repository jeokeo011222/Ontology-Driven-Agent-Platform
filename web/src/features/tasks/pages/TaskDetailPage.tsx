import { useMemo, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import {
  Button,
  Card,
  CardBody,
  CardHeader,
  PageHeader,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TabPanel,
  Tabs,
} from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { ErrorState, StateView } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { EmptyState } from '@/components/empty-state';
import { PermissionGate } from '@/components/permission-gate';
import { OntologyReviewPanel } from '@/components/ontology-review-panel';
import {
  ErpOperationStatusBadge,
  RunErpStatusPair,
  RunStatusBadge,
  StepStatusBadge,
  needsReconciliation,
} from '@/components/status-badge';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { RunStep } from '@/types';
import { useCancelRun, useReconcileRun, useRun, useRunErpOperations, useRunSteps } from '../api';
import { Icon } from '@/components/icon';

const TAB_ITEMS = [
  { id: 'overview', label: '概览' },
  { id: 'steps', label: '执行过程' },
  { id: 'external', label: '外部操作与对账' },
];

function stepDotClass(status: RunStep['status']): string {
  if (status === 'SUCCEEDED') return 'timeline__dot timeline__dot--success';
  if (status === 'FAILED') return 'timeline__dot timeline__dot--error';
  if (status === 'RUNNING' || status === 'RECONCILING') return 'timeline__dot timeline__dot--active';
  return 'timeline__dot';
}

/**
 * MVP 页面 2：任务执行详情 `/tasks/:runId`（§4.6 执行报告 / §14.3）
 *
 * 硬性要求：
 *   - 步骤列表 + 每步判断依据（本体版本、规则命中、事实来源）
 *   - 错误与恢复状态
 *   - Run 状态与 ERP Operation 状态**并列展示**（contracts/01 §5.1）
 *   - 结果待确认时提供对账入口，绝不显示为“失败”或“成功”
 *   - 不向普通用户暴露模型密钥、内部 Prompt 或其他租户数据（§4.1 功能要求 6）
 */
export function TaskDetailPage() {
  const { runId } = useParams<{ runId: string }>();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'overview';

  const runQuery = useRun(runId);
  const stepsQuery = useRunSteps(runId);
  const opsQuery = useRunErpOperations(runId);

  const cancelMutation = useCancelRun(runId ?? '');
  const reconcileMutation = useReconcileRun(runId ?? '');

  const [cancelOpen, setCancelOpen] = useState(false);

  const erpOperation = useMemo(() => opsQuery.data?.items?.[0], [opsQuery.data]);

  const setTab = (id: string) => {
    const next = new URLSearchParams(params);
    next.set('tab', id);
    setParams(next, { replace: true });
  };

  if (runQuery.isLoading) return <LoadingState label="正在加载任务详情…" />;
  if (runQuery.error) return <ErrorState error={runQuery.error} onRetry={() => void runQuery.refetch()} />;

  const run = runQuery.data;
  if (!run) return <EmptyState title="任务不存在或不在可见范围" inline />;

  const reconcileNeeded = needsReconciliation(run.status, erpOperation?.status);
  const canCancel = run.status !== 'COMPLETED' && run.status !== 'FAILED' && run.status !== 'CANCELLED' && run.status !== 'EXPIRED';

  return (
    <div className="page page--wide">
      <PageHeader
        breadcrumbs={
          <nav className="breadcrumbs" aria-label="面包屑">
            <Link to="/tasks">我的任务</Link>
            <span className="breadcrumbs__sep">/</span>
            <span>{run.id}</span>
          </nav>
        }
        icon="fileText"
        title={
          <>
            {run.objective}
          </>
        }
        subtitle={
          <>
            <span className="copy-id">{run.id}</span>
            {run.business_type ? <> · {run.business_type}</> : null}
            {run.ontology_version ? <> · 本体 {run.ontology_version}</> : null}
            {run.plan_template_version ? <> · 计划模板 {run.plan_template_version}</> : null}
            {' · 更新于 '}
            {formatDateTime(run.updated_at)}（{formatRelative(run.updated_at)}）
          </>
        }
        actions={
          <>
            <RunStatusBadge status={run.status} />
            <Button variant="secondary" size="sm" onClick={() => void runQuery.refetch()} icon="refresh">
              刷新
            </Button>
            {reconcileNeeded ? (
              <PermissionGate permission="run.reconcile">
                <Button
                  variant="primary"
                  size="sm"
                  loading={reconcileMutation.isPending}
                  onClick={() => reconcileMutation.mutate()}
                  icon="arrowLeftRight">
                  发起对账
                </Button>
              </PermissionGate>
            ) : null}
            {canCancel ? (
              <PermissionGate permission="run.cancel">
                <Button variant="danger" size="sm" onClick={() => setCancelOpen(true)} icon="ban">
                  请求取消
                </Button>
              </PermissionGate>
            ) : null}
          </>
        }
      />

      {cancelMutation.isSuccess ? (
        <div className="banner banner--warning" style={{ marginBottom: 16 }} role="status">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="ban" size={16} />
          </span>
          <span>
            取消请求已受理（202），Run 状态已置为{' '}
            <strong>{cancelMutation.data?.status ?? 'CANCELLING'}</strong>。
            <strong>受理不等于已取消</strong>：已派发的在途外部操作需等待收敛；若存在结果未知的操作，Run 可能转入对账。
          </span>
        </div>
      ) : null}

      {reconcileMutation.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={reconcileMutation.error} compact />
        </div>
      ) : null}

      {reconcileMutation.isSuccess ? (
        <div className="banner banner--warning" style={{ marginBottom: 16 }} role="status">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="arrowLeftRight" size={16} />
          </span>
          <span>
            对账已受理（202）。<strong>受理不等于已确认结果</strong>：请继续跟踪对账状态，直到外部操作状态转为
            “已确认成功”“已失败”或“需人工复核”。任务编号：
            <span className="copy-id">{reconcileMutation.data?.task_id ?? '—'}</span>
            {reconcileMutation.data?.status_url ? (
              <>
                （状态查询地址 <span className="mono">{reconcileMutation.data.status_url}</span>）
              </>
            ) : null}
          </span>
        </div>
      ) : null}

      <Tabs items={TAB_ITEMS} value={tab} onChange={setTab} ariaLabel="任务详情分区" />

      <TabPanel id="overview" active={tab === 'overview'}>
        <div className="stack">
          <Card>
            <CardHeader
              title="状态并列视图"
              description="任务（Run）状态与外部操作（ERP Operation）状态可合法地不同：Run 已完成不代表业务操作已成功。"
            />
            <CardBody>
              <RunErpStatusPair
                runStatus={run.status}
                erpStatus={erpOperation?.status ?? null}
                erpOperationId={erpOperation?.id}
                reconciliationId={erpOperation?.reconciliation_id}
                reconcileHref={`/tasks/${run.id}?tab=external`}
              />
              {opsQuery.isLoading ? <LoadingState inline label="正在加载外部操作状态…" /> : null}
              {opsQuery.error ? <ErrorState error={opsQuery.error} compact /> : null}
            </CardBody>
          </Card>

          {/* 本体审查结论：只在服务端确实返回了结论时出现。
              本体驱动任务的「业务结果」就是这份结论，因此它放在概览页最显眼处；
              详细到每一步的按本体处理过程在「执行过程」页签。 */}
          {run.review_conclusion ? (
            <OntologyReviewPanel conclusion={run.review_conclusion} plan={run.plan} />
          ) : null}

          {run.waiting_reason ? (
            <div className="banner banner--info" role="status">
              <span className="banner__icon" aria-hidden="true">
                <Icon name="userCheck" size={16} />
              </span>
              <span>当前等待原因：{run.waiting_reason}</span>
            </div>
          ) : null}

          {run.status === 'FAILED' ? (
            <StateView
              kind="failure"
              title="任务失败"
              description={
                <>
                  任务已终止且无法继续。
                  <br />
                  恢复指引：请查看「执行过程」中失败步骤的错误码与判断依据；修正前置条件（数据、权限或外部系统状态）后重新发起任务；
                  若失败涉及外部系统且可能已派发，请先核对外部操作状态，不要直接重发。
                </>
              }
            />
          ) : null}

          {run.status === 'MANUAL_REVIEW' ? (
            <StateView
              kind="unknown"
              title="需人工复核"
              description="自动流程无法安全判定，需要人工介入。请核对业务对象当前状态与外部回执后再决定后续动作。"
            />
          ) : null}
        </div>
      </TabPanel>

      <TabPanel id="steps" active={tab === 'steps'}>
        {stepsQuery.isLoading ? <LoadingState label="正在加载执行步骤…" /> : null}
        {stepsQuery.error ? <ErrorState error={stepsQuery.error} onRetry={() => void stepsQuery.refetch()} /> : null}
        {stepsQuery.data && stepsQuery.data.items.length === 0 ? (
          <EmptyState title="暂无执行步骤" description="任务刚创建时步骤可能尚未生成。" />
        ) : null}
        {stepsQuery.data && stepsQuery.data.items.length > 0 ? (
          <div className="timeline">
            {stepsQuery.data.items.map((step, index, all) => (
              <div className="timeline__item" key={step.id}>
                <div className="timeline__rail">
                  <span className={stepDotClass(step.status)} aria-hidden="true" />
                  {index < all.length - 1 ? <span className="timeline__line" /> : null}
                </div>
                <div className="timeline__content">
                  <div className="timeline__title">
                    <span className="mono">{step.seq}.</span>
                    <span>{step.name}</span>
                    <StepStatusBadge status={step.status} />
                    <span className="text-xs muted">{step.kind}</span>
                  </div>

                  {step.rationale ? (
                    <p className="text-sm" style={{ marginTop: 6 }}>
                      <strong>判断依据：</strong>
                      {step.rationale}
                    </p>
                  ) : null}

                  <div className="text-xs muted" style={{ marginTop: 6 }}>
                    {step.ontology_version ? <>本体 {step.ontology_version} · </> : null}
                    {step.tool_id ? <>工具 {step.tool_id} · </> : null}
                    {step.started_at ? <>开始 {formatDateTime(step.started_at)}</> : null}
                    {step.finished_at ? <> · 结束 {formatDateTime(step.finished_at)}</> : null}
                  </div>

                  {step.rule_refs?.length ? (
                    <div className="text-xs muted" style={{ marginTop: 4 }}>
                      规则命中：{step.rule_refs.map((r) => <span key={r} className="copy-id" style={{ marginRight: 4 }}>{r}</span>)}
                    </div>
                  ) : null}
                  {step.fact_refs?.length ? (
                    <div className="text-xs muted" style={{ marginTop: 4 }}>
                      事实来源：{step.fact_refs.map((f) => <span key={f} className="copy-id" style={{ marginRight: 4 }}>{f}</span>)}
                    </div>
                  ) : null}

                  {step.error_code ? (
                    <div className="banner banner--error" style={{ marginTop: 8 }} role="alert">
                      <span className="banner__icon" aria-hidden="true">
                        <Icon name="circleX" size={16} />
                      </span>
                      <span>
                        错误码 <span className="mono">{step.error_code}</span>
                        {step.error_message ? ` — ${step.error_message}` : ''}
                      </span>
                    </div>
                  ) : null}
                </div>
              </div>
            ))}
          </div>
        ) : null}
      </TabPanel>

      <TabPanel id="external" active={tab === 'external'}>
        <div className="stack">
          <Card>
            <CardHeader
              title="外部操作账本"
              description="外部操作状态是本任务业务结果的唯一权威判据；结果待确认时必须走对账，而不是重试或按失败处理。"
            />
            <CardBody flush>
              {opsQuery.isLoading ? (
                <div style={{ padding: 16 }}>
                  <LoadingState inline label="正在加载外部操作…" />
                </div>
              ) : null}
              {opsQuery.error ? (
                <div style={{ padding: 16 }}>
                  <ErrorState error={opsQuery.error} onRetry={() => void opsQuery.refetch()} compact />
                </div>
              ) : null}
              {opsQuery.data && opsQuery.data.items.length === 0 && !opsQuery.isLoading ? (
                <div style={{ padding: 16 }}>
                  <EmptyState title="本任务未涉及外部写操作" inline />
                </div>
              ) : null}
              {opsQuery.data && opsQuery.data.items.length > 0 ? (
                <Table caption="外部操作列表">
                  <TableHead>
                    <TableRow>
                      <TableCell head>操作 ID</TableCell>
                      <TableCell head>动作</TableCell>
                      <TableCell head>业务对象</TableCell>
                      <TableCell head>状态</TableCell>
                      <TableCell head>幂等键引用</TableCell>
                      <TableCell head>派发时间</TableCell>
                      <TableCell head>回执</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {opsQuery.data.items.map((op) => (
                      <TableRow key={op.id}>
                        <TableCell>
                          <span className="copy-id">{op.id}</span>
                        </TableCell>
                        <TableCell className="mono">{op.action}</TableCell>
                        <TableCell>
                          {op.resource_type} / <span className="copy-id">{op.resource_id}</span>
                        </TableCell>
                        <TableCell>
                          <ErpOperationStatusBadge status={op.status} />
                        </TableCell>
                        <TableCell>
                          <span className="copy-id">{op.idempotency_key_ref ?? '—'}</span>
                        </TableCell>
                        <TableCell className="text-sm">{formatDateTime(op.dispatched_at)}</TableCell>
                        <TableCell className="text-sm">
                          {op.receipt ? (
                            <>
                              回执 <span className="copy-id">{op.receipt.receipt_id}</span>
                              <br />
                              <span className="text-xs muted">{formatDateTime(op.receipt.received_at)}</span>
                            </>
                          ) : (
                            <span className="text-xs muted">尚无回执</span>
                          )}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              ) : null}
            </CardBody>
          </Card>

          {reconcileNeeded ? (
            <StateView
              kind="unknown"
              title="结果待确认"
              description={
                <>
                  平台未获得可信的外部操作结果。请发起对账确认最终状态；
                  <strong>禁止</strong>按失败处理，也<strong>不要</strong>重复提交同一笔非幂等操作。
                </>
              }
              actions={
                <PermissionGate permission="run.reconcile">
                  <Button
                    variant="primary"
                    size="sm"
                    loading={reconcileMutation.isPending}
                    onClick={() => reconcileMutation.mutate()}
                    icon="arrowLeftRight">
                    发起对账
                  </Button>
                </PermissionGate>
              }
            />
          ) : null}
        </div>
      </TabPanel>

      <ConfirmDialog
        open={cancelOpen}
        title="请求取消任务"
        confirmLabel="请求取消任务"
        danger
        target={
          <>
            <span className="copy-id">{run.id}</span> — {run.objective}
          </>
        }
        impact="取消将进入「取消中」状态：已派发的在途外部操作不会立即回滚，需等待收敛；若存在结果未知的外部操作，任务可能转入对账或人工复核。"
        scope={`租户 ${run.subject_id ?? 'current'} · 环境由顶部栏标识`}
        reversible={false}
        requiresApproval={false}
        verification="取消后在任务详情核对最终状态是否为「已取消」；若存在在途外部操作，请同时核对对外部操作的对账结果。"
        confirmPhrase={run.id}
        busy={cancelMutation.isPending}
        error={cancelMutation.error}
        onCancel={() => setCancelOpen(false)}
        onConfirm={() => {
          cancelMutation.mutate('用户在工作台请求取消', {
            onSuccess: () => setCancelOpen(false),
          });
        }}
      />
    </div>
  );
}
