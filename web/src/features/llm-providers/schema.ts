import { z } from 'zod';

/**
 * 新建模型供应商（含首个端点）表单 schema —— 与契约 CreateModelProviderRequest 同源。
 *
 * 历史（缺陷 #22 遗留）：本文件此前是一版**从未被任何组件引用**的表单 schema，
 *   它要求 `api_key`（**明文密钥**）、`model_id`，且 `protocol` 取值为
 *   `openai-compatible | anthropic | azure-openai`——三者后端一律不认。
 *   现按真实契约重写。
 *
 * 校验意图与服务端**同源**（服务端仍独立校验，前端只为提前反馈）：
 *   - provider_type 只允许 OPENAI_COMPATIBLE；deployment_type 只允许 HTTP
 *     （MOCK 部署形态已下线：它会在进程内返回确定性内容，属于伪造数据）
 *   - HTTP 端点必须同时有 base_url，且凭据二选一：`credential_ref`（环境变量引用名）
 *     或 `api_key`（明文密钥，服务端 AES-256-GCM 加密落库）。二者**不可同时提供**。
 *   - provider_id 须满足规范形状 `^[a-z0-9]+(-[a-z0-9]+)*$`（长度 2–63），
 *     **派生与显式提供一视同仁**（服务端 ValidateProviderID 同规则）。
 *
 * 凭据安全（§8.2 / §13.2）：明文 `api_key` 只入不出——可随请求提交给服务端加密落库，
 *   但响应与审计绝不回显；列表页只展示打码值（如 sk-****6e22）。
 */

/** 与服务端 providerIDRe 同源的规范形状。 */
export const PROVIDER_ID_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;
export const PROVIDER_ID_MIN = 2;
export const PROVIDER_ID_MAX = 63;

/** 与服务端 isIDSafeSeparator 同源：折叠为 '-' 不丢失身份语义的字符。 */
function isIdSafeSeparator(ch: string): boolean {
  return ' -_./+:;,()[]@&\''.includes(ch);
}

/**
 * 与服务端 DeriveProviderID 同源：由显示名派生 provider_id，失败返回 null。
 *
 * 只在**无损**映射时成功：字母数字保留（转小写），安全分隔符折叠为单个 '-'；
 * 出现任何不可映射字符（中文、重音字母、emoji……）即返回 null —— 与服务端
 * fail-closed 行为一致，避免"前端放过、后端 400"的往返。
 */
export function deriveProviderId(displayName: string): string | null {
  let out = '';
  let lastDash = true;
  for (const ch of displayName.toLowerCase()) {
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
      out += ch;
      lastDash = false;
    } else if (isIdSafeSeparator(ch)) {
      if (!lastDash) {
        out += '-';
        lastDash = true;
      }
    } else {
      return null;
    }
  }
  const id = out.replace(/^-+|-+$/g, '');
  return id === '' ? null : id;
}

export const createModelProviderSchema = z
  .object({
    display_name: z.string().trim().min(1, '请填写供应商显示名'),
    /** 留空由服务端按 display_name 派生；派生不成立时必须显式填写 */
    provider_id: z.string().trim().optional(),
    provider_type: z.enum(['OPENAI_COMPATIBLE']),
    endpoint_name: z.string().trim().min(1, '请填写端点名'),
    deployment_type: z.enum(['HTTP']),
    region_code: z.string().trim().optional(),
    base_url: z.string().trim().optional(),
    /** 环境变量引用名（如 ODAP_LLM_API_KEY）；与 api_key 二选一 */
    credential_ref: z.string().trim().optional(),
    /** 明文密钥（只入不出）：与 credential_ref 二选一，服务端加密落库 */
    api_key: z.string().trim().optional(),
  })
  .superRefine((v, ctx) => {
    // ---- provider_id：显式填写时校验规范形状；留空时校验"派生是否成立" ----
    const pid = v.provider_id ?? '';
    if (pid !== '') {
      if (pid.length < PROVIDER_ID_MIN || pid.length > PROVIDER_ID_MAX) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['provider_id'],
          message: `标识长度须在 ${PROVIDER_ID_MIN}–${PROVIDER_ID_MAX} 之间`,
        });
      } else if (!PROVIDER_ID_PATTERN.test(pid)) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['provider_id'],
          message: '只允许小写字母、数字与单个连字符（如 acme-llm），不能以连字符开头或结尾',
        });
      }
    } else if (v.display_name !== '' && deriveProviderId(v.display_name) === null) {
      // 与服务端同一判定：显示名含中文等不可映射字符 ⇒ 无法隐式派生。
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['provider_id'],
        message: '显示名含中文等非 ASCII 字符，无法自动派生标识，请在此显式填写（如 acme-llm）',
      });
    }

    // ---- 端点：HTTP 需地址 + 凭据二选一（MOCK 部署形态已下线） ----
    if (!v.base_url) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['base_url'], message: '端点必须提供 base_url' });
    } else if (!/^https?:\/\//.test(v.base_url)) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['base_url'],
        message: 'base_url 须以 http:// 或 https:// 开头',
      });
    }
    if (v.credential_ref && v.api_key) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['api_key'],
        message: '凭据二选一：credential_ref 与 api_key 不能同时填写',
      });
    } else if (!v.credential_ref && !v.api_key) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['api_key'],
        message: '端点须填写「密钥」或「环境变量引用名」之一',
      });
    }
  });

export type CreateModelProviderFormValues = z.infer<typeof createModelProviderSchema>;

/**
 * 探测（「测试连接」）表单校验 —— 与创建 schema 同源规则。
 *
 * 探测同样要求"base_url + 凭据二选一"，否则后端 Probe 会以
 * INVALID_ARGUMENT 直接拒绝（回落成"填错了"的往返）。
 */
export const probeSchema = z
  .object({
    deployment_type: z.enum(['HTTP']),
    base_url: z.string().trim().optional(),
    credential_ref: z.string().trim().optional(),
    api_key: z.string().trim().optional(),
    model_id: z.string().trim().optional(),
  })
  .superRefine((v, ctx) => {
    if (v.deployment_type === 'HTTP') {
      if (!v.base_url) {
        ctx.addIssue({ code: z.ZodIssueCode.custom, path: ['base_url'], message: '探测需要 base_url' });
      } else if (!/^https?:\/\//.test(v.base_url)) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['base_url'],
          message: 'base_url 须以 http:// 或 https:// 开头',
        });
      }
      if (v.credential_ref && v.api_key) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['api_key'],
          message: '凭据二选一：credential_ref 与 api_key 不能同时填写',
        });
      } else if (!v.credential_ref && !v.api_key) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['api_key'],
          message: '探测需要密钥或环境变量引用名之一',
        });
      }
    }
  });

export type ProbeFormValues = z.infer<typeof probeSchema>;
