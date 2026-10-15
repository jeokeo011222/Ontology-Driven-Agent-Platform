import { useModelProviders } from '@/features/llm-providers/api';
import type { SelectOption } from '@/components/ui';

export interface ModelOptionsResult {
  options: SelectOption[];
  /**
   * 无可用模型端点时的明确原因。fail-closed：绝不静默渲染一个空下拉让用户
   * 误以为"没有可选模型是正常的"——原因必须指向「模型与推理」页的配置动作。
   */
  emptyReason: string | null;
  isLoading: boolean;
}

/**
 * AI 生成弹窗的「指定模型」选项源：复用「模型与推理」页的供应商/端点配置
 * （GET /admin/model-providers），把「端点 × 已登记模型」展开为下拉选项。
 *
 * 口径与模型网关路由一致：
 *   - 只列 status=ACTIVE 且已登记 model_ids 的端点（未登记模型版本的端点不参与路由）；
 *   - value 是 model_id（网关 Routing 的键），同一模型名挂在多个端点时去重取首个；
 *   - label 带「供应商 / 端点 / 模型」，用户能看出这个模型从哪来。
 */
export function useModelOptions(): ModelOptionsResult {
  const { data, isLoading } = useModelProviders();

  if (isLoading) {
    return { options: [], emptyReason: null, isLoading: true };
  }

  const providerName = new Map(
    (data?.providers ?? []).map((p) => [p.provider_id, p.display_name] as const),
  );
  const options: SelectOption[] = [];
  const seen = new Set<string>();
  for (const ep of data?.endpoints ?? []) {
    if (ep.status !== 'ACTIVE' || !ep.model_ids) continue;
    for (const mid of ep.model_ids.split(',').map((s) => s.trim()).filter(Boolean)) {
      if (seen.has(mid)) continue;
      seen.add(mid);
      options.push({
        value: mid,
        label: `${providerName.get(ep.provider_id) ?? ep.provider_id} / ${ep.endpoint_name} / ${mid}`,
      });
    }
  }

  const emptyReason =
    options.length === 0
      ? '尚未配置可参与路由的模型端点：请先到「模型与推理」页登记供应商、端点并登记模型版本。'
      : null;
  return { options, emptyReason, isLoading: false };
}
