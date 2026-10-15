import type { ReactNode } from 'react';
import { Icon, type IconName } from '@/components/icon';

/**
 * AuthShell —— 登录 / 注册页共用的认证外壳。
 *
 * 背景：此前 LoginPage / RegisterPage 使用的 `.auth-page` / `.auth-card` /
 *   `.auth-title` 等类名**在任何 CSS 文件中都不存在**，页面实际上是零样式的
 *   裸 HTML（这也是它看起来极其粗糙的直接原因）。本组件把认证页的版式收敛到
 *   一处：左侧品牌叙事 + 右侧表单卡，两者样式统一由 globals.css 的 `.auth-*`
 *   规则提供，避免再次出现"类名悬空、样式缺失"。
 *
 * 无障碍与降级：
 *   - 左侧纯装饰层一律 aria-hidden，读屏只读到标题与要点。
 *   - prefers-reduced-motion 下动画由 globals.css 关闭。
 *   - 窄屏（< 900px）时左侧折叠为紧凑品牌条，表单区占满宽度。
 */
export interface AuthShellProps {
  /** 右侧卡片内的内容（表单） */
  children: ReactNode;
  /** 卡片顶部的小徽标文案，如"登录" / "注册" */
  eyebrow: string;
  /** 卡片主标题 */
  title: string;
  /** 卡片副标题 */
  subtitle: string;
}

const HIGHLIGHTS: { icon: IconName; title: string; text: string }[] = [
  {
    icon: 'ontology',
    title: '本体驱动的语义底座',
    text: '业务概念、关系与约束集中建模，Agent 的每一步推理都有据可依。',
  },
  {
    icon: 'shieldCheck',
    title: '可治理的模型调用链',
    text: '路由、限额、预算与用量账本全程留痕，模型调用不失控、不静默降级。',
  },
  {
    icon: 'workflow',
    title: '受控的自动化执行',
    text: '需要审批的动作自动进入流程，关键操作可回溯、可复核。',
  },
];

export function AuthShell({ children, eyebrow, title, subtitle }: AuthShellProps) {
  return (
    <div className="auth-page">
      {/* ---------- 左：品牌叙事 ---------- */}
      <aside className="auth-hero">
        <div className="auth-hero__deco" aria-hidden="true">
          <span className="auth-hero__glow auth-hero__glow--a" />
          <span className="auth-hero__glow auth-hero__glow--b" />
          <span className="auth-hero__grid" />
          <svg className="auth-hero__watermark" viewBox="0 0 240 240" fill="none" focusable="false">
            <g stroke="currentColor" strokeWidth="1.1" strokeLinecap="round">
              <path d="M120 40 44 96l28 88h96l28-88z" />
              <path d="M120 40v56M120 96 44 96M120 96l76-6M68 184l52-88M172 184l-52-88M68 184h104" />
            </g>
            <g fill="currentColor">
              <circle cx="120" cy="40" r="7" />
              <circle cx="120" cy="96" r="7" />
              <circle cx="44" cy="96" r="7" />
              <circle cx="196" cy="90" r="7" />
              <circle cx="68" cy="184" r="7" />
              <circle cx="172" cy="184" r="7" />
            </g>
          </svg>
        </div>

        <div className="auth-hero__inner">
          <div className="auth-brand">
            <span className="auth-brand__mark" aria-hidden="true">
              <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" focusable="false">
                <circle cx="12" cy="5.2" r="2.6" />
                <circle cx="5.2" cy="18" r="2.6" />
                <circle cx="18.8" cy="18" r="2.6" />
                <path d="M10.1 7.3 6.6 15.7" />
                <path d="M13.9 7.3l3.5 8.4" />
                <path d="M7.8 18h8.4" />
              </svg>
            </span>
            <span className="auth-brand__text">
              <span className="auth-brand__name">ODAP</span>
              <span className="auth-brand__sub">Ontology-Driven Agent Platform</span>
            </span>
          </div>

          <h2 className="auth-hero__headline">
            让企业的每一次推理
            <br />
            都落在本体之上
          </h2>
          <p className="auth-hero__lede">
            面向制度密集型企业的一体化智能体平台：本体自动生成、插件式能力编排、
            ERP 与外部系统集成，全部在一个受治理的运行时里完成。
          </p>

          <ul className="auth-hero__points">
            {HIGHLIGHTS.map((h) => (
              <li className="auth-point" key={h.title}>
                <span className="auth-point__icon" aria-hidden="true">
                  <Icon name={h.icon} size={18} />
                </span>
                <span className="auth-point__body">
                  <span className="auth-point__title">{h.title}</span>
                  <span className="auth-point__text">{h.text}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>

        <p className="auth-hero__foot">© 2026 ODAP · 企业本体驱动智能体平台</p>
      </aside>

      {/* ---------- 右：表单卡 ---------- */}
      <main className="auth-panel">
        <div className="auth-card">
          <span className="auth-card__eyebrow">{eyebrow}</span>
          <h1 className="auth-title">{title}</h1>
          <p className="auth-sub">{subtitle}</p>
          {children}
        </div>
        <p className="auth-panel__foot">
          登录即表示你已阅读并同意平台的数据使用与安全规范。
        </p>
      </main>
    </div>
  );
}
