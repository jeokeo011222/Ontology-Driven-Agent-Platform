import type { ReactNode } from 'react';
import { useState } from 'react';
import { Button, Dialog, Field, Input } from '@/components/ui';
import { ErrorState } from '@/components/error-state';

export interface ConfirmDialogProps {
  open: boolean;
  title: ReactNode;
  /** 确认按钮文字必须明确描述动作，例如“停用连接器”，不要用统一的“确定”（§9.2） */
  confirmLabel: string;
  onConfirm: () => void;
  onCancel: () => void;
  /** 操作对象 */
  target?: ReactNode;
  /** 操作影响 */
  impact?: ReactNode;
  /** 适用租户与环境 */
  scope?: ReactNode;
  /** 是否可逆 */
  reversible?: boolean;
  /** 是否需要额外审批 */
  requiresApproval?: boolean;
  /** 操作完成后的核验方式 */
  verification?: ReactNode;
  /** 需要用户输入的确认短语（如对象 ID），输入一致后才允许确认 */
  confirmPhrase?: string;
  /** 高风险/不可逆：确认按钮使用 danger 视觉权重 */
  danger?: boolean;
  /** 提交中（§9.4 防止重复提交） */
  busy?: boolean;
  /** 提交失败（尤其结果未知时不得显示成功） */
  error?: unknown;
}

/**
 * 危险操作确认对话框（§9.2）。
 * 展示：操作对象、影响、租户与环境、可逆性、是否需额外审批、确认信息、核验方式。
 */
export function ConfirmDialog({
  open,
  title,
  confirmLabel,
  onConfirm,
  onCancel,
  target,
  impact,
  scope,
  reversible,
  requiresApproval,
  verification,
  confirmPhrase,
  danger,
  busy,
  error,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState('');
  const phraseOk = !confirmPhrase || typed.trim() === confirmPhrase;

  return (
    <Dialog
      open={open}
      title={title}
      onClose={busy ? () => undefined : onCancel}
      footer={
        <>
          <Button variant="secondary" onClick={onCancel} disabled={busy} icon="x">
            取消
          </Button>
          <Button
            variant={danger ? 'danger' : 'primary'}
            onClick={onConfirm}
            loading={busy}
            disabled={!phraseOk}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <dl className="def-list">
        {target ? (
          <>
            <dt className="def-list__term">操作对象</dt>
            <dd className="def-list__desc">{target}</dd>
          </>
        ) : null}
        {impact ? (
          <>
            <dt className="def-list__term">操作影响</dt>
            <dd className="def-list__desc">{impact}</dd>
          </>
        ) : null}
        {scope ? (
          <>
            <dt className="def-list__term">适用范围（租户 / 环境）</dt>
            <dd className="def-list__desc">{scope}</dd>
          </>
        ) : null}
        <dt className="def-list__term">是否可逆</dt>
        <dd className="def-list__desc">{reversible ? '可逆（详见恢复方式）' : '不可逆，请谨慎操作'}</dd>
        <dt className="def-list__term">是否需要额外审批</dt>
        <dd className="def-list__desc">{requiresApproval ? '需要额外审批后才能生效' : '不需要额外审批'}</dd>
        {verification ? (
          <>
            <dt className="def-list__term">操作完成后核验方式</dt>
            <dd className="def-list__desc">{verification}</dd>
          </>
        ) : null}
      </dl>

      {confirmPhrase ? (
        <div style={{ marginTop: 16 }}>
          <Field
            label={`请输入 ${confirmPhrase} 以确认`}
            htmlFor="confirm-phrase"
            hint="该输入用于避免误操作，最终幂等与授权由服务端保证。"
          >
            <Input
              id="confirm-phrase"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={confirmPhrase}
              autoComplete="off"
            />
          </Field>
        </div>
      ) : null}

      {error ? (
        <div style={{ marginTop: 12 }}>
          <ErrorState error={error} compact />
        </div>
      ) : null}
    </Dialog>
  );
}
