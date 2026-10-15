import { useCallback, useEffect, useState } from 'react';

/**
 * hooks/useTheme.ts —— 亮色 / 暗色主题切换
 *
 * 优先级：
 *   1. 用户手动选择（localStorage 中的 'odap-theme'）
 *   2. 系统偏好 (prefers-color-scheme)
 *   3. 默认亮色（与 tokens.css 中的 :root 保持一致）
 *
 * 为什么用 data-theme 而不是 CSS @media：
 *   - 用户可以强制覆盖系统偏好
 *   - 切换瞬间不需要重新计算媒体查询
 *   - 与 index.html 中的 inline script 配合避免 FOUC
 */

export type Theme = 'light' | 'dark';

const STORAGE_KEY = 'odap-theme';

function getInitialTheme(): Theme {
  // 优先读取用户保存的偏好
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved === 'light' || saved === 'dark') return saved;

  // 否则检测系统偏好
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  return 'light';
}

function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
}

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(getInitialTheme);

  // 挂载时立即应用主题（与 index.html 中的 inline script 双重保险）
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  // 监听系统偏好变化（仅当用户没有手动覆盖时才跟随）
  useEffect(() => {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === 'light' || saved === 'dark') return; // 用户已锁定

    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = (e: MediaQueryListEvent) => {
      setTheme(e.matches ? 'dark' : 'light');
    };

    // 现代浏览器
    if (mq.addEventListener) {
      mq.addEventListener('change', onChange);
      return () => mq.removeEventListener('change', onChange);
    }
    // Safari 旧版兼容
    mq.addListener(onChange);
    return () => mq.removeListener(onChange);
  }, []);

  const toggleTheme = useCallback(() => {
    setTheme((prev) => {
      const next: Theme = prev === 'light' ? 'dark' : 'light';
      localStorage.setItem(STORAGE_KEY, next);
      return next;
    });
  }, []);

  const setThemeManual = useCallback((next: Theme) => {
    localStorage.setItem(STORAGE_KEY, next);
    setTheme(next);
  }, []);

  return { theme, toggleTheme, setTheme: setThemeManual };
}
