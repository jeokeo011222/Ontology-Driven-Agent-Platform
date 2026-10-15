import { Select } from '@/components/ui';
import { useModelOptions } from '@/hooks/useModelOptions';

export interface ModelSelectProps {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}

/**
 * AI 生成弹窗共用的「指定模型」选择器。
 *
 * 数据来自「模型与推理」页的供应商/端点配置（useModelOptions），不再是自由
 * 文本：自由文本可以让用户填出任何 model_id，而网关只会把它路由到
 * Routing 表里真实登记的模型——填错的值要到一次昂贵往返之后才报错。
 *
 * 留空 = 按能力择优（合法路径）；无可用端点时展示明确原因（fail-closed）。
 */
export function ModelSelect({ value, onChange, disabled }: ModelSelectProps) {
  const { options, emptyReason, isLoading } = useModelOptions();
  return (
    <>
      <Select
        value={value}
        disabled={disabled || isLoading}
        placeholder={isLoading ? '加载模型端点…' : '按能力自动路由（可指定）'}
        options={options}
        onChange={(e) => onChange(e.target.value)}
      />
      {emptyReason ? <span className="field__hint">{emptyReason}</span> : null}
    </>
  );
}
