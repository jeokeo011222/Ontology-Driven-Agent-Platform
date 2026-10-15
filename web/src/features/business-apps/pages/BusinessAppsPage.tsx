import { useNavigate } from 'react-router-dom';
import { Badge, Button, Card, CardBody, CardHeader, PageHeader } from '@/components/ui';
import { Icon, type IconName } from '@/components/icon';
import { EmptyState } from '@/components/empty-state';
import { ErrorState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import type { BusinessApp } from '@/types';
import { useBusinessApps } from '../api';

/** 业务域 → 图标。服务端只给 domain 字符串，图标由前端图标集统一提供（同线宽同尺寸）。 */
const DOMAIN_ICON: Record<string, IconName> = {
  finance: 'scale',
  expense: 'fileText',
  procurement: 'package',
  contract: 'fileText',
  hr: 'user',
  sales: 'apps',
  it: 'wrench',
  analytics: 'activity',
};

function iconFor(app: BusinessApp): IconName {
  const key = app.domain?.toLowerCase() ?? '';
  for (const [needle, icon] of Object.entries(DOMAIN_ICON)) {
    if (key.includes(needle)) return icon;
  }
  return 'apps';
}

/**
 * 业务应用 `/apps`（§4.5）
 *
 * 口径已明确为 **本体**：业务应用 = 一组业务对象 + 一组规则，这正是本体的内容。
 * 服务端 `/apps` 直接投影 `ontology.ontologies`，因此这里的每一张卡片就是一个
 * 管理端设计出来的本体；点「用该本体发起业务」把用户带到 AI 业务助手，并预选该本体。
 *
 * 约束：
 *   - 页面头部**无论有无数据都必须渲染**（空态提前 return 会让用户"没有页面标题"，
 *     失去位置感，也不符合 §3.3 一致的页面层级）；
 *   - 字段严格取契约 `App`（id/ontology_key/name/domain/description/status/
 *     latest_version/latest_version_status），不假定契约未声明的字段。
 */
export function BusinessAppsPage() {
  const query = useBusinessApps();
  const navigate = useNavigate();

  const apps = query.data?.items ?? [];

  return (
    <div className="page">
      <PageHeader
        icon="apps"
        title="业务应用"
        subtitle="业务应用由企业本体承载：一个本体就是一组业务对象与一组规则。选择本体后，可在 AI 业务助手中按该本体的规则发起业务。"
      />

      {query.isLoading ? (
        <LoadingState label="正在加载业务应用…" inline />
      ) : query.error ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : apps.length === 0 ? (
        <EmptyState
          title="暂无可用业务应用"
          description="业务应用来自企业本体；请先在管理端的企业本体页面设计本体。"
          inline
        />
      ) : (
        <div className="grid grid--three">
          {apps.map((app) => (
            <Card key={app.id}>
              <CardHeader
                icon={iconFor(app)}
                title={app.name || app.ontology_key}
                description={`${app.domain || '未分类'} · 本体键 ${app.ontology_key}`}
                actions={
                  app.latest_version ? (
                    <Badge tone={app.latest_version_status === 'PUBLISHED' ? 'success' : 'neutral'}>
                      {app.latest_version}
                    </Badge>
                  ) : (
                    <Badge tone="warning">尚未登记版本</Badge>
                  )
                }
              />
              <CardBody>
                <p className="text-sm">{app.description || '（无描述）'}</p>
                <div className="row" style={{ marginTop: 16 }}>
                  <Button
                    variant="primary"
                    size="sm"
                    icon="arrowRight"
                    onClick={() => navigate(`/assistant?ontology=${encodeURIComponent(app.id)}`)}
                  >
                    用该本体发起业务
                  </Button>
                  <span className="text-xs muted">
                    状态 <span className="mono">{app.status}</span>
                  </span>
                </div>
              </CardBody>
            </Card>
          ))}
        </div>
      )}

      <p className="text-xs muted" style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 4 }}>
        <Icon name="info" size={14} />
        本体是否可用由服务端按当前身份的数据范围实时判定；前端隐藏入口只是体验优化，不是安全边界。
      </p>
    </div>
  );
}
