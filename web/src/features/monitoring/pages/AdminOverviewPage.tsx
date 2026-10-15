import { Link } from 'react-router-dom';
import { Badge, Card, CardBody, CardHeader, MetricCard, PageHeader } from '@/components/ui';
import { Icon } from '@/components/icon';
import { ErrorState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { EmptyState } from '@/components/empty-state';
import { formatDateTime, formatNumber, formatPercent } from '@/lib/format';
import type { AdminOverview } from '@/types';
import { useAdminOverview } from '../api';

const HEALTH_TONE = { healthy: 'success', degraded: 'warning', down: 'error' } as const;
const HEALTH_LABEL = { healthy: '健康', degraded: '降级', down: '不可用' } as const;

/** 把某段的数值字段放宽为 `number | null`（null = 后端未返回，渲染「—」） */
type NullableNumeric<T> = { [K in keyof T]: T[K] extends number ? number | null : T[K] };

interface OverviewData {
  system_health: AdminOverview['system_health'];
  business: NullableNumeric<AdminOverview['business']>;
  semantic: NullableNumeric<AdminOverview['semantic']>;
  governance: NullableNumeric<AdminOverview['governance']>;
  counts: NullableNumeric<AdminOverview['counts']>;
  /** 平台级用户数（iam.users 无 RLS）；后端未返回时为 null */
  platform_users: number | null;
  generated_at?: string;
}

/**
 * 归一化：补齐可选段，但**绝不把缺失伪装成 0**。
 *
 * 保留兜底的理由：聚合端点字段可演进，缺一段不该让整页白屏（此前
 * `data.system_health.map` 直接抛 TypeError 导致 /admin 白屏）。
 * 但兜底必须"留痕"：`missing` 列出"后端未返回、当前为占位"的段，
 * 页面顶部据此给出明确提示 —— 否则用户会把占位值读成真实统计值。
 * 为此数值一律落为 `null`（`formatNumber`/`formatPercent` 会渲染成「—」），
 * 而不是补 0：0 是"真实的零"，与"未知"必须区分。
 */
function normalize(raw: AdminOverview | undefined): { data: OverviewData; missing: string[] } {
  const missing: string[] = [];
  if (!raw?.system_health) missing.push('系统健康');
  if (!raw?.business) missing.push('业务运行');
  if (!raw?.semantic) missing.push('语义健康');
  if (!raw?.governance) missing.push('治理与质量');
  if (!raw?.counts) missing.push('平台资源');
  if (raw?.platform_users == null) missing.push('平台用户数');

  const data: OverviewData = {
    system_health: raw?.system_health ?? [],
    business: {
      total_runs: raw?.business?.total_runs ?? null,
      success_rate: raw?.business?.success_rate ?? null,
      failures: raw?.business?.failures ?? null,
      pending_approvals: raw?.business?.pending_approvals ?? null,
      pending_reconciliations: raw?.business?.pending_reconciliations ?? null,
    },
    semantic: {
      ontology_validation_failures: raw?.semantic?.ontology_validation_failures ?? null,
      mapping_issues: raw?.semantic?.mapping_issues ?? null,
      data_freshness: raw?.semantic?.data_freshness ?? '无数据',
      semantic_regressions: raw?.semantic?.semantic_regressions ?? null,
    },
    governance: {
      recent_policy_changes: raw?.governance?.recent_policy_changes ?? null,
      failed_gates: raw?.governance?.failed_gates ?? null,
      open_security_alerts: raw?.governance?.open_security_alerts ?? null,
      pending_releases: raw?.governance?.pending_releases ?? null,
    },
    counts: {
      roles: raw?.counts?.roles ?? null,
      role_bindings: raw?.counts?.role_bindings ?? null,
      policy_versions: raw?.counts?.policy_versions ?? null,
      agents: raw?.counts?.agents ?? null,
      tools: raw?.counts?.tools ?? null,
      ontologies: raw?.counts?.ontologies ?? null,
      active_installations: raw?.counts?.active_installations ?? null,
      evaluation_runs: raw?.counts?.evaluation_runs ?? null,
      audit_events: raw?.counts?.audit_events ?? null,
    },
    platform_users: raw?.platform_users ?? null,
    generated_at: raw?.generated_at,
  };
  return { data, missing };
}

/**
 * MVP 支撑页面：管理概览 `/admin`（§5.3）
 *
 * 要求：按"系统健康 / 业务运行 / 语义健康 / 治理与质量"四层呈现；
 * 每项指标必须标明统计范围、时间窗口与数据来源；
 * 平台健康不能只用一个综合分数表示，更不能让平均成功率掩盖未授权写入等严重问题。
 */
export function AdminOverviewPage() {
  const query = useAdminOverview();

  if (query.isLoading) return <LoadingState label="正在加载管理概览…" />;
  if (query.error) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

  const { data, missing } = normalize(query.data);

  return (
    <div className="page page--wide">
      <PageHeader
        icon="gauge"
        title="平台管理概览"
        subtitle={
          <>
            统计范围：当前租户下全部可见资源 · 时间窗口：策略变更 30 天 / 安全告警 24 小时 · 数据来源：
            <span className="mono">/api/v1/admin/overview</span>（BFF 聚合，底层 runs / approvals / erp-operations / audit-events）
          </>
        }
      />

      {missing.length > 0 ? (
        <div className="banner banner--error" role="alert" style={{ marginBottom: 16 }}>
          <span className="banner__icon" aria-hidden="true">
            <Icon name="alertTriangle" size={16} />
          </span>
          <span>
            管理概览数据缺失：聚合端点未返回 <strong>{missing.join('、')}</strong>。相关指标以「—」占位
            （数值缺省不补 0），请勿把占位读成真实统计值。
          </span>
        </div>
      ) : null}

      <section className="section" aria-labelledby="health-title">
        <div className="section__heading">
          <h2 className="section__title" id="health-title">
            系统健康
          </h2>
          {data.generated_at ? <span className="section__meta">聚合时间 {formatDateTime(data.generated_at)}</span> : null}
        </div>
        {data.system_health.length === 0 ? (
          <EmptyState title="暂无健康数据" description="聚合端点未返回 system_health 字段。" inline />
        ) : (
          <div className="grid grid--three">
            {data.system_health.map((c) => (
              <Card key={c.component}>
                <CardBody>
                  <div className="row row--between">
                    <span style={{ fontWeight: 500 }}>{c.component}</span>
                    <Badge tone={HEALTH_TONE[c.state] ?? 'neutral'}>{HEALTH_LABEL[c.state] ?? c.state}</Badge>
                  </div>
                  {c.detail ? (
                    <p className="text-xs muted" style={{ marginTop: 8 }}>
                      {c.detail}
                    </p>
                  ) : null}
                </CardBody>
              </Card>
            ))}
          </div>
        )}
      </section>

      <div className="grid grid--two section">
        <Card>
          <CardHeader
            icon="activity"
            title="业务运行"
            description="统计范围：当前租户 · 成功率为 COMPLETED / 终态 Run（无终态 Run 时为 0）"
          />
          <CardBody>
            <dl className="def-list">
              <dt className="def-list__term">任务总量</dt>
              <dd className="def-list__desc">{formatNumber(data.business.total_runs)}</dd>
              <dt className="def-list__term">成功率</dt>
              <dd className="def-list__desc">
                {formatPercent(data.business.success_rate)}
                <span className="text-xs muted">（平均值可能掩盖未授权写入等严重问题，须结合下方治理指标判断）</span>
              </dd>
              <dt className="def-list__term">失败数</dt>
              <dd className="def-list__desc">{formatNumber(data.business.failures)}</dd>
              <dt className="def-list__term">待审批</dt>
              <dd className="def-list__desc">
                {formatNumber(data.business.pending_approvals)} · <Link to="/approvals">前往审批中心</Link>
              </dd>
              <dt className="def-list__term">待对账操作</dt>
              <dd className="def-list__desc">
                {formatNumber(data.business.pending_reconciliations)} ·{' '}
                <Link to="/tasks?view=attention">查看需关注任务</Link>
              </dd>
            </dl>
          </CardBody>
        </Card>

        <Card>
          <CardHeader
            icon="ontology"
            title="语义健康"
            description="数据来源：ontology.validation_runs、planning.validation_runs、audit.audit_events"
          />
          <CardBody>
            <dl className="def-list">
              <dt className="def-list__term">本体验证失败</dt>
              <dd className="def-list__desc">{formatNumber(data.semantic.ontology_validation_failures)}</dd>
              <dt className="def-list__term">映射问题</dt>
              <dd className="def-list__desc">
                {data.semantic.mapping_issues === null ? (
                  <>
                    未接入
                    <span className="text-xs muted">（数据映射尚未接入平台，无可统计口径）</span>
                  </>
                ) : (
                  formatNumber(data.semantic.mapping_issues)
                )}
              </dd>
              <dt className="def-list__term">数据新鲜度</dt>
              <dd className="def-list__desc">
                {data.semantic.data_freshness === '无数据' ? '无数据' : formatDateTime(data.semantic.data_freshness)}
                <span className="text-xs muted">（最近一条审计事件时间）</span>
              </dd>
              <dt className="def-list__term">语义回归</dt>
              <dd className="def-list__desc">{formatNumber(data.semantic.semantic_regressions)}</dd>
            </dl>
          </CardBody>
        </Card>
      </div>

      <section className="section" aria-labelledby="governance-title">
        <div className="section__heading">
          <h2 className="section__title" id="governance-title">
            治理与质量
          </h2>
          <span className="section__meta">策略变更窗口 30 天 · 安全告警窗口 24 小时</span>
        </div>
        <div className="grid grid--metrics">
          <MetricCard
            label="近期策略变更"
            value={data.governance.recent_policy_changes ?? '—'}
            hint="最近 30 天创建的策略版本"
            icon="scale"
            tone="brand"
          />
          <MetricCard
            label="失败门禁"
            value={data.governance.failed_gates ?? '—'}
            hint="质量门禁决策为 FAILED/REJECTED/BLOCKED"
            icon="clipboardCheck"
            tone="warning"
            className="metric--attention"
          />
          <MetricCard
            label="未处理安全告警"
            value={data.governance.open_security_alerts ?? '—'}
            hint="最近 24 小时 DENIED/REJECTED 审计事件"
            icon="shieldX"
            tone="error"
            className="metric--danger"
          />
          <MetricCard
            label="待发布版本"
            value={data.governance.pending_releases ?? '—'}
            hint="门禁决策为 PENDING"
            icon="package"
            tone="neutral"
          />
        </div>
      </section>

      <section className="section" aria-labelledby="counts-title">
        <div className="section__heading">
          <h2 className="section__title" id="counts-title">
            平台资源
          </h2>
          <span className="section__meta">按租户 RLS 过滤；用户数为平台级计数（iam.users 无 RLS）</span>
        </div>
        <div className="grid grid--three">
          <Card>
            <CardHeader title="治理与身份" description="角色 / 角色绑定 / 策略版本" />
            <CardBody>
              <dl className="def-list">
                <dt className="def-list__term">角色</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.roles)}</dd>
                <dt className="def-list__term">角色绑定</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.role_bindings)}</dd>
                <dt className="def-list__term">策略版本</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.policy_versions)}</dd>
                <dt className="def-list__term">平台用户</dt>
                <dd className="def-list__desc">{formatNumber(data.platform_users)}</dd>
              </dl>
            </CardBody>
          </Card>
          <Card>
            <CardHeader title="设计与执行" description="Agent / 工具 / 本体" />
            <CardBody>
              <dl className="def-list">
                <dt className="def-list__term">Agent</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.agents)}</dd>
                <dt className="def-list__term">工具</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.tools)}</dd>
                <dt className="def-list__term">本体</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.ontologies)}</dd>
              </dl>
            </CardBody>
          </Card>
          <Card>
            <CardHeader title="质量与留痕" description="插件安装 / 评估 / 审计事件" />
            <CardBody>
              <dl className="def-list">
                <dt className="def-list__term">活跃安装</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.active_installations)}</dd>
                <dt className="def-list__term">评估任务</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.evaluation_runs)}</dd>
                <dt className="def-list__term">审计事件</dt>
                <dd className="def-list__desc">{formatNumber(data.counts.audit_events)}</dd>
              </dl>
            </CardBody>
          </Card>
        </div>
      </section>

      <div className="banner banner--warning" style={{ marginBottom: 16 }} role="status">
        <span className="banner__icon" aria-hidden="true">
          <Icon name="alertTriangle" size={16} />
        </span>
        <span>
          高危安全事件（越权写入、审批绕过、跨租户访问、审计哈希链校验失败）<strong>不走站内/邮件兜底</strong>
          ，必须进入告警生命周期；通知中心只展示告警引用条目，不作为唯一告警载体（§12.4）。
        </span>
      </div>
    </div>
  );
}
