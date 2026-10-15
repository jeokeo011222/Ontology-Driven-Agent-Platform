import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { register, type RegisterRequest } from '@/features/auth/api';
import { Button, Input, Field } from '@/components/ui';
import { Icon } from '@/components/icon';
import { AuthShell } from '@/features/auth/AuthShell';

/**
 * 注册页面 —— POST /api/v1/auth/register
 * 用户基于 org_code 加入对应组织
 */
export function RegisterPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<RegisterRequest>({
    org_code: '',
    login_name: '',
    display_name: '',
    email: '',
    password: '',
  });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await register(form);
      navigate('/workspace', { replace: true });
    } catch (err) {
      const message = err instanceof Error ? err.message : '注册失败，请稍后重试';
      setError(message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <AuthShell
      eyebrow="创建账号"
      title="注册 ODAP 账号"
      subtitle="填写所属组织编码，账号会自动加入该租户"
    >
      <form className="auth-form" onSubmit={handleSubmit} noValidate>
        <Field label="组织编码 org_code" required>
          <Input
            type="text"
            value={form.org_code}
            onChange={(e) => setForm({ ...form, org_code: e.target.value })}
            placeholder="由管理员预先创建的组织编码"
            required
            disabled={loading}
          />
        </Field>
        <Field label="登录名" required>
          <Input
            type="text"
            autoComplete="username"
            value={form.login_name}
            onChange={(e) => setForm({ ...form, login_name: e.target.value })}
            placeholder="全局唯一，3 字符以上"
            required
            minLength={3}
            disabled={loading}
          />
        </Field>
        <Field label="显示名" required>
          <Input
            type="text"
            value={form.display_name}
            onChange={(e) => setForm({ ...form, display_name: e.target.value })}
            placeholder="真实姓名或昵称"
            required
            disabled={loading}
          />
        </Field>
        <Field label="邮箱">
          <Input
            type="email"
            value={form.email}
            onChange={(e) => setForm({ ...form, email: e.target.value })}
            placeholder="可选"
            disabled={loading}
          />
        </Field>
        <Field label="密码" required>
          <Input
            type="password"
            autoComplete="new-password"
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
            placeholder="至少 6 位"
            required
            minLength={6}
            disabled={loading}
          />
        </Field>

        {error && (
          <div className="auth-error" role="alert">
            <Icon name="alertTriangle" size={16} />
            <span>{error}</span>
          </div>
        )}

        <Button type="submit" variant="primary" size="lg" loading={loading} block>
          {loading ? '注册中…' : '注册并登录'}
        </Button>

        <p className="auth-alt">
          已有账号？<Link to="/login">直接登录</Link>
        </p>
      </form>
    </AuthShell>
  );
}
