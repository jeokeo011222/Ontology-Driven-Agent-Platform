import { Card, CardBody, CardHeader, PageHeader } from '@/components/ui';
import { DataTable, type DataTableColumn } from '@/components/data-table';
import { formatDateTime, formatRelative } from '@/lib/format';
import type { BadgeTone } from '@/components/ui';
import { MetaBadge } from '@/components/status-badge';
import type { DataSource } from '@/types';
import { useDataSources, DATA_SOURCES_ENDPOINT_AVAILABLE } from '../api';
import { Icon } from '@/components/icon';

const FRESHNESS_META: Record<DataSource['freshness'], { label: string; tone: BadgeTone }> = {
  fresh: { label: '数据新鲜', tone: 'success' },
  stale: { label: '数据过期', tone: 'warning' },
  unknown: { label: '新鲜度未知', tone: 'neutral' },
};

/**
 * 数据接入与映射 `/admin/data-sources`（§6.4）
 *
 * 要求：映射页面应区分「源字段不存在」「源字段为空」「转换失败」和「多个来源互相冲突」，
 * 不能统一显示成一个模糊的“数据异常”。
 */
export function DataMappingPage() {
  const query = useDataSources();

  const columns: DataTableColumn<DataSource>[] = [
    {
      key: 'name',
      header: '数据源',
      render: (item) => (
        <div>
          <div style={{ fontWeight: 500 }}>{item.name}</div>
          <div className="text-xs muted" style={{ marginTop: 4 }}>
            <span className="copy-id">{item.id}</span> · 连接器 {item.connector_id}
          </div>
        </div>
      ),
    },
    {
      key: 'mapping',
      header: '映射版本',
      tight: true,
      render: (item) => <span className="mono text-sm">{item.mapping_version ?? '未配置'}</span>,
    },
    {
      key: 'freshness',
      header: '数据新鲜度',
      tight: true,
      render: (item) => <MetaBadge map={FRESHNESS_META} value={item.freshness} context="数据源新鲜度" />,
    },
    {
      key: 'sync',
      header: '最近同步',
      tight: true,
      render: (item) => (
        <span className="text-sm">
          {formatDateTime(item.last_sync_at)}
          <br />
          <span className="text-xs muted">{formatRelative(item.last_sync_at)}</span>
        </span>
      ),
    },
    {
      key: 'issues',
      header: '问题分类',
      render: (item) =>
        !item.issues || item.issues.length === 0 ? (
          <span className="text-xs muted">无</span>
        ) : (
          <ul className="stack stack--tight">
            {item.issues.map((issue) => (
              <li key={issue} className="text-xs" style={{ color: 'var(--color-warning)' }}>
                · {issue}
              </li>
            ))}
          </ul>
        ),
    },
  ];

  return (
    <div className="page page--wide">
      <PageHeader
        icon="database"
        title="数据接入与映射"
        subtitle="把 ERP 字段和外部数据转换为本体属性与关系：源字段映射、类型转换、主键与对象身份映射、空值与冲突处理、小样本预览、版本比较与数据血缘。"
      />

      <div className="banner banner--neutral" style={{ marginBottom: 16 }}>
        <span className="banner__icon" aria-hidden="true">
          <Icon name="flag" size={16} />
        </span>
        <span>
          问题必须分类呈现：<strong>源字段不存在</strong>、<strong>源字段为空</strong>、
          <strong>转换失败</strong>、<strong>多来源冲突</strong> 是四类不同问题，不得合并为“数据异常”。
        </span>
      </div>

      <DataTable
        caption="数据源列表"
        columns={columns}
        rows={DATA_SOURCES_ENDPOINT_AVAILABLE ? (query.data?.items ?? []) : []}
        rowKey={(item) => item.id}
        loading={DATA_SOURCES_ENDPOINT_AVAILABLE && query.isLoading}
        error={DATA_SOURCES_ENDPOINT_AVAILABLE ? query.error : undefined}
        onRetry={() => void query.refetch()}
        emptyTitle={DATA_SOURCES_ENDPOINT_AVAILABLE ? '暂无数据源' : '端点尚未登记'}
        emptyDescription={
          DATA_SOURCES_ENDPOINT_AVAILABLE
            ? '请先在「ERP 与外部集成」中配置连接器。'
            : 'contracts/openapi.yaml 中还没有 /api/v1/data-sources，前端不自行创建未登记的业务端点，也不把 404 伪装成"资源不存在"。'
        }
        nextCursor={DATA_SOURCES_ENDPOINT_AVAILABLE ? (query.data?.next_cursor ?? null) : null}
      />

      <Card className="section" style={{ marginTop: 16 }}>
        <CardHeader title="字段映射编辑器" description="需后端映射端点登记进 openapi.yaml 后启用" />
        <CardBody>
          <p className="text-sm muted">
            映射编辑器（源字段 → 本体属性、类型转换与规范化规则、主键与对象身份映射、小样本预览、映射版本比较、测试运行与错误报告）
            需要后端映射端点登记到 <span className="mono">contracts/openapi.yaml</span> 后方可实现；
            前端不自行创建未登记的业务端点。当前页面提供数据源清单与同步/映射状态的可视化骨架。
          </p>
          <div className="row" style={{ marginTop: 12 }}>
            <button type="button" className="btn btn--secondary btn--sm" disabled>
              打开映射编辑器（待端点登记）
            </button>
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
