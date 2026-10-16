import { useState } from 'react';
import { MermaidDiagram } from './MermaidDiagram';

/**
 * MermaidDiagram 组件用法 Demo。
 *
 * 展示常见图表类型的渲染效果，以及错误时的降级行为。
 * 此文件不作为路由页面，仅供开发验证与参考用法。
 *
 * 在其他页面中使用：
 * ```tsx
 * import { MermaidDiagram } from '@/components/mermaid';
 *
 * <MermaidDiagram code="flowchart TD\n  A-->B" />
 * ```
 */

const FLOWCHART = `flowchart TD
    A[用户发起报销] --> B{金额 > 5000?}
    B -->|是| C[部门经理审批]
    B -->|否| D[直接通过]
    C --> E{经理同意?}
    E -->|是| D
    E -->|否| F[驳回申请]
    D --> G[财务付款]
    F --> H[结束]
    G --> H`;

const SEQUENCE = `sequenceDiagram
    participant U as 用户
    participant C as 前端
    participant A as Agent
    participant G as 本体图

    U->>C: 提交报销单
    C->>A: 创建推理运行
    A->>G: 查询审批规则
    G-->>A: 返回 policy:approve
    A->>G: 写入断言三元组
    A-->>C: run_id
    C-->>U: 提交成功`;

const CLASS_DIAGRAM = `classDiagram
    class Employee {
        +String employeeId
        +String name
        +String department
        +decimal salary
    }
    class ExpenseReport {
        +String reportId
        +String title
        +decimal amount
        +Date submittedAt
        +String status
    }
    class Approval {
        +String approvalId
        +Date approvedAt
        +String decision
    }
    Employee "1" --> "0..*" ExpenseReport : submits
    ExpenseReport "1" --> "0..1" Approval : has`;

const ER_DIAGRAM = `erDiagram
    EMPLOYEE ||--o{ EXPENSE_REPORT : submits
    EXPENSE_REPORT ||--o| APPROVAL : has
    EMPLOYEE {
        string employee_id PK
        string name
        string department
    }
    EXPENSE_REPORT {
        string report_id PK
        string title
        decimal amount
        string status
    }
    APPROVAL {
        string approval_id PK
        string decision
        date approved_at
    }`;

const STATE_DIAGRAM = `stateDiagram-v2
    [*] --> 草稿
    草稿 --> 已提交 : 提交
    已提交 --> 审批中 : 经理打开
    审批中 --> 已通过 : 批准
    审批中 --> 已驳回 : 拒绝
    已通过 --> 已付款 : 财务打款
    已付款 --> [*]
    已驳回 --> [*]`;

const GANTT = `gantt
    title 报销处理时间线
    dateFormat  YYYY-MM-DD
    axisFormat  %m/%d

    section 用户
    填写报销单       :a1, 2026-10-01, 1d
    提交申请         :a2, after a1, 1d

    section 经理
    审核             :b1, after a2, 2d

    section 财务
    付款             :c1, after b1, 2d`;

const INVALID_CODE = `flowchart TD
    A[开始] ---> B{判断}
    B -->|是| C[执行
    这是一段故意写错的代码`;

type Preset = { label: string; code: string };
const PRESETS: Preset[] = [
  { label: 'Flowchart', code: FLOWCHART },
  { label: 'Sequence', code: SEQUENCE },
  { label: 'Class', code: CLASS_DIAGRAM },
  { label: 'ER', code: ER_DIAGRAM },
  { label: 'State', code: STATE_DIAGRAM },
  { label: 'Gantt', code: GANTT },
  { label: 'Invalid (error test)', code: INVALID_CODE },
];

export function MermaidDiagramDemo() {
  const [selected, setSelected] = useState(0);
  const [customCode, setCustomCode] = useState<string>(PRESETS[0].code);

  const activePreset = PRESETS[selected];

  return (
    <div style={{ padding: 24, display: 'flex', flexDirection: 'column', gap: 16 }}>
      <h2 style={{ margin: 0 }}>MermaidDiagram Demo</h2>
      <p style={{ margin: 0, color: 'var(--color-text-muted)' }}>
        Mermaid v12 + mermaid.run API 渲染组件。选择预设或编辑下方代码，右侧实时预览。
      </p>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        {PRESETS.map((p, idx) => (
          <button
            key={p.label}
            onClick={() => {
              setSelected(idx);
              setCustomCode(p.code);
            }}
            style={{
              padding: '4px 12px',
              cursor: 'pointer',
              background: idx === selected ? 'var(--color-primary)' : 'transparent',
              color: idx === selected ? '#fff' : 'var(--color-text)',
              border: '1px solid var(--color-border)',
              borderRadius: 4,
            }}
          >
            {p.label}
          </button>
        ))}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <div>
          <div style={{ fontSize: 12, color: 'var(--color-text-muted)', marginBottom: 4 }}>
            编辑 Mermaid 代码：
          </div>
          <textarea
            value={customCode}
            onChange={(e) => setCustomCode(e.target.value)}
            spellCheck={false}
            style={{
              width: '100%',
              minHeight: 320,
              fontFamily: 'ui-monospace, monospace',
              fontSize: 13,
              padding: 12,
              borderRadius: 6,
              border: '1px solid var(--color-border)',
              resize: 'vertical',
              boxSizing: 'border-box',
            }}
          />
        </div>
        <div>
          <div style={{ fontSize: 12, color: 'var(--color-text-muted)', marginBottom: 4 }}>
            渲染预览：
          </div>
          <div
            style={{
              padding: 16,
              border: '1px solid var(--color-border)',
              borderRadius: 6,
              minHeight: 320,
              overflow: 'auto',
              background: 'var(--color-bg-secondary, #fafafa)',
            }}
          >
            <MermaidDiagram code={customCode} id={`demo-${activePreset.label}`} />
          </div>
        </div>
      </div>
    </div>
  );
}
