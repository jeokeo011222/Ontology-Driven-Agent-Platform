import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

export interface FieldProps {
  label?: ReactNode;
  htmlFor?: string;
  required?: boolean;
  hint?: ReactNode;
  error?: ReactNode;
  children: ReactNode;
  className?: string;
}

/**
 * 表单字段容器：标签 + 提示 + 错误。
 * §12.2：输入框具有可识别标签；错误提示说明具体字段与修复方向。
 */
export function Field({ label, htmlFor, required, hint, error, children, className }: FieldProps) {
  return (
    <div className={cn('field', className)}>
      {label ? (
        <label className="field__label" htmlFor={htmlFor}>
          {label}
          {required ? (
            <span className="field__required" aria-hidden="true">
              *
            </span>
          ) : null}
        </label>
      ) : null}
      {children}
      {hint ? <span className="field__hint">{hint}</span> : null}
      {error ? (
        <span className="field__error" role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}
