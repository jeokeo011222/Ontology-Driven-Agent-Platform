import { useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { login, type LoginRequest } from '@/features/auth/api';
import { Button, Field, Input } from '@/components/ui';
import { Icon } from '@/components/icon';
import { AuthShell } from '@/features/auth/AuthShell';

/**
 * 演示账号预填开关（构建期决定，运行时不可变）。
 *
 * 判定规则是"默认关闭、按需开启"：
 *  - Vite 开发服务器（import.meta.env.DEV）下自动开启 —— 这正是"开发环境默认预填"；
 *  - 生产构建产物 DEV 恒为 false，只有显式设置 VITE_DEV_PREFILL=true 才会预填，
 *    以免口令被写进真实部署的前端产物（fail-closed）。
 *
 * 口令默认值与后端种子 internal/seed/apply.go 的 DefaultDevLoginPassword 保持一致，
 * 两侧都可用环境变量覆盖（ODAP_DEV_LOGIN_PASSWORD / VITE_DEV_LOGIN_PASSWORD）。
 */
const DEV_PREFILL = import.meta.env.DEV || import.meta.env.VITE_DEV_PREFILL === 'true';
const DEV_LOGIN_NAME = import.meta.env.VITE_DEV_LOGIN_NAME || 'demo.admin';
const DEV_LOGIN_PASSWORD = import.meta.env.VITE_DEV_LOGIN_PASSWORD || 'odap-demo-2026';

/**
 * 登录页面 —— POST /api/v1/auth/login
 * 成功后持久化 Bearer token 并跳转原来目标页或 /workspace
 *
 * 版式由 AuthShell 提供（左侧品牌叙事 + 右侧表单卡）。字段旁的前导图标与
 * 密码可见性开关属于表单内部的视觉增强，不改变任何提交语义。
 */
export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from ?? '/workspace';

  const [form, setForm] = useState<LoginRequest>(
    DEV_PREFILL
      ? { login_name: DEV_LOGIN_NAME, password: DEV_LOGIN_PASSWORD }
      : { login_name: '', password: '' },
  );
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await login(form);
      navigate(from, { replace: true });
    } catch (err) {
      const message = err instanceof Error ? err.message : '登录失败，请检查网络后重试';
      setError(message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <AuthShell
      eyebrow="欢迎回来"
      title="登录 ODAP 平台"
      subtitle="使用组织账户进入你的工作空间"
    >
      <form className="auth-form" onSubmit={handleSubmit} noValidate>
        <Field label="登录名" required>
          <span className="auth-input">
            <span className="auth-input__icon" aria-hidden="true">
              <Icon name="user" size={16} />
            </span>
            <Input
              className="auth-input__field"
              type="text"
              autoComplete="username"
              value={form.login_name}
              onChange={(e) => setForm({ ...form, login_name: e.target.value })}
              placeholder="你的登录名"
              required
              disabled={loading}
            />
          </span>
        </Field>

        <Field label="密码" required>
          <span className="auth-input">
            <span className="auth-input__icon" aria-hidden="true">
              <Icon name="lock" size={16} />
            </span>
            <Input
              className="auth-input__field auth-input__field--pw"
              type={showPassword ? 'text' : 'password'}
              autoComplete="current-password"
              value={form.password}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
              placeholder="密码"
              required
              minLength={6}
              disabled={loading}
            />
            <button
              type="button"
              className="auth-input__toggle"
              onClick={() => setShowPassword((v) => !v)}
              aria-label={showPassword ? '隐藏密码' : '显示密码'}
              aria-pressed={showPassword}
            >
              {/* 图标集里没有眼睛图标，这里就地内联，避免用一个语义不符的图标凑数 */}
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"
                focusable="false">
                <path d="M2.4 12S5.9 5.8 12 5.8 21.6 12 21.6 12 18.1 18.2 12 18.2 2.4 12 2.4 12Z" />
                <circle cx="12" cy="12" r="3.1" />
                {showPassword ? <path d="m4.2 19.8 15.6-15.6" /> : null}
              </svg>
            </button>
          </span>
        </Field>

        {error && (
          <div className="auth-error" role="alert">
            <Icon name="alertTriangle" size={16} />
            <span>{error}</span>
          </div>
        )}

        <Button type="submit" variant="primary" size="lg" loading={loading} block>
          {loading ? '登录中…' : '登录'}
        </Button>

        <p className="auth-alt">
          还没有账号？<Link to="/register">基于组织注册</Link>
        </p>

        {DEV_PREFILL && (
          <p className="auth-devnote">
            <Icon name="info" size={14} />
            <span>
              开发环境已预填演示账号 <code>{DEV_LOGIN_NAME}</code>，可直接登录
            </span>
          </p>
        )}
      </form>
    </AuthShell>
  );
}
