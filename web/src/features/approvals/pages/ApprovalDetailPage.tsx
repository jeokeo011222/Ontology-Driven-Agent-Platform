import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardHeader,
  Field,
  PageHeader,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Textarea,
} from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { EmptyState } from '@/components/empty-state';
import { ErrorState, SubmittingState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { PermissionGate } from '@/components/permission-gate';
import { ErpOperationStatusBadge, RiskBadge } from '@/components/status-badge';
import { formatDateTime, formatParameterValue, truncateMiddle } from '@/lib/format';
import { describeError } from '@/api/errors';
import type { ApprovalStatus, ErpOperationStatus } from '@/types';
import { useApproval, useApprovalDecision } from '../api';
import { DECISION_LABEL, DECISION_OPTIONS, decisionSchema, type DecisionFormValues } from '../schema';
import { Icon } from '@/components/icon';

const STATUS_TONE: Record<ApprovalStatus, 'neutral' | 'warning' | 'success' | 'error' | 'info'> = {
  PENDING: 'warning',
  APPROVED: 'success',
  REJECTED: 'error',
  RETURNED: 'info',
  EXPIRED: 'neutral',
  CANCELLED: 'neutral',
};

const STATUS_LABEL: Record<ApprovalStatus, string> = {
  PENDING: '待审批',
  APPROVED: '已通过',
  REJECTED: '已拒绝',
  RETURNED: '已退回',
  EXPIRED: '已过期',
  CANCELLED: '已取消',
};

/**
 * MVP 页面 3b：审批详情 `/approvals/:id`（§4.4 / §9.3 / §14.3）
 *
 * 必须完整展示：操作对象、影响、规则依据、参数摘要（§13.2 验收清单）。
 * 硬性要求：
 *   - 提交走 `POST /api/v1/approvals/{id}/decision`，带 Idempotency-Key 与 If-Match
 *   - 高风险操作二次确认，明确说明授权什么操作、作用于哪个对象、有效期多久
 *   - **审批通过后 UI 不得显示为“ERP 操作成功”**
 */
export function ApprovalDetailPage() {
  const { id } = useParams<{ id: string }>();
  const approvalQuery = useApproval(id);
  const decisionMutation = useApprovalDecision(id ?? '');

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [pending, setPending] = useState<DecisionFormValues | null>(null);

  const form = useForm<DecisionFormValues>({
    resolver: zodResolver(decisionSchema),
    defaultValues: { decision: 'APPROVE', comment: '', confirm_scope: '' },
  });

  const decision = form.watch('decision');

  if (approvalQuery.isLoading) return <LoadingState label="正在加载审批详情…" />;
  if (approvalQuery.error) return <ErrorState error={approvalQuery.error} onRetry={() => void approvalQuery.refetch()} />;

  const approval = approvalQuery.data;
  if (!approval) return <EmptyState title="审批不存在或不在可见范围" inline />;

  const needsSecondConfirm = approval.risk_level === 'high' || approval.risk_level === 'critical';
  const actionable = approval.status === 'PENDING';

  const submitDecision = (values: DecisionFormValues) => {
    decisionMutation.mutate({
      decision: values.decision,
      comment: values.comment,
      confirm_scope: values.confirm_scope,
      // §10.6.4 强制乐观并发：绑定审批摘要的不可变版本标识
      ifMatch: approval.parameter_version,
    });
  };

  const onSubmit = form.handleSubmit((values) => {
    if (needsSecondConfirm) {
      setPending(values);
      setConfirmOpen(true);
      return;
    }
    submitDecision(values);
  });

  const params = Object.entries(approval.parameters);

  return (
    <div className="page page--wide">
      <PageHeader
        breadcrumbs={
          <nav className="breadcrumbs" aria-label="面包屑">
            <Link to="/approvals">审批中心</Link>
            <span className="breadcrumbs__sep">/</span>
            <span>{approval.id}</span>
          </nav>
        }
        icon="clipboardCheck"
        title={
          <>
            {approval.title}
          </>
        }
        subtitle={
          <>
            <span className="copy-id">{approval.id}</span> · 关联任务{' '}
            <Link to={`/tasks/${approval.run_id}`}>
              <span className="copy-id">{approval.run_id}</span>
            </Link>
            {' · '}
            {formatDateTime(approval.created_at)}
          </>
        }
        actions={
          <>
            <Badge tone={STATUS_TONE[approval.status]}>{STATUS_LABEL[approval.status]}</Badge>
            <RiskBadge level={approval.risk_level} />
          </>
        }
      />

      {/* 审批状态与 ERP 执行状态必须分别呈现（§4.4） */}
      <div className="status-pair" style={{ marginBottom: 16 }}>
        <div className="status-pair__block">
          <div className="status-pair__label">审批状态</div>
          <Badge tone={STATUS_TONE[approval.status]}>{STATUS_LABEL[approval.status]}</Badge>
          <p className="status-pair__note">
            审批通过仅表示已获得执行授权，**不代表**外部操作已成功。
          </p>
        </div>
        <div className="status-pair__block">
          <div className="status-pair__label">ERP 执行状态</div>
          {approval.erp_operation_status ? (
            <ErpOperationStatusBadge status={approval.erp_operation_status as ErpOperationStatus} />
          ) : (
            <span className="muted text-sm">尚未产生外部操作</span>
          )}
          <p className="text-xs muted" style={{ marginTop: 8 }}>
            以外部操作状态作为业务结果的权威判据。
          </p>
        </div>
      </div>

      {decisionMutation.isSuccess ? (
        <div className="banner banner--warning" style={{ marginBottom: 16 }} role="status" aria-live="assertive">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="arrowLeftRight" size={16} />
          </span>
          <span>
            审批结论已提交：<strong>{decisionMutation.data?.status}</strong>。
            这只是<strong>审批结论</strong>，后续外部操作状态为
            <strong>{decisionMutation.data?.erp_operation_status ?? 'PENDING'}</strong>，
            并不代表 ERP 操作已成功。请前往任务详情查看任务与外部操作状态的并列视图。
          </span>
        </div>
      ) : null}

      {decisionMutation.isError ? (
        <div style={{ marginBottom: 16 }}>
          <ErrorState
            error={decisionMutation.error}
            onRetry={() => {
              if (pending) submitDecision(pending);
              else void form.handleSubmit(submitDecision)();
            }}
          />
        </div>
      ) : null}

      <div className="grid grid--two section">
        <Card>
          <CardHeader title="申请信息" />
          <CardBody>
            <dl className="def-list">
              <dt className="def-list__term">申请人</dt>
              <dd className="def-list__desc">{approval.requester.display_name}</dd>
              <dt className="def-list__term">所属组织</dt>
              <dd className="def-list__desc">{approval.requester.org_path ?? '—'}</dd>
              <dt className="def-list__term">业务对象</dt>
              <dd className="def-list__desc">
                {approval.resource_type} / <span className="copy-id">{approval.resource_id}</span>
              </dd>
              <dt className="def-list__term">审批链</dt>
              <dd className="def-list__desc">
                {approval.approval_chain.map((step) => (
                  <div key={step.step}>
                    第 {step.step} 级：{step.approver.display_name}（{step.state}）
                  </div>
                ))}
                <div className="text-xs muted" style={{ marginTop: 4 }}>
                  已通过 {approval.granted_approvals} / 需要 {approval.required_approvals}
                </div>
              </dd>
            </dl>
          </CardBody>
        </Card>

        <Card>
          <CardHeader title="本次操作与预期影响" />
          <CardBody>
            <dl className="def-list">
              <dt className="def-list__term">操作动作</dt>
              <dd className="def-list__desc mono">{approval.operation.action}</dd>
              <dt className="def-list__term">操作说明</dt>
              <dd className="def-list__desc">{approval.operation.description}</dd>
              <dt className="def-list__term">预期业务影响</dt>
              <dd className="def-list__desc">{approval.operation.expected_impact}</dd>
              <dt className="def-list__term">是否可逆</dt>
              <dd className="def-list__desc">{approval.operation.reversible ? '可逆' : '不可逆'}</dd>
              <dt className="def-list__term">授权范围</dt>
              <dd className="def-list__desc">
                {approval.authorization_scope.map((s) => (
                  <div key={s} className="copy-id" style={{ display: 'inline-block', marginRight: 6 }}>
                    {s}
                  </div>
                ))}
              </dd>
            </dl>
          </CardBody>
        </Card>
      </div>

      <div className="section">
        <Card>
          <CardHeader
            title="关键输入参数与审批摘要"
            description={`参数版本 ${approval.parameter_version}；审批摘要绑定参数、对象与版本，参数变化后必须重新审批。`}
          />
          <CardBody>
            <Table caption="审批参数">
              <TableHead>
                <TableRow>
                  <TableCell head>参数</TableCell>
                  <TableCell head>值</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {params.map(([key, value]) => (
                  <TableRow key={key}>
                    <TableCell className="mono">{key}</TableCell>
                    <TableCell>{formatParameterValue(value)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>

            <div className="banner banner--neutral" style={{ marginTop: 16 }}>
              <span className="banner__icon" aria-hidden="true">
                <Icon name="flag" size={16} />
              </span>
              <span>
                审批摘要（不可变版本标识）：算法 <span className="mono">{approval.payload_digest.algo}</span> · 摘要{' '}
                <span className="copy-id" title={approval.payload_digest.value}>
                  {truncateMiddle(approval.payload_digest.value, 12, 10)}
                </span>{' '}
                · 绑定时间 {formatDateTime(approval.payload_digest.bound_at)}
                <br />
                审批操作参数与最终执行参数必须一致（§9.3）；执行前服务端会重新读取权威业务状态，
                关键参数或对象版本变化时将要求重新审批或转人工复核。
              </span>
            </div>
          </CardBody>
        </Card>
      </div>

      <div className="grid grid--two section">
        <Card>
          <CardHeader title="Agent 建议与结构化理由" />
          <CardBody>
            {approval.agent_recommendation ? (
              <>
                <p>
                  <strong>建议：</strong>
                  {approval.agent_recommendation.decision}
                </p>
                <p className="text-sm" style={{ marginTop: 8 }}>
                  {approval.agent_recommendation.rationale}
                </p>
                <p className="text-xs muted" style={{ marginTop: 8 }}>
                  注意：模型建议属于“模型意见”，不等同于已验证事实或规则结论（§4.6）。
                </p>
              </>
            ) : (
              <EmptyState title="无 Agent 建议" inline />
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader title="适用规则、事实来源与异常项" />
          <CardBody>
            <Table caption="规则命中">
              <TableHead>
                <TableRow>
                  <TableCell head>规则</TableCell>
                  <TableCell head>版本</TableCell>
                  <TableCell head>结论</TableCell>
                  <TableCell head>证据</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {approval.rules.map((rule) => (
                  <TableRow key={`${rule.rule_id}-${rule.version}`}>
                    <TableCell className="mono">{rule.rule_id}</TableCell>
                    <TableCell className="mono">{rule.version}</TableCell>
                    <TableCell>
                      <Badge tone={rule.outcome === 'PASS' ? 'success' : rule.outcome === 'REQUIRE_APPROVAL' ? 'warning' : 'error'}>
                        {rule.outcome}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <span className="copy-id">{rule.evidence_ref ?? '—'}</span>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>

            <div style={{ marginTop: 12 }}>
              <p className="text-sm">
                <strong>异常项：</strong>
                {approval.exceptions.length === 0 ? '无' : null}
              </p>
              {approval.exceptions.map((ex) => (
                <p key={ex} className="text-sm" style={{ color: 'var(--color-warning)' }}>
                  · {ex}
                </p>
              ))}
            </div>
          </CardBody>
        </Card>
      </div>

      <div className="section">
        <Card>
          <CardHeader
            title="审批决策"
            description="决定将授权后续执行。请确认授权范围、作用对象与有效期后再提交。"
          />
          <CardBody>
            {!actionable ? (
              <div className="banner banner--neutral">
                <span className="banner__icon" aria-hidden="true">
                  <Icon name="circleCheck" size={16} />
                </span>
                <span>该审批已结束（当前状态：{STATUS_LABEL[approval.status]}），不再接受决策。</span>
              </div>
            ) : (
              <PermissionGate
                permission="approval.decide"
                fallback={
                  <div className="banner banner--warning">
                    <span className="banner__icon" aria-hidden="true">
                      <Icon name="ban" size={16} />
                    </span>
                    <span>当前身份无审批决策权限（approval:decide），可查看但不能提交决策。</span>
                  </div>
                }
              >
                <form onSubmit={onSubmit} noValidate>
                  <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
                    <legend className="field__label">审批结论</legend>
                    <div className="stack stack--tight" style={{ marginTop: 8 }}>
                      {DECISION_OPTIONS.map((opt) => (
                        <label className="checkbox-row" key={opt.value}>
                          <input type="radio" value={opt.value} {...form.register('decision')} />
                          <span>{opt.label}</span>
                        </label>
                      ))}
                    </div>
                  </fieldset>

                  <div style={{ marginTop: 16 }}>
                    <Field
                      label={decision === 'REJECT' || decision === 'RETURN' ? '理由（必填）' : '备注（可选）'}
                      htmlFor="comment"
                      error={form.formState.errors.comment?.message}
                    >
                      <Textarea id="comment" {...form.register('comment')} placeholder="说明判断依据或补充要求…" />
                    </Field>
                  </div>

                  <Field
                    label="本次授权范围与有效期"
                    htmlFor="confirm_scope"
                    required
                    hint="例如：授权对 claim-1001 执行 SUBMIT_FOR_APPROVAL，有效期至本次任务结束。"
                    error={form.formState.errors.confirm_scope?.message}
                  >
                    <Textarea id="confirm_scope" {...form.register('confirm_scope')} placeholder="明确写出作用对象与有效期…" />
                  </Field>

                  {decisionMutation.isPending ? <SubmittingState label="正在提交审批决策…" /> : null}

                  <div className="row" style={{ marginTop: 8 }}>
                    <Button type="submit" variant="primary" loading={decisionMutation.isPending} disabled={decisionMutation.isPending}>
                      {DECISION_LABEL[decision] ?? '提交决策'}
                    </Button>
                    <span className="text-xs muted">
                      提交将携带 Idempotency-Key 与 If-Match；并发修改会被服务端以 409 拒绝。
                    </span>
                  </div>
                </form>
              </PermissionGate>
            )}

            {decisionMutation.isError && describeError(decisionMutation.error).conflict ? (
              <div className="banner banner--warning" style={{ marginTop: 12 }} role="alert">
                <span className="banner__icon" aria-hidden="true">
                  <Icon name="alertTriangle" size={16} />
                </span>
                <span>
                  审批参数已被他人修改（409 冲突）。请重新加载最新审批内容，核对差异后再次提交。
                </span>
              </div>
            ) : null}
          </CardBody>
        </Card>
      </div>

      <ConfirmDialog
        open={confirmOpen}
        title="确认高风险审批决策"
        confirmLabel={`确认${pending ? DECISION_LABEL[pending.decision] : ''}`}
        danger={pending?.decision === 'APPROVE'}
        target={
          <>
            {approval.resource_type} / <span className="copy-id">{approval.resource_id}</span> — {approval.operation.description}
          </>
        }
        impact={
          <>
            {approval.operation.expected_impact}
            <br />
            授权后服务端将按审批摘要执行本次操作；参数或对象版本变化将要求重新审批。
          </>
        }
        scope={`租户 ${approval.requester.org_path ?? 'current'} · 环境由顶部栏标识`}
        reversible={approval.operation.reversible}
        requiresApproval={approval.required_approvals > 1}
        verification="提交后在任务详情核对「审批状态」与「ERP 执行状态」并列视图；外部操作状态未确认为 SUCCEEDED 前，不得视为业务成功。"
        confirmPhrase={approval.id}
        busy={decisionMutation.isPending}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={() => {
          if (pending) {
            submitDecision(pending);
            setConfirmOpen(false);
          }
        }}
      />
    </div>
  );
}
