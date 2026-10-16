import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// 本配置 self-contained 于 web/ 目录，不引用 D:/Code/Ontology-Agent 上层目录。
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      // 契约前缀 /api/v1（contracts/03 §1.1），开发期代理到后端。
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      // A2A 协议标准路径（Agent Card 预览页签）：不在 /api/v1 前缀下，
      // 由前端带 Bearer Token 直取；生产走 nginx 同名反代。
      '/.well-known': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
    chunkSizeWarningLimit: 1200,
  },
});
