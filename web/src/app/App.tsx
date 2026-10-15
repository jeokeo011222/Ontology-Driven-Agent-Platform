import { RouterProvider } from 'react-router-dom';
import { Providers } from './providers';
import { router } from './router';

/**
 * app/App.tsx —— 应用根组件
 * Providers 提供数据层（TanStack Query）与会话上下文；RouterProvider 挂载路由表。
 */
export function App() {
  return (
    <Providers>
      <RouterProvider router={router} />
    </Providers>
  );
}
