/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** 后端基址；默认空字符串 → 同源 + vite proxy（§10.1） */
  readonly VITE_API_BASE?: string;
  /** 为 "1" 时启用静态样例数据，仅用于 UI 演示与构建，不改变契约路径 */
  readonly VITE_USE_FIXTURES?: string;
  /** 环境标识：dev / staging / prod（§5.2 顶部栏环境标识） */
  readonly VITE_ENV_NAME?: string;
  /** 实时通道端点覆盖（默认 /api/v1/stream） */
  readonly VITE_STREAM_PATH?: string;
}
