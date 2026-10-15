import { useEffect, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { Button, Card, CardBody, CardHeader, Field, PageHeader, Select, Textarea } from '@/components/ui';
import { ErrorState } from '@/components/error-state';
import { LoadingState } from '@/components/loading-state';
import { PermissionGate } from '@/components/permission-gate';
import { OntologyReviewPanel } from '@/components/ontology-review-panel';
import { Icon } from '@/components/icon';
import { useCreateRun, useWorkspaceOntologies } from '../api';

const schema = z.object({
  ontology_id: z.string().min(1, '请选择本次审查依据的本体'),
  objective: z.string().min(8, '请至少用 8 个字描述你的业务目标').max(500, '不超过 500 字'),
  business_type: z.string().max(60).optional(),
});

type FormValues = z.infer<typeof schema>;

const EXAMPLES = [
  '请检查报销单 claim-1001，核对凭证、费用类别和预算，并在符合制度时提交审批',
  '审查采购合同 contract-2201 的条款风险与授权范围',
  '查询企业差旅制度的适用范围',
];

/**
 * AI 业务助手 `/assistant`（§4.2）
 *
 * 关键约定：
 *   - **基于本体**：业务规则来自用户选中的本体（形状约束 / 形状属性），助手据此
 *     审查业务目标；没有选中本体就没有可执行的规则，提交按钮不可用；
 *   - 本体清单来自服务端 `GET /workspace/ontologies`，口径与"管理端设计了多少本体"
 *     一致，前端不自行筛选或补位；
 *   - 采用「自然语言输入 + 结构化执行计划 + 明确业务结果」三段式，而不是聊天机器人；
 *   - 不伪造推理进度：执行计划与审查结论全部来自服务端响应，前端不预置静态步骤。
 */
export function AssistantPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const createRun = useCreateRun();
  const ontologiesQuery = useWorkspaceOntologies();

  const ontologies = useMemo(() => ontologiesQuery.data?.items ?? [], [ontologiesQuery.data]);

  // 下拉项由服务端清单直接映射，不在前端补位或占位：清单里有多少本体，就有多少个
  // 可选项（需求：管理端设计了多少本体，业务工作台就显示多少个）。
  const ontologyOptions = useMemo(
    () =>
      ontologies.map((ontology) => ({
        value: ontology.id,
        label: `${ontology.name || ontology.ontology_key}${ontology.domain ? ` · ${ontology.domain}` : ''}`,
      })),
    [ontologies],
  );

  // 从「业务应用」页跳转而来时带上 ?ontology=<id>：把用户选的业务应用直接映射成
  // 审查依据。只在候选项里确实存在该 id 时采用，避免把任意 URL 参数当成有效输入。
  const preselected = searchParams.get('ontology') ?? '';
  const defaultOntologyID = ontologies.some((o) => o.id === preselected) ? preselected : '';

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { ontology_id: defaultOntologyID, objective: '', business_type: '' },
  });

  // 本体清单是异步加载的，`defaultValues` 只在首次渲染生效；等清单到位后再把
  // ?ontology= 预选项写进表单（仅在用户尚未选择时），否则「从业务应用进入」会
  // 变成永远空着的一个下拉框。
  useEffect(() => {
    if (defaultOntologyID && !form.getValues('ontology_id')) {
      form.setValue('ontology_id', defaultOntologyID, { shouldValidate: true });
    }
  }, [defaultOntologyID, form]);

  const onSubmit = form.handleSubmit((values) => {
    createRun.mutate({
      ontology_id: values.ontology_id,
      objective: values.objective,
      business_type: values.business_type || undefined,
    });
  });

  const result = createRun.data;
  const noOntology = !ontologiesQuery.isLoading && !ontologiesQuery.error && ontologies.length === 0;

  return (
    <div className="page">
      <PageHeader
        icon="sparkles"
        title="AI 业务助手"
        subtitle="选择一个企业本体作为审查依据，描述你要完成的工作；系统会按该本体的规则逐条校验并给出结论。"
      />

      <Card className="section">
        <CardHeader
          title="用户请求"
          description="用自然语言描述业务目标；规则来自你选中的本体，不需要你了解图存储或执行引擎细节。"
        />
        <CardBody>
          {ontologiesQuery.isLoading ? (
            <LoadingState label="正在读取可用本体…" inline />
          ) : ontologiesQuery.error ? (
            <ErrorState error={ontologiesQuery.error} onRetry={() => void ontologiesQuery.refetch()} />
          ) : noOntology ? (
            <div className="banner banner--warning">
              <span className="banner__icon" aria-hidden="true">
                <Icon name="alertTriangle" size={16} />
              </span>
              <span>
                当前没有可用的企业本体，因此无法按本体规则审查业务目标。请先在管理端的企业本体页面设计本体。
              </span>
            </div>
          ) : (
            <PermissionGate
              permission="run.create"
              fallback={
                <div className="banner banner--warning">
                  <span className="banner__icon" aria-hidden="true">
                    <Icon name="ban" size={16} />
                  </span>
                  <span>当前身份无发起任务的权限（run:create）。</span>
                </div>
              }
            >
              <form onSubmit={onSubmit} noValidate>
                <Field
                  label="审查依据（企业本体）"
                  htmlFor="ontology_id"
                  required
                  error={form.formState.errors.ontology_id?.message}
                  hint="本体的形状约束（SHACL）就是本次审查执行的规则来源。"
                >
                  <Select
                    id="ontology_id"
                    placeholder="请选择本体…"
                    options={ontologyOptions}
                    {...form.register('ontology_id')}
                  />
                </Field>

                <Field
                  label="业务目标"
                  htmlFor="objective"
                  required
                  error={form.formState.errors.objective?.message}
                  hint="示例：申请报销、审查合同、查询制度适用范围。"
                >
                  <Textarea
                    id="objective"
                    {...form.register('objective')}
                    placeholder="描述你的业务目标……"
                    rows={4}
                  />
                </Field>

                <div className="row" style={{ marginBottom: 16 }}>
                  {EXAMPLES.map((ex) => (
                    <button
                      key={ex}
                      type="button"
                      className="btn btn--secondary btn--sm"
                      onClick={() => form.setValue('objective', ex, { shouldValidate: true })}
                    >
                      {ex.length > 24 ? `${ex.slice(0, 24)}…` : ex}
                    </button>
                  ))}
                </div>

                <div className="row">
                  <Button
                    type="submit"
                    variant="primary"
                    loading={createRun.isPending}
                    disabled={ontologies.length === 0}
                    icon="send"
                  >
                    提交并执行本体审查
                  </Button>
                  <span className="text-xs muted">
                    提交将创建 Run（携带 Idempotency-Key），并在本次请求内完成按本体的规则校验。
                  </span>
                </div>
              </form>
            </PermissionGate>
          )}

          {createRun.isError ? (
            <div style={{ marginTop: 16 }}>
              <ErrorState error={createRun.error} compact />
            </div>
          ) : null}
        </CardBody>
      </Card>

      {result?.review_conclusion ? (
        <div className="section">
          <OntologyReviewPanel conclusion={result.review_conclusion} plan={result.plan} />
          <div className="row" style={{ marginTop: 16 }}>
            <Button variant="primary" onClick={() => navigate(`/tasks/${result.id}`)} icon="arrowRight">
              查看完整处理过程
            </Button>
            <span className="text-xs muted">
              任务号 <span className="copy-id">{result.id}</span> · 当前状态 {result.status}
              （创建受理不等于业务成功）
            </span>
          </div>
        </div>
      ) : createRun.isSuccess && result?.id ? (
        // 有 Run 却没有结论：说明服务端未返回 review_conclusion。按"不伪造"原则如实说明，
        // 而不是回落到一段静态计划冒充结果。
        <div className="section">
          <div className="banner banner--warning">
            <span className="banner__icon" aria-hidden="true">
              <Icon name="alertTriangle" size={16} />
            </span>
            <span>
              服务端未随本次 Run 返回审查结论。任务已创建（
              <span className="mono">{result.id}</span>），可前往任务详情查看执行步骤。
            </span>
          </div>
          <div className="row" style={{ marginTop: 12 }}>
            <Button variant="secondary" onClick={() => navigate(`/tasks/${result.id}`)} icon="arrowRight">
              查看执行详情
            </Button>
          </div>
        </div>
      ) : null}

      <p className="text-xs muted" style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 4 }}>
        <Icon name="flag" size={14} />
        执行边界：不会直接付款；需要审批的动作将进入审批流程。只读操作是否自动启动由策略决定，界面遵守策略，不自行降低确认门槛。
      </p>
    </div>
  );
}
