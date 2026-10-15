import { z } from 'zod';

/**
 * features/approvals/schema.ts —— 审批决策表单 Schema
 *
 * §10.3：Zod schema 与契约生成类型对齐；**前端校验只做体验层，权威校验在服务端**。
 */
export const decisionSchema = z
  .object({
    decision: z.enum(['APPROVE', 'REJECT', 'RETURN'], {
      errorMap: () => ({ message: '请选择审批结论' }),
    }),
    comment: z.string().max(500, '备注不超过 500 字').optional(),
    /** 本次授权会执行什么操作、作用于哪个对象、有效期多久（§4.4 二次确认要求） */
    confirm_scope: z.string().min(1, '请说明本次授权的作用范围与有效期').max(300, '不超过 300 字'),
  })
  .superRefine((value, ctx) => {
    if ((value.decision === 'REJECT' || value.decision === 'RETURN') && !value.comment?.trim()) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['comment'],
        message: '拒绝或退回时必须填写理由',
      });
    }
  });

export type DecisionFormValues = z.infer<typeof decisionSchema>;

export const DECISION_LABEL: Record<DecisionFormValues['decision'], string> = {
  APPROVE: '审批通过',
  REJECT: '拒绝',
  RETURN: '退回补充信息',
};

export const DECISION_OPTIONS = [
  { value: 'APPROVE', label: '审批通过' },
  { value: 'REJECT', label: '拒绝' },
  { value: 'RETURN', label: '退回补充信息' },
];
