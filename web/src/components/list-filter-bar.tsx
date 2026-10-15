import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Button, Input, Select, type SelectOption } from '@/components/ui';

export interface ListFilterBarProps {
  /** 关键字搜索：受控值（后端 ?q= 参数）。组件内部 300ms 防抖后回调。 */
  searchValue: string;
  onSearchChange: (value: string) => void;
  searchPlaceholder?: string;
  /** 状态筛选（后端 ?status= 参数）；不传则不渲染状态下拉。 */
  statusValue?: string;
  onStatusChange?: (value: string) => void;
  statusOptions?: SelectOption[];
  /** 其他自定义筛选控件（如系统类型、时间范围）。 */
  extraFilters?: ReactNode;
}

/**
 * 列表筛选条：管理控制台各列表页统一的关键字查询 + 状态筛选入口。
 *
 * 约定：
 *   - 关键字走后端 ?q=（ILIKE 检索），不是前端本地过滤——本地过滤会在
 *     cursor 翻页时静默丢行，属于假查询；
 *   - 300ms 防抖，避免每个按键都打一次后端；
 *   - 「重置」清空全部筛选并立即回调查询。
 */
export function ListFilterBar({
  searchValue,
  onSearchChange,
  searchPlaceholder = '搜索名称 / 关键字…',
  statusValue,
  onStatusChange,
  statusOptions,
  extraFilters,
}: ListFilterBarProps) {
  const [localSearch, setLocalSearch] = useState(searchValue);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    setLocalSearch(searchValue);
  }, [searchValue]);

  const handleSearchInput = (next: string) => {
    setLocalSearch(next);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => onSearchChange(next), 300);
  };

  useEffect(() => () => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
  }, []);

  const hasFilters = localSearch !== '' || (statusValue ?? '') !== '';

  const reset = () => {
    setLocalSearch('');
    onSearchChange('');
    if (onStatusChange) onStatusChange('');
  };

  return (
    <div
      role="search"
      style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBottom: 12 }}
    >
      <div style={{ minWidth: 220, flex: '0 1 280px' }}>
        <Input
          value={localSearch}
          placeholder={searchPlaceholder}
          aria-label="关键字查询"
          onChange={(e) => handleSearchInput(e.target.value)}
        />
      </div>
      {statusOptions && onStatusChange ? (
        <div style={{ minWidth: 140 }}>
          <Select
            value={statusValue ?? ''}
            options={statusOptions}
            aria-label="状态筛选"
            onChange={(e) => onStatusChange(e.target.value)}
          />
        </div>
      ) : null}
      {extraFilters}
      {hasFilters ? (
        <Button variant="secondary" size="sm" onClick={reset} icon="x">
          重置
        </Button>
      ) : null}
    </div>
  );
}

export const FILTER_ALL_OPTION: SelectOption = { value: '', label: '全部状态' };
