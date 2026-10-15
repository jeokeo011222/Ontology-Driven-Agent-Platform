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
  TabPanel,
  Tabs,
  Textarea,
} from '@/components/ui';
import { ConfirmDialog } from '@/components/confirm-dialog';
import { EmptyState } from '@/components/empty-state';
import { ErrorState, StateView, SubmittingState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { PermissionGate } from '@/components/permission-gate';
import { useSession } from '@/hooks/useSession';
import { ApiError } from '@/api/errors';
import type { OntologyDetail, OntologyPublishResult, OntologyValidationReport } from '@/types';
import { PUBLISH_ENVIRONMENT, useOntology, usePublishOntology, useValidateOntology } from '../api';
import { ontologyVersionBadge } from '../state';
import { publishSchema, type PublishFormValues } from '../schema';
import { Icon } from '@/components/icon';

const TAB_ITEMS = [
  { id: 'structure', label: '类与关系' },
  { id: 'findings', label: '验证发现项' },
  { id: 'release', label: '版本与发布' },
];

/**
 * 校验结论面板（**同步**返回，§6.3）。
 *
 * 刻意不画进度条、不轮询任务：`POST /ontologies/{id}/validate` 是一次同步请求，
 * 响应到达时结论已经确定。此前把它按 202 长任务渲染（`TaskProgressPanel`），
 * 页面会挂一个永远停在"正在获取任务状态…"的面板，而真正的结论——conforms、
 * version_status、发现项条数、以及"到底查了多少数据"——一条都没被展示。
 */
function ValidateReportPanel({ report }: { report: OntologyValidationReport }) {
  const badge = ontologyVersionBadge(report.version_status, '本体验证');
  const blocking = report.findings.filter((f) => f.blocking).length;

  /** 三值检查项：true=通过，false=未通过，null/undefined=**未执行**（既非通过也非失败）。 */
  const checkItem = (label: string, value: boolean | null | undefined) => {
    const executed = value === true || value === false;
    const icon = !executed ? 'circle' : value ? 'circleCheck' : 'circleX';
    const text = !executed ? '未执行' : value ? '通过' : '未通过';
    return (
      <li>
        <span className="inline-ico">
          <Icon name={icon} size={15} title={text} />
        </span>
        {label}：{text}
      </li>
    );
  };

  return (
    <Card>
      <CardHeader
        icon={report.conforms ? 'circleCheck' : 'circleX'}
        title="本次校验结论"
        description={
          <>
            校验器 <span className="mono">{report.validator_version ?? '—'}</span>
            {report.validation_run_id ? (
              <>
                {' · '}运行号 <span className="mono copy-id">{report.validation_run_id}</span>
              </>
            ) : null}
          </>
        }
        actions={
          <Badge tone={badge.tone} title={badge.title}>
            {badge.label}
          </Badge>
        }
      />
      <CardBody>
        <ul className="stack stack--tight text-sm">
          <li>
            版本状态：<Badge tone={badge.tone}>{badge.label}</Badge>
            {' · '}校验运行：<strong>{report.run_status === 'PASSED' ? '通过' : '未通过'}</strong>
            {report.version ? (
              <>
                {' '}（版本 <span className="mono">{report.version}</span>）
              </>
            ) : null}
          </li>
          <li>
            发现项：{report.findings.length} 条
            {blocking > 0 ? (
              <>
                ，其中<strong> 阻断发布 {blocking} 条</strong>
              </>
            ) : (
              '（无阻断项）'
            )}
          </li>
          {report.graph ? (
            <li>
              校验的数据面：命中目标类实例 <strong>{report.graph.focus_nodes}</strong> 个 · TBox{' '}
              {report.graph.tbox_triples} 条 · ABox {report.graph.abox_triples} 条
            </li>
          ) : null}
          {checkItem('语法校验', report.checks?.syntax_valid)}
          {checkItem('SHACL 形状校验', report.checks?.shacl_conformant)}
          {checkItem('OWL 一致性', report.checks?.owl_consistent)}
          {checkItem('业务规则', report.checks?.business_rules_valid)}
        </ul>
        {report.checks && (report.checks.owl_consistent === null || report.checks.business_rules_valid === null) ? (
          <p className="text-xs muted" style={{ marginTop: 8 }}>
            标记为「未执行」的检查项表示该类校验尚未接入平台——它既不是通过，也不是失败；不要把空结论读成合规。
          </p>
        ) : null}
        {!report.conforms ? (
          <p className="text-xs" style={{ marginTop: 8 }}>
            结论未通过：版本已置为 <span className="mono">REJECTED</span>，发布门禁不会放行。请在「验证发现项」页签查看每条违规的焦点节点与属性路径。
          </p>
        ) : null}
      </CardBody>
    </Card>
  );
}

/** 发布结果面板（同步返回）。显示的是服务端回带的真实 environment 与 version_id。 */
function PublishResultPanel({ result }: { result: OntologyPublishResult }) {
  return (
    <div className="banner banner--success" role="status" aria-live="polite">
      <span className="banner__icon" aria-hidden="true">
        <Icon name="rocket" size={16} />
      </span>
      <span>
        已发布到 <strong>{result.environment}</strong>：状态 <strong>{result.status}</strong>
        {' · '}版本主键 <span className="mono copy-id">{result.version_id}</span>
        {' '}（该接口同步返回，发布结论已在本次响应中确定，无需再跟踪任务）。
      </span>
    </div>
  );
}

function FindingsTable({ findings }: { findings: OntologyDetail['findings'] }) {
  if (findings.length === 0) {
    return <EmptyState title="没有验证发现项" description="最近一次验证未发现 SHACL 违规。" inline />;
  }
  return (
    <Table caption="SHACL 验证发现项">
      <TableHead>
        <TableRow>
          <TableCell head>级别</TableCell>
          <TableCell head>说明</TableCell>
          <TableCell head>焦点节点</TableCell>
          <TableCell head>路径</TableCell>
          <TableCell head>是否阻断发布</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {findings.map((f) => (
          <TableRow key={f.id}>
            <TableCell>
              <Badge tone={f.severity === 'violation' ? 'error' : f.severity === 'warning' ? 'warning' : 'info'}>
                {f.level}
              </Badge>
            </TableCell>
            <TableCell>{f.message}</TableCell>
            <TableCell>
              <span className="copy-id">{f.focus_node ?? '—'}</span>
            </TableCell>
            <TableCell className="mono">{f.path ?? '—'}</TableCell>
            <TableCell>{f.blocking ? <Badge tone="error">阻断</Badge> : <Badge tone="neutral">不阻断</Badge>}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/**
 * 版本生命周期状态 →「为什么现在不能发布」。
 *
 * 后端发布门禁只放行 VALID / APPROVED（否则返回 409「没有可发布的本体版本」）。
 * 前端必须用同一份口径判断，否则会出现"按钮可点、点下去才被拒"的体验：
 * 用户看到的是 409 横幅，而不是"请先执行验证"的前置说明。
 */
const PUBLISHABLE_VERSION_STATUSES = new Set(['VALID', 'APPROVED']);

const PUBLISH_BLOCK_HINT: Record<string, string> = {
  DRAFT: '当前版本尚未执行校验（状态：草稿）。请先点「执行验证」，结论通过后再发布。',
  VALIDATING: '当前版本正在校验中，请等待校验结束后再发布。',
  REJECTED: '当前版本校验未通过（状态：已拒绝），请先修复发现项并重新执行验证。',
  PUBLISHED: '当前版本已发布，无需重复发布；如需变更请基于新内容重新生成。',
  DEPRECATED: '当前版本已废弃，不可发布。',
  DISABLED: '当前版本已停用，不可发布。',
};

/**
 * MVP 页面 4b：本体详情 `/admin/ontologies/:id`（§6.2 / §6.3 / §14.3）
 *
 * 硬性要求（§13.2 验收清单）：
 *   本体发布前能够查看结构差异、验证报告和回归结果。
 */
export function OntologyDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [params] = useState(() => new URLSearchParams(window.location.search));
  const version = params.get('version') ?? undefined;
  const [tab, setTab] = useState('structure');

  const query = useOntology(id, version);
  const validateMutation = useValidateOntology(id ?? '');
  const publishMutation = usePublishOntology(id ?? '');
  const session = useSession();

  const [validateReport, setValidateReport] = useState<OntologyValidationReport | null>(null);
  const [publishResult, setPublishResult] = useState<OntologyPublishResult | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [pendingNotes, setPendingNotes] = useState<string | null>(null);

  /** 发布目标环境：由顶部栏环境决定。省略它会静默落到后端的 DEVELOPMENT 默认值。 */
  const targetEnvironment = PUBLISH_ENVIRONMENT[session.env];

  const form = useForm<PublishFormValues>({
    resolver: zodResolver(publishSchema),
    defaultValues: { release_notes: '', no_semantic_drift_confirmed: false as unknown as true },
  });

  if (query.isLoading) return <LoadingState label="正在加载本体详情…" />;

  // 404 的处理保留：即便端点已注册，`enabled: Boolean(id)` 为假或资源不可见时
  // 仍会走到这里。**不得**把 404 一律说成"契约缺项"——那会把"没有这个本体"
  // 与"端点没实现"混为一谈。此处只说"资源不存在或不可见"。
  if (query.error instanceof ApiError && query.error.isNotFound) {
    return (
      <div className="page">
        <PageHeader
          breadcrumbs={
            <nav className="breadcrumbs" aria-label="面包屑">
              <Link to="/admin/ontologies">企业本体</Link>
              <span className="breadcrumbs__sep">/</span>
              <span>详情</span>
            </nav>
          }
          icon="ontology"
          title="本体详情"
          subtitle="该本体不存在，或不在当前身份可见范围内。"
        />
        <StateView
          kind="stale"
          title="本体不存在或不可见"
          description={
            <>
              请求 <span className="mono">GET /api/v1/ontologies/{'{id}'}</span> 返回 404。该端点
              已在契约与路由中登记，因此这里只能读作<strong>「本体不存在或不在可见范围」</strong>
              ——出于安全，服务端不区分这两者（与「无权限」同样不单独暴露）。
              <br />
              注：历史缺陷 #20 曾把此处 404 解释为「详情端点未实现」，该说法已随端点落地而作废；
              继续照抄会掩盖真实的资源标识错误。
            </>
          }
          actions={
            <Link className="btn btn--secondary btn--sm" to="/admin/ontologies">
              返回本体列表
            </Link>
          }
        />
      </div>
    );
  }

  if (query.error) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

  const ontology = query.data;
  if (!ontology) return <EmptyState title="本体不存在或不在可见范围" inline />;

  const blockingFindings = ontology.findings.filter((f) => f.blocking);
  // 发布前置条件必须与后端一致：既要有"无阻断发现项"，版本本身也要处于可发布的生命周期状态。
  // 只看发现项的话，一个从未校验的草稿（发现项为空）也能一路点到「发布新版本」才吃 409。
  const versionStatus = ontology.latest_version_status;
  const publishBlockReason =
    blockingFindings.length > 0
      ? `存在 ${blockingFindings.length} 条阻断发布的验证发现项，发布被禁止。请先修复后在「验证发现项」中重新验证。`
      : versionStatus && PUBLISHABLE_VERSION_STATUSES.has(versionStatus)
        ? null
        : versionStatus
          ? (PUBLISH_BLOCK_HINT[versionStatus] ?? '当前版本状态不可发布。')
          : '当前没有可发布的版本。';
  const canPublish = publishBlockReason === null;
  const versionBadge = ontologyVersionBadge(ontology.latest_version_status, '本体详情');

  const onSubmit = form.handleSubmit((values) => {
    setPendingNotes(values.release_notes);
    setConfirmOpen(true);
  });

  const doPublish = () => {
    // 冗余但必要：确认弹窗的 onConfirm 直连这里，若前置条件不满足（如版本不可发布），
    // 不该把请求发出去再让服务端 409 —— 拒绝理由应当在前置说明里给到，而不是靠报错。
    if (!canPublish || !pendingNotes) return;
    publishMutation.mutate(
      // 必须显式带上 environment：请求体里没有它时，后端把目标环境默认成
      // DEVELOPMENT —— 用户在生产环境点"发布"，实际发到开发环境且不会报错。
      { environment: targetEnvironment, release_notes: pendingNotes },
      {
        onSuccess: (result) => {
          setPublishResult(result);
          setConfirmOpen(false);
        },
      },
    );
  };

  return (
    <div className="page page--wide">
      <PageHeader
        breadcrumbs={
          <nav className="breadcrumbs" aria-label="面包屑">
            <Link to="/admin/ontologies">企业本体</Link>
            <span className="breadcrumbs__sep">/</span>
            <span>{ontology.name}</span>
          </nav>
        }
        icon="ontology"
        title={
          <>
            {ontology.name}
          </>
        }
        subtitle={
          <>
            <span className="mono">{ontology.ontology_key}</span>
            {ontology.latest_version ? (
              <>
                {' · '}最新版本 <span className="mono">{ontology.latest_version}</span>
              </>
            ) : null}
            {' · '}业务域 {ontology.domain} · 粒度 {ontology.granularity}
            {ontology.description ? <> · {ontology.description}</> : null}
          </>
        }
        actions={
          <>
            <Badge tone={versionBadge.tone} title={versionBadge.title}>
              {versionBadge.label}
            </Badge>
            <PermissionGate permission="admin.ontology.validate">
              <Button
                variant="secondary"
                size="sm"
                loading={validateMutation.isPending}
                onClick={() => {
                  // 先清空上一次结论，再发起请求：否则"新请求在飞"的这段时间里，
                  // 页面仍会显示上一次的 conforms 值，用户会误以为结论已更新。
                  setValidateReport(null);
                  validateMutation.mutate(undefined, {
                    // 同步返回：响应到达即结论确定。失败时保持空态，
                    // 由上方错误提示说明"本次没有取得结论"。
                    onSuccess: (report) => setValidateReport(report),
                  });
                }}
                icon="shieldCheck">
                执行验证
              </Button>
            </PermissionGate>
            <PermissionGate permission="admin.ontology.publish">
              <Button
                variant="primary"
                size="sm"
                disabled={!canPublish}
                title={publishBlockReason ?? undefined}
                onClick={() => {
                  setTab('release');
                  document.getElementById('release-notes')?.focus();
                }}
                icon="rocket">
                准备发布
              </Button>
            </PermissionGate>
          </>
        }
      />

      {validateMutation.isError ? (
        // 422 不是"接口出错"，而是校验器给出的**实质结论**（未定义 shapes / 图中无实例），
        // 因此照原样把服务端说明展示出来，不折叠、不替换成通用失败文案。
        <div style={{ marginBottom: 16 }}>
          <ErrorState error={validateMutation.error} compact />
        </div>
      ) : null}
      {validateReport ? (
        <div style={{ marginBottom: 16 }}>
          <ValidateReportPanel report={validateReport} />
        </div>
      ) : null}
      {publishResult ? (
        <div style={{ marginBottom: 16 }}>
          <PublishResultPanel result={publishResult} />
        </div>
      ) : null}
      {publishMutation.isError && publishMutation.error instanceof ApiError && publishMutation.error.isConflict ? (
        <div className="banner banner--warning" style={{ marginBottom: 16 }} role="alert">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="ban" size={16} />
          </span>
          <span>
            发布被服务端拒绝：目标版本当前不处于 <span className="mono">VALID</span> /{' '}
            <span className="mono">APPROVED</span>（可能是未校验、校验未通过、已停用，或已发布到其它环境）。
            {' '}请先执行「执行验证」，确认结论通过后再发布。
            {publishMutation.error.message ? <> 服务端说明：{publishMutation.error.message}</> : null}
          </span>
        </div>
      ) : null}

      {publishBlockReason ? (
        <div className="banner banner--error" style={{ marginBottom: 16 }} role="alert">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="circleX" size={16} />
          </span>
          <span>{publishBlockReason}</span>
        </div>
      ) : null}

      <Tabs
        items={TAB_ITEMS.map((item) =>
          item.id === 'findings' ? { ...item, badge: <Badge tone={blockingFindings.length ? 'error' : 'neutral'}>{ontology.findings.length}</Badge> } : item,
        )}
        value={tab}
        onChange={setTab}
        ariaLabel="本体工作台分区"
      />

      <TabPanel id="structure" active={tab === 'structure'}>
        {ontology.classes.length === 0 ? (
          <EmptyState title="尚未定义类与关系" inline />
        ) : (
          <div className="stack">
            {ontology.classes.map((cls) => (
              <Card key={cls.iri}>
                <CardHeader title={cls.label} description={<span className="mono">{cls.iri}</span>} />
                <CardBody flush>
                  {cls.description ? (
                    <p className="text-sm" style={{ padding: '0 20px 12px' }}>
                      {cls.description}
                    </p>
                  ) : null}
                  {cls.relations.length === 0 ? (
                    <div style={{ padding: 20 }}>
                      <EmptyState title="该类暂无关系定义" inline />
                    </div>
                  ) : (
                    <Table caption={`${cls.label} 关系`}>
                      <TableHead>
                        <TableRow>
                          <TableCell head>谓词</TableCell>
                          <TableCell head>目标类型</TableCell>
                          <TableCell head>说明</TableCell>
                        </TableRow>
                      </TableHead>
                      <TableBody>
                        {cls.relations.map((rel) => (
                          <TableRow key={`${cls.iri}-${rel.predicate}`}>
                            <TableCell className="mono">{rel.predicate}</TableCell>
                            <TableCell className="mono">{rel.target}</TableCell>
                            <TableCell>{rel.label}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  )}
                </CardBody>
              </Card>
            ))}
          </div>
        )}
      </TabPanel>

      <TabPanel id="findings" active={tab === 'findings'}>
        <Card>
          <CardHeader
            title="SHACL 验证发现项"
            description="某个测试对象缺少必需关系时，是否阻断发布取决于已配置的约束级别。"
          />
          <CardBody flush>
            <FindingsTable findings={ontology.findings} />
          </CardBody>
        </Card>
      </TabPanel>

      <TabPanel id="release" active={tab === 'release'}>
        <div className="stack">
          <Card>
            <CardHeader title="发布前检查" description="发布前必须执行结构校验、SHACL 验证、语义回归和依赖兼容性检查（§6.3）。" />
            <CardBody>
              <ul className="stack stack--tight text-sm">
                <li>
                  <span className="inline-ico">
                    <Icon
                      name={blockingFindings.length === 0 ? 'circleCheck' : 'circleX'}
                      size={15}
                      title={blockingFindings.length === 0 ? '通过' : '未通过'}
                    />
                  </span>
                  结构校验与 SHACL 验证：
                  {blockingFindings.length === 0 ? '无阻断项' : `${blockingFindings.length} 条阻断项`}
                </li>
                <li>
                  <span className="inline-ico">
                    <Icon
                      name={validateReport ? 'circleCheck' : 'circle'}
                      size={15}
                      title={validateReport ? '已执行' : '尚未执行'}
                    />
                  </span>
                  本次会话的校验结论：
                  {validateReport
                    ? `${validateReport.conforms ? '通过' : '未通过'}（版本已置为 ${validateReport.version_status}）`
                    : '尚未在当前会话执行'}
                </li>
                <li>
                  <span className="inline-ico">
                    <Icon name="ban" size={15} title="未接入" />
                  </span>
                  语义回归与 OWL 一致性检查：<strong>平台尚未接入</strong>——校验响应里这两项恒为{' '}
                  <span className="mono">null</span>（未执行），既不是通过也不是失败。发布门禁当前只依据
                  SHACL 结论。
                </li>
                <li className="muted">依赖兼容性检查：由服务端在执行发布门禁时校验（前端不代替质量门禁）。</li>
              </ul>
              <p className="text-xs muted" style={{ marginTop: 12 }}>
                发布报告将显示与上一个稳定版本的差异；工作流与 Agent 运行时锁定所使用的本体版本。
              </p>
            </CardBody>
          </Card>

          <Card>
            <CardHeader title="发布新版本" description="已发布版本不可原地修改；本次发布将创建不可变版本。" />
            <CardBody>
              <PermissionGate
                permission="admin.ontology.publish"
                fallback={
                  <div className="banner banner--warning">
                    <span className="banner__icon" aria-hidden="true">
                      <Icon name="ban" size={16} />
                    </span>
                    <span>当前身份无本体发布权限（admin.ontology.publish）。</span>
                  </div>
                }
              >
                <form onSubmit={onSubmit} noValidate>
                  <Field
                    label="发布说明"
                    htmlFor="release-notes"
                    required
                    hint="说明本次发布的结构差异、影响范围与回滚方式。"
                    error={form.formState.errors.release_notes?.message}
                  >
                    <Textarea id="release-notes" {...form.register('release_notes')} placeholder="例如：新增付款指令实体与授权约束；调整费用类别枚举…" />
                  </Field>

                  <label className="checkbox-row">
                    <input type="checkbox" {...form.register('no_semantic_drift_confirmed')} />
                    <span>我确认本次发布不会静默改变正在执行任务的语义上下文，且已评估依赖该版本的任务是否需要重跑。</span>
                  </label>
                  {form.formState.errors.no_semantic_drift_confirmed ? (
                    <p className="field__error" role="alert">
                      {form.formState.errors.no_semantic_drift_confirmed.message}
                    </p>
                  ) : null}

                  {publishMutation.isPending ? <SubmittingState label="正在提交发布…" /> : null}

                  <div className="row" style={{ marginTop: 12 }}>
                    <Button type="submit" variant="primary" disabled={!canPublish} loading={publishMutation.isPending} icon="rocket">
                      发布新版本
                    </Button>
                    {publishBlockReason ? (
                      <span className="text-xs" style={{ color: 'var(--color-error)' }}>
                        {publishBlockReason}
                      </span>
                    ) : null}
                  </div>
                </form>
              </PermissionGate>

              {publishMutation.isError && !(publishMutation.error instanceof ApiError && publishMutation.error.isConflict) ? (
                // 409 的"版本未通过校验"已在页面顶部用专门文案解释，此处不再重复渲染
                // （重复会让同一次拒绝出现两条不同措辞的提示）。
                <div style={{ marginTop: 12 }}>
                  <ErrorState error={publishMutation.error} compact />
                </div>
              ) : null}
            </CardBody>
          </Card>

          <StateView
            kind="stale"
            title="版本回滚"
            description="回滚必须恢复到明确的已发布版本，并评估依赖该版本的任务是否需要重跑。回滚入口在版本列表页，属高风险操作，需二次确认与审计留痕。"
          />
        </div>
      </TabPanel>

      <ConfirmDialog
        open={confirmOpen}
        title="确认发布本体新版本"
        confirmLabel="发布本体新版本"
        danger
        target={
          <>
            {ontology.name} <span className="mono">{ontology.latest_version ?? '新版本'}</span>（本体{' '}
            <span className="mono">{ontology.ontology_key}</span>）
          </>
        }
        impact="发布将创建不可变版本并可能被新的 Run 引用；运行中的任务仍锁定其原有版本，不会静默改变语义上下文。"
        scope={<>当前租户 · 目标环境 <strong>{targetEnvironment}</strong></>}
        reversible
        requiresApproval={false}
        verification="发布为同步操作：本次响应即最终状态（status / version_id）。发布后回到详情页核对版本状态；若新版本需回滚，恢复到明确的已发布版本并评估受影响任务。"
        confirmPhrase={ontology.id}
        busy={publishMutation.isPending}
        error={publishMutation.error instanceof ApiError && publishMutation.error.isConflict ? null : publishMutation.error}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={doPublish}
      />
    </div>
  );
}
