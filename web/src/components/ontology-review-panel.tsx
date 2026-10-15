import { Badge, Card, CardBody, CardHeader } from '@/components/ui';
import { Icon } from '@/components/icon';
import type { OntologyReviewConclusion, OntologyReviewDecision, OntologyReviewPlan } from '@/types';

/**
 * 本体审查结论面板。
 *
 * 在「AI 业务助手」提交结果与「我的任务 → 详情」两处复用同一份渲染：同一个结论
 * 在两个页面里措辞/配色不一致，用户会怀疑是两份不同的结论。
 *
 * 展示纪律（本项目最看重的诚实性）：
 *   - `conforms` 是 SHACL 引擎的**权威判定**，永远单独展示；模型的 `model_decision`
 *     只作为"模型怎么说"的旁注——因为服务端已经保证不合规不会被模型覆盖，界面也
 *     不能把两者混成一个"最终判断"，否则用户看不出到底是谁在拍板；
 *   - 缺失的字段（无 rule_checks / 无 shape_findings）显示「无」而不是渲染空框，
 *     避免看起来像渲染失败。
 */

const DECISION_LABEL: Record<OntologyReviewDecision, string> = {
  APPROVE: '审查通过',
  REJECT: '审查不通过',
  NEED_INFO: '需补充信息',
  NEED_APPROVAL: '需人工审批',
};

const DECISION_TONE: Record<OntologyReviewDecision, 'success' | 'error' | 'warning' | 'info'> = {
  APPROVE: 'success',
  REJECT: 'error',
  NEED_INFO: 'warning',
  NEED_APPROVAL: 'info',
};

const RULE_OUTCOME_LABEL: Record<string, string> = {
  SATISFIED: '满足',
  VIOLATED: '违反',
  INCONCLUSIVE: '无法判定',
};

const RULE_OUTCOME_TONE: Record<string, 'success' | 'error' | 'warning'> = {
  SATISFIED: 'success',
  VIOLATED: 'error',
  INCONCLUSIVE: 'warning',
};

export interface OntologyReviewPanelProps {
  conclusion: OntologyReviewConclusion;
  /** 服务端随 Run 返回的结构化执行计划（可选，仅用于展示计划步骤）。 */
  plan?: OntologyReviewPlan;
  className?: string;
}

export function OntologyReviewPanel({ conclusion, plan, className }: OntologyReviewPanelProps) {
  const decisionLabel = DECISION_LABEL[conclusion.decision] ?? conclusion.decision;
  const decisionTone = DECISION_TONE[conclusion.decision] ?? 'neutral';

  return (
    <Card className={className}>
      <CardHeader
        icon="shieldCheck"
        title="本体审查结论"
        description="结论由服务端依据真实 SHACL 校验结果确定性合成；模型判断不能覆盖「不合规」的引擎判定。"
        actions={
          <div className="row" style={{ gap: 6 }}>
            <Badge tone={decisionTone}>{decisionLabel}</Badge>
            <Badge tone={conclusion.conforms ? 'success' : 'error'}>
              {conclusion.conforms ? '形状约束：合规' : '形状约束：不合规'}
            </Badge>
          </div>
        }
      />
      <CardBody>
        {/* 依据本体的执行计划：服务端按本体真实结构生成，前端不自行编造步骤。 */}
        {plan?.steps?.length ? (
          <div style={{ marginBottom: 16 }}>
            <div className="text-xs muted" style={{ marginBottom: 6 }}>
              执行计划（来源：<span className="mono">{plan.source}</span>
              {plan.focus_nodes ? `，待校验实例 ${plan.focus_nodes} 个` : ''}）
            </div>
            <ol className="stack stack--tight text-sm" style={{ paddingLeft: 20, listStyle: 'decimal' }}>
              {plan.steps.map((step) => (
                <li key={step.kind}>
                  {step.name}
                  <span className="text-xs muted" style={{ marginLeft: 6 }}>
                    <span className="mono">{step.kind}</span>
                  </span>
                </li>
              ))}
            </ol>
          </div>
        ) : null}

        <div className="text-xs muted" style={{ marginBottom: 6 }}>
          审查理由
        </div>
        {conclusion.reasons?.length ? (
          <ul className="stack stack--tight text-sm" style={{ paddingLeft: 20, listStyle: 'disc' }}>
            {conclusion.reasons.map((reason, index) => (
              <li key={`${index}-${reason}`}>{reason}</li>
            ))}
          </ul>
        ) : (
          <p className="text-sm muted">服务端未给出理由。</p>
        )}

        {/* 逐条规则复核结果：形状 → 判定 → 证据。 */}
        {conclusion.rule_checks?.length ? (
          <div style={{ marginTop: 16 }}>
            <div className="text-xs muted" style={{ marginBottom: 6 }}>
              规则复核明细
            </div>
            <ul className="stack stack--tight text-sm" style={{ paddingLeft: 0, listStyle: 'none' }}>
              {conclusion.rule_checks.map((check, index) => (
                <li key={`${index}-${check.shape_id}`} className="row" style={{ gap: 8, alignItems: 'baseline' }}>
                  <Badge tone={RULE_OUTCOME_TONE[check.outcome] ?? 'neutral'}>
                    {RULE_OUTCOME_LABEL[check.outcome] ?? check.outcome}
                  </Badge>
                  <span className="mono text-xs">{check.shape_id}</span>
                  {check.evidence ? <span className="text-xs muted">{check.evidence}</span> : null}
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        {/* 引擎发现项：这是"不合规"的原始证据，必须原样可读。 */}
        {conclusion.shape_findings?.length ? (
          <div style={{ marginTop: 16 }}>
            <div className="text-xs muted" style={{ marginBottom: 6 }}>
              引擎发现项（SHACL）
            </div>
            <ul className="stack stack--tight text-sm" style={{ paddingLeft: 20, listStyle: 'disc' }}>
              {conclusion.shape_findings.map((finding, index) => (
                <li key={`${index}-${finding.digest ?? finding.message ?? index}`}>
                  <span className="mono text-xs">{finding.shape_id ?? '（未标注形状）'}</span>
                  {finding.path ? <span className="mono text-xs"> · {finding.path}</span> : null}
                  {finding.message ? <span> — {finding.message}</span> : null}
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        <div className="row" style={{ marginTop: 16, gap: 16, flexWrap: 'wrap' }}>
          {conclusion.ontology?.version ? (
            <span className="text-xs muted">
              本体版本 <span className="mono">{conclusion.ontology.version}</span>
            </span>
          ) : null}
          {conclusion.model_decision ? (
            <span className="text-xs muted">
              模型判断 <span className="mono">{conclusion.model_decision}</span>（仅供参考，不覆盖引擎判定）
            </span>
          ) : null}
          {conclusion.graph_iri ? (
            <span className="text-xs muted">
              数据图 <span className="mono">{conclusion.graph_iri}</span>
            </span>
          ) : null}
        </div>

        {conclusion.facts?.length ? (
          <div style={{ marginTop: 12 }}>
            <div className="text-xs muted" style={{ marginBottom: 6 }}>
              抽取的业务对象（{conclusion.facts.length}）
            </div>
            <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
              {conclusion.facts.map((fact) => (
                <Badge key={`${fact.class}-${fact.id}`} tone="neutral" title={fact.class}>
                  {fact.id}
                </Badge>
              ))}
            </div>
          </div>
        ) : null}

        {!conclusion.conforms ? (
          <div className="banner banner--warning" style={{ marginTop: 16 }}>
            <span className="banner__icon" aria-hidden="true">
              <Icon name="alertTriangle" size={16} />
            </span>
            <span>
              引擎判定存在违反形状约束的数据，因此结论不能是「审查通过」。请按上方发现项补齐材料或修正数据后重新提交。
            </span>
          </div>
        ) : null}
      </CardBody>
    </Card>
  );
}
