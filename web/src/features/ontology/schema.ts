import { z } from 'zod';

/** 本体发布表单：发布必须进入不可变版本，并显式确认不会静默改变运行中任务的语义上下文（§6.3） */
export const publishSchema = z.object({
  release_notes: z.string().min(10, '发布说明至少 10 个字符').max(1000, '发布说明不超过 1000 字'),
  no_semantic_drift_confirmed: z.literal(true, {
    errorMap: () => ({ message: '必须确认本次发布不会静默改变运行中任务的语义上下文' }),
  }),
});

export type PublishFormValues = z.infer<typeof publishSchema>;
