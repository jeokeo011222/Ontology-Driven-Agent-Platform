import { useEffect, useMemo, useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Badge, Button, Dialog, Field, Input, Select } from '@/components/ui';
import { describeError } from '@/api/errors';
import { z } from 'zod';
import { Icon } from '@/components/icon';
import type { ModelEndpointSummary, ModelProviderSummary } from '@/types';
import {
  useProbeModelProvider, useModelRevisions, useRegisterModelRevision, useDeleteModelRevision,
  type AddModelEndpointInput, type ModelCapabilities, type ProbeResult, type UpdateModelEndpointInput,
} from './api';

/** 端点选择器的「新建端点」哨兵值。 */
export const NEW_ENDPOINT = '__new__';

/**
 * 「编辑接入」表单 schema —— 供应商 + 某一个端点。
 *
 * 与后端 updateModelEndpoint 的**三态密钥**对齐：api_key 留空表示“不改动现有密钥”，
 * 因此**不在** schema 层强制其非空；只有“新建端点 + HTTP”这一种情形必须补凭据
 * （后端 AddModelEndpoint 会对无凭据的 HTTP 端点直接 400 / fail-closed）。
 */
export function makeEditAccessSchema(requireCredential: boolean) {
  return z
    .object({
      display_name: z.string().trim().min(1, '请填写供应商显示名'),
      provider_status: z.enum(['ACTIVE', 'DISABLED']),
      endpoint_select: z.string(),
      endpoint_name: z.string().trim().min(1, '请填写端点名'),
      deployment_type: z.enum(['HTTP']),
      region_code: z.string().trim().optional(),
      base_url: z.string().trim().optional(),
      credential_ref: z.string().trim().optional(),
      api_key: z.string().trim().optional(),
      endpoint_status: z.enum(['ACTIVE', 'DISABLED']),
    })
    .superRefine((v, ctx) => {
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
          message: '凭据二选一：环境变量引用名与新密钥不能同时填写',
        });
      } else if (requireCredential && !v.credential_ref && !v.api_key) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['api_key'],
          message: '端点须填写「新密钥」或「环境变量引用名」之一',
        });
      }
    });
}

export type EditAccessFormValues = z.infer<ReturnType<typeof makeEditAccessSchema>>;

/** 提交载荷：供应商元数据 + 端点（新建 or 部分更新）。 */
export interface EditAccessSubmitPayload {
  provider: { display_name: string; status: 'ACTIVE' | 'DISABLED' };
  /** null = 新建端点 */
  endpointID: string | null;
  endpoint: AddModelEndpointInput | UpdateModelEndpointInput;
}

export interface EditAccessDialogProps {
  open: boolean;
  provider: ModelProviderSummary | null;
  /** 该供应商的全部端点（已按创建时间升序，首个即「接入条目」主端点） */
  endpoints: ModelEndpointSummary[];
  busy?: boolean;
  error?: unknown;
  onClose: () => void;
  onSubmit: (payload: EditAccessSubmitPayload) => void;
}

/**
 * 「编辑接入」对话框：一次管理**供应商 + 其一个端点**（含新增第二个端点）。
 *
 * 密钥显示：**只显示打码值**（如 sk-****6e22）；输入框留空即“不改动现有密钥”，
 * 输入非空则替换为新的密文凭据。任何明文都不回显。
 */
export function EditAccessDialog(props: EditAccessDialogProps) {
  // 以 key 强制在切换供应商时重挂载，保证内部表单初值正确。
  return <EditAccessDialogInner key={props.provider?.provider_id ?? 'none'} {...props} />;
}

function EditAccessDialogInner({
  open, provider, endpoints, busy, error, onClose, onSubmit,
}: EditAccessDialogProps) {
  const [selectedId, setSelectedId] = useState<string>(endpoints[0]?.endpoint_id ?? NEW_ENDPOINT);
  const selected = endpoints.find((e) => e.endpoint_id === selectedId) ?? null;
  const isNew = selected === null;
  const requireCredential = isNew;

  const probe = useProbeModelProvider();
  // 「模型测试」用到的模型名：**仅用于探测**，不属于端点配置，故不进表单、不随保存提交。
  const [probeModelId, setProbeModelId] = useState('');
  const [probeResult, setProbeResult] = useState<ProbeResult | null>(null);

  const schema = useMemo(() => makeEditAccessSchema(requireCredential), [requireCredential]);

  const {
    register, handleSubmit, getValues, reset, formState: { errors, isSubmitting },
  } = useForm<EditAccessFormValues>({
    resolver: zodResolver(schema),
    defaultValues: endpointDefaults(provider, endpoints[0] ?? null),
  });

  // 切换端点后重置表单初值（密钥输入框始终留空 = 不改动），并清掉上一次探测结论
  // （结论属于"上一个端点"，继续显示会张冠李戴）。
  useEffect(() => {
    const e = endpoints.find((x) => x.endpoint_id === selectedId) ?? null;
    reset(endpointDefaults(provider, e));
    setProbeResult(null);
  }, [selectedId, provider, endpoints, reset]);

  const close = () => {
    reset(endpointDefaults(provider, endpoints.find((x) => x.endpoint_id === selectedId) ?? null));
    setProbeResult(null);
    setProbeModelId('');
    probe.reset();
    onClose();
  };

  /**
   * 「模型测试」：用当前表单参数真实试调一次；结果以内联 banner 呈现，不阻塞录入。
   *
   * 凭据来源优先级：表单里新填的「新密钥」→ 表单里的「环境变量引用名」→ 该端点
   * **已存凭据**（传 endpoint_id 交由服务端解析）。因此编辑一个已登记的端点时，
   * 不必为了测试而重新粘贴一次明文密钥；而**新建端点**尚未落库，没有任何已存凭据，
   * 必须当场填写其一。
   *
   * 模型名留空时服务端退化为 GET {base_url}/models（只验地址与鉴权，不消耗配额）；
   * 填入模型名时才真实调用一次聊天补全（max_tokens=1）。
   */
  const onTest = () => {
    setProbeResult(null);
    const v = getValues();
    const apiKey = (v.api_key ?? '').trim();
    const credentialRef = (v.credential_ref ?? '').trim();
    if (!apiKey && !credentialRef && isNew) {
      setProbeResult({
        ok: false,
        latency_ms: 0,
        status_code: 0,
        message: '新建端点尚未保存，没有已存凭据可复用；请先填写「新密钥」或「环境变量引用名」再测试。',
      });
      return;
    }
    probe.mutate(
      {
        deployment_type: v.deployment_type,
        ...(v.base_url ? { base_url: v.base_url } : {}),
        ...(apiKey ? { api_key: apiKey } : {}),
        ...(credentialRef ? { credential_ref: credentialRef } : {}),
        ...(probeModelId.trim() ? { model_id: probeModelId.trim() } : {}),
        ...(isNew ? {} : { endpoint_id: selectedId }),
      },
      {
        onSuccess: (res) => setProbeResult(res.probe),
        onError: (e) => {
          const d = describeError(e);
          setProbeResult({
            ok: false,
            latency_ms: 0,
            status_code: 0,
            message: `探测请求失败：${d.title}：${d.message}（${d.guidance}）`,
          });
        },
      },
    );
  };

  const submit = handleSubmit((v) => {
    const endpoint: AddModelEndpointInput | UpdateModelEndpointInput = {
      endpoint_name: v.endpoint_name,
      deployment_type: v.deployment_type,
      region_code: v.region_code ?? '',
      base_url: v.base_url ?? '',
      status: v.endpoint_status,
      // 凭据：仅在用户显式输入时提交；留空 = 不改动现有密钥（三态之“省略”）。
      ...(v.api_key ? { api_key: v.api_key } : {}),
      ...(v.credential_ref ? { credential_ref: v.credential_ref } : {}),
    };
    onSubmit({
      provider: { display_name: v.display_name, status: v.provider_status },
      endpointID: isNew ? null : selectedId,
      endpoint,
    });
  });

  return (
    <Dialog open={open} onClose={close} title="编辑接入" wide
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={close} disabled={isSubmitting}>取消</Button>
          <Button variant="secondary" size="sm" icon="plug" loading={probe.isPending}
            disabled={isSubmitting} onClick={onTest}
            title="用当前表单参数真实试调一次（不落库、不写账本、不记密钥）">
            模型测试
          </Button>
          <Button variant="primary" size="sm" loading={busy || isSubmitting}
            onClick={() => void submit()}>保存</Button>
        </>
      }>
      <form onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <div className="banner banner--neutral" style={{ marginBottom: 16 }} role="note">
          <Icon name="info" size={16} />
          <span>
            供应商标识 <span className="mono">{provider?.provider_id}</span> 是主键，创建后不可更改。
            密钥字段<strong>留空即不改动</strong>；填入新值会替换为新的加密凭据，界面只回显打码值。
          </span>
        </div>
        {error ? (
          <div className="banner banner--warning" role="alert">
            {(() => { const d = describeError(error); return `${d.title}：${d.message}`; })()}
          </div>
        ) : null}
        {probeResult ? (
          <div
            className={probeResult.ok ? 'banner banner--success' : 'banner banner--warning'}
            style={{ marginBottom: 16 }}
            role="status">
            <Icon name={probeResult.ok ? 'check' : 'alertTriangle'} size={16} />
            <span>
              <strong>{probeResult.ok ? '模型测试通过' : '模型测试未通过'}</strong>
              {'：'}
              {probeResult.message}
              {probeResult.ok ? `（耗时 ${probeResult.latency_ms} ms）` : null}
              {!probeResult.ok && probeResult.status_code
                ? `（HTTP ${probeResult.status_code}）`
                : null}
              {!probeResult.ok && probeResult.vendor_error_snippet ? (
                <>
                  <br />
                  <span className="text-xs muted">端点回显：{probeResult.vendor_error_snippet}</span>
                </>
              ) : null}
              {probeResult.ok && probeResult.preview ? (
                <>
                  <br />
                  <span className="text-xs muted">模型回显：{probeResult.preview}</span>
                </>
              ) : null}
            </span>
          </div>
        ) : null}
        <div className="form-grid">
          <Field label="供应商显示名" htmlFor="ea-name" required error={errors.display_name?.message}>
            <Input id="ea-name" invalid={!!errors.display_name} {...register('display_name')} />
          </Field>
          <Field label="供应商状态" htmlFor="ea-pstatus" required error={errors.provider_status?.message}>
            <Select id="ea-pstatus"
              options={[
                { value: 'ACTIVE', label: 'ACTIVE —— 启用，端点可参与路由' },
                { value: 'DISABLED', label: 'DISABLED —— 停用，整个供应商不参与路由' },
              ]}
              {...register('provider_status')} />
          </Field>

          <Field label="端点" htmlFor="ea-select" required
            hint={isNew ? '当前为「新建端点」：保存后会为该供应商创建新端点。' : undefined}>
            <Select id="ea-select"
              value={selectedId}
              options={[
                ...endpoints.map((e) => ({
                  value: e.endpoint_id,
                  label: e.endpoint_name,
                })),
                { value: NEW_ENDPOINT, label: '＋ 新建端点' },
              ]}
              onChange={(ev) => setSelectedId(ev.target.value)} />
          </Field>

          <Field label="端点名" htmlFor="ea-ename" required error={errors.endpoint_name?.message}>
            <Input id="ea-ename" invalid={!!errors.endpoint_name} {...register('endpoint_name')} />
          </Field>
          <Field label="端点状态" htmlFor="ea-estatus" required error={errors.endpoint_status?.message}>
            <Select id="ea-estatus"
              options={[
                { value: 'ACTIVE', label: 'ACTIVE' },
                { value: 'DISABLED', label: 'DISABLED' },
              ]}
              {...register('endpoint_status')} />
          </Field>
          <Field label="区域（可选）" htmlFor="ea-region" error={errors.region_code?.message}>
            <Input id="ea-region" placeholder="例如：cn-north-1" {...register('region_code')} />
          </Field>

          <Field label="端点地址 base_url" htmlFor="ea-url" required error={errors.base_url?.message}>
            <Input id="ea-url" placeholder="https://api.example.com/v1"
              invalid={!!errors.base_url} {...register('base_url')} />
          </Field>
          <Field
            label="新密钥（留空即不改动）"
            htmlFor="ea-apikey"
            required={requireCredential}
            hint={selected
              ? `当前凭据：${credentialLabel(selected)}。填入新值将替换并加密存储；不填则保持原凭据。`
              : '新建端点必须提供凭据：填密钥（加密存储）或环境变量引用名。'}
            error={errors.api_key?.message}
          >
            <Input id="ea-apikey" type="password" autoComplete="off" placeholder="sk-..."
              invalid={!!errors.api_key} {...register('api_key')} />
          </Field>
          <Field label="环境变量引用名（可选）" htmlFor="ea-cred" error={errors.credential_ref?.message}>
            <Input id="ea-cred" placeholder="ODAP_LLM_API_KEY" {...register('credential_ref')} />
          </Field>
        </div>

        <div style={{ marginTop: 16 }}>
          <Field
            label="模型名（model_id，可选）"
            htmlFor="ea-probe-model"
            hint="仅用于上方「模型测试」，不会随保存写入端点。填入真实模型名时服务端会调用一次聊天补全验证该模型可用（消耗极小额度）；留空则只验证地址与鉴权（GET /models）。">
            <Input
              id="ea-probe-model"
              placeholder="例如：deepseek-chat"
              value={probeModelId}
              onChange={(e) => setProbeModelId(e.target.value)}
            />
          </Field>
        </div>
        {endpoints.length > 1 && selected ? (
          <p className="text-xs muted" style={{ marginTop: 8 }}>
            该供应商共有 {endpoints.length} 个端点；此处编辑的是所选的「{selected.endpoint_name}」。
          </p>
        ) : null}
      </form>

      {!isNew && selected ? (
        <ModelRevisionsSection endpoint={selected} suggestedModelId={probeModelId.trim()} />
      ) : null}
    </Dialog>
  );
}

/**
 * 「模型版本登记」区块：列出该端点已登记的模型版本，并可登记/删除。
 *
 * 为什么必须登记：端点在 Model Registry 装配时，只有 llm.model_revisions 里存在
 * 一条 ACTIVE 记录、且其 capability_digest 指向一条能力档案，才会参与路由。
 * 二者缺一，端点在装配期被排除（原因"该端点尚未登记任何模型版本"），
 * 自动生成类端点会一直以「模型网关未装配」fail-closed。
 *
 * 登记与删除都是即时生效的独立写操作（不走外层「保存」按钮）：它们写的是
 * 模型版本表，与端点的地址/凭据无关，混在一个提交里会让"保存失败"的语义含糊。
 */
function ModelRevisionsSection({
  endpoint, suggestedModelId,
}: { endpoint: ModelEndpointSummary; suggestedModelId: string }) {
  const list = useModelRevisions(endpoint.endpoint_id);
  const register = useRegisterModelRevision(endpoint.endpoint_id);
  const remove = useDeleteModelRevision(endpoint.endpoint_id);

  const [modelId, setModelId] = useState('');
  const [revisionLabel, setRevisionLabel] = useState('');
  const [capabilityRevision, setCapabilityRevision] = useState('');
  const [verified, setVerified] = useState<'UNVERIFIED' | 'VERIFIED'>('UNVERIFIED');
  const [nativeSchema, setNativeSchema] = useState<'false' | 'true'>('false');
  const [supportsSeed, setSupportsSeed] = useState<'false' | 'true'>('false');
  const [supportsTools, setSupportsTools] = useState<'false' | 'true'>('true');
  const [supportsEmbeddings, setSupportsEmbeddings] = useState<'false' | 'true'>('false');
  const [maxTokens, setMaxTokens] = useState('65536');
  const [feedback, setFeedback] = useState<{ ok: boolean; text: string } | null>(null);

  const revisions = list.data?.models ?? [];

  const submit = () => {
    setFeedback(null);
    const tokens = Number(maxTokens);
    if (modelId.trim() === '') {
      setFeedback({ ok: false, text: '请填写 model_id（模型标识，例如 deepseek-chat）。' });
      return;
    }
    if (capabilityRevision.trim() === '') {
      setFeedback({ ok: false, text: '请填写能力修订标识（capability_revision）。' });
      return;
    }
    if (!Number.isInteger(tokens) || tokens <= 0) {
      setFeedback({ ok: false, text: 'max_context_tokens 必须为正整数，否则该模型会在装配期被排除。' });
      return;
    }
    const capabilities: ModelCapabilities = {
      native_schema: nativeSchema === 'true',
      supports_seed: supportsSeed === 'true',
      supports_tools: supportsTools === 'true',
      embeddings: supportsEmbeddings === 'true',
      max_context_tokens: tokens,
    };
    register.mutate(
      {
        model_id: modelId.trim(),
        revision_label: revisionLabel.trim(),
        capability_revision: capabilityRevision.trim(),
        capability_verified: verified === 'VERIFIED',
        capabilities,
      },
      {
        onSuccess: () => {
          setFeedback({ ok: true, text: `已登记模型版本 ${modelId.trim()}，该模型现在可参与路由。` });
          setModelId('');
          setRevisionLabel('');
          setCapabilityRevision('');
        },
        onError: (e) => {
          const d = describeError(e);
          setFeedback({ ok: false, text: `${d.title}：${d.message}` });
        },
      },
    );
  };

  return (
    <section style={{ marginTop: 24, borderTop: '1px solid var(--border-subtle, #e5e7eb)', paddingTop: 16 }}>
      <h3 style={{ margin: 0, fontSize: 14 }}>模型版本登记</h3>
      <p className="text-xs muted" style={{ marginTop: 4 }}>
        端点只有在登记了模型版本后才会参与路由。未登记时自动生成类功能会提示「模型网关未装配」。
        {suggestedModelId ? ` 上方填写的模型名「${suggestedModelId}」可直接用于登记。` : null}
      </p>

      {feedback ? (
        <div className={feedback.ok ? 'banner banner--success' : 'banner banner--warning'}
          style={{ marginTop: 12 }} role="status">
          <Icon name={feedback.ok ? 'check' : 'alertTriangle'} size={16} />
          <span>{feedback.text}</span>
        </div>
      ) : null}

      {/* 已登记列表 */}
      {list.isLoading ? (
        <p className="text-xs muted" style={{ marginTop: 12 }}>正在读取已登记的模型版本…</p>
      ) : list.isError ? (
        <div className="banner banner--warning" style={{ marginTop: 12 }} role="alert">
          <Icon name="alertTriangle" size={16} />
          <span>{(() => { const d = describeError(list.error); return `${d.title}：${d.message}`; })()}</span>
        </div>
      ) : revisions.length === 0 ? (
        <div className="banner banner--warning" style={{ marginTop: 12 }} role="note">
          <Icon name="alertTriangle" size={16} />
          <span>该端点尚未登记任何模型版本，目前不参与路由。</span>
        </div>
      ) : (
        <div style={{ marginTop: 12 }}>
          {revisions.map((r) => (
            <div key={r.model_revision_id} className="row" style={{
              display: 'flex', alignItems: 'center', gap: 8,
              padding: '6px 0', borderBottom: '1px solid var(--border-subtle, #eee)',
            }}>
              <span className="mono" style={{ fontWeight: 500 }}>{r.model_id}</span>
              {r.revision_label ? <span className="text-xs muted">{r.revision_label}</span> : null}
              <Badge tone={r.capability_status === 'VERIFIED' ? 'success' : 'warning'}
                title="能力声明是否经过验证；未验证仍参与路由，但装配诊断会留痕">
                {r.capability_status === 'VERIFIED' ? '能力已验证' : '能力未验证'}
              </Badge>
              <span className="text-xs muted mono" title="能力档案摘要（内容寻址主键）">
                {r.capability_digest.slice(0, 12)}…
              </span>
              <span style={{ marginLeft: 'auto' }}>
                <Button variant="danger" size="sm" loading={remove.isPending}
                  title="删除该模型版本登记；若已产生用量账本将拒绝删除"
                  onClick={() => {
                    setFeedback(null);
                    remove.mutate(r.model_revision_id, {
                      onSuccess: () => setFeedback({ ok: true, text: `已删除模型版本 ${r.model_id}。` }),
                      onError: (e) => {
                        const d = describeError(e);
                        setFeedback({ ok: false, text: `${d.title}：${d.message}` });
                      },
                    });
                  }}>
                  <Icon name="x" size={14} /> 删除
                </Button>
              </span>
            </div>
          ))}
        </div>
      )}

      {/* 登记表单 */}
      <div className="form-grid" style={{ marginTop: 16 }}>
        <Field label="模型标识 model_id" htmlFor={`ea-mr-model-${endpoint.endpoint_id}`} required>
          <Input id={`ea-mr-model-${endpoint.endpoint_id}`} placeholder="例如：deepseek-chat"
            value={modelId} onChange={(e) => setModelId(e.target.value)} />
        </Field>
        <Field label="版本修订标识（可选）" htmlFor={`ea-mr-rev-${endpoint.endpoint_id}`}>
          <Input id={`ea-mr-rev-${endpoint.endpoint_id}`} placeholder="例如：2026-01"
            value={revisionLabel} onChange={(e) => setRevisionLabel(e.target.value)} />
        </Field>
        <Field label="能力修订标识 capability_revision" htmlFor={`ea-mr-caprev-${endpoint.endpoint_id}`}
          required hint="能力档案的修订标识（会随能力档案一起落库）。">
          <Input id={`ea-mr-caprev-${endpoint.endpoint_id}`} placeholder="例如：probe-2026-01"
            value={capabilityRevision} onChange={(e) => setCapabilityRevision(e.target.value)} />
        </Field>
        <Field label="能力验证状态" htmlFor={`ea-mr-verified-${endpoint.endpoint_id}`}>
          <Select id={`ea-mr-verified-${endpoint.endpoint_id}`}
            value={verified}
            options={[
              { value: 'UNVERIFIED', label: '未验证（仍参与路由，装配诊断留痕）' },
              { value: 'VERIFIED', label: '已验证（记录验证时间）' },
            ]}
            onChange={(e) => setVerified(e.target.value as 'UNVERIFIED' | 'VERIFIED')} />
        </Field>
        <Field label="原生 Schema 能力" htmlFor={`ea-mr-native-${endpoint.endpoint_id}`}
          hint="端点是否原生支持严格 Schema 约束生成。不确定时选「不支持」，服务端会走本地校验回路。">
          <Select id={`ea-mr-native-${endpoint.endpoint_id}`}
            value={nativeSchema}
            options={[{ value: 'false', label: '不支持' }, { value: 'true', label: '支持' }]}
            onChange={(e) => setNativeSchema(e.target.value as 'false' | 'true')} />
        </Field>
        <Field label="确定性 seed 能力" htmlFor={`ea-mr-seed-${endpoint.endpoint_id}`}
          hint="仅在请求带 seed 时作为硬约束。">
          <Select id={`ea-mr-seed-${endpoint.endpoint_id}`}
            value={supportsSeed}
            options={[{ value: 'false', label: '不支持' }, { value: 'true', label: '支持' }]}
            onChange={(e) => setSupportsSeed(e.target.value as 'false' | 'true')} />
        </Field>
        <Field label="工具调用能力" htmlFor={`ea-mr-tools-${endpoint.endpoint_id}`}
          hint="仅在请求带工具时作为硬约束。">
          <Select id={`ea-mr-tools-${endpoint.endpoint_id}`}
            value={supportsTools}
            options={[{ value: 'true', label: '支持' }, { value: 'false', label: '不支持' }]}
            onChange={(e) => setSupportsTools(e.target.value as 'false' | 'true')} />
        </Field>
        <Field label="向量嵌入能力" htmlFor={`ea-mr-emb-${endpoint.endpoint_id}`}
          hint="知识库文档嵌入（kb.ingest）必需；仅当该模型提供 /embeddings 接口时声明，未声明的模型不会参与嵌入路由。">
          <Select id={`ea-mr-emb-${endpoint.endpoint_id}`}
            value={supportsEmbeddings}
            options={[{ value: 'false', label: '不支持' }, { value: 'true', label: '支持' }]}
            onChange={(e) => setSupportsEmbeddings(e.target.value as 'false' | 'true')} />
        </Field>
        <Field label="最大上下文 tokens" htmlFor={`ea-mr-ctx-${endpoint.endpoint_id}`} required
          hint="必须为正整数，否则该模型会在装配期被排除。">
          <Input id={`ea-mr-ctx-${endpoint.endpoint_id}`} inputMode="numeric"
            value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} />
        </Field>
      </div>
      <div style={{ marginTop: 12 }}>
        <Button variant="secondary" size="sm" icon="plus" loading={register.isPending}
          onClick={submit}
          title="写入能力档案与模型版本，登记后该模型即可参与路由">
          登记模型版本
        </Button>
      </div>
    </section>
  );
}

/** 表单初值：供应商元数据 + 所选端点（密钥框留空）。 */
function endpointDefaults(
  provider: ModelProviderSummary | null,
  endpoint: ModelEndpointSummary | null,
): EditAccessFormValues {
  return {
    display_name: provider?.display_name ?? '',
    provider_status: (provider?.status as 'ACTIVE' | 'DISABLED') ?? 'ACTIVE',
    endpoint_select: endpoint?.endpoint_id ?? NEW_ENDPOINT,
    endpoint_name: endpoint?.endpoint_name ?? '',
    deployment_type: (endpoint?.deployment_type as 'HTTP') ?? 'HTTP',
    region_code: endpoint?.region_code ?? '',
    base_url: endpoint?.base_url ?? '',
    credential_ref: '',
    api_key: '',
    endpoint_status: (endpoint?.status as 'ACTIVE' | 'DISABLED') ?? 'ACTIVE',
  };
}

/** 凭据打码标签：只展示打码值 + 来源，永不展示明文。 */
function credentialLabel(e: ModelEndpointSummary): string {
  switch (e.credential_source) {
    case 'CIPHERTEXT':
      return `${e.credential_masked || '已加密'}（加密存储）`;
    case 'ENV_REF':
      return '环境变量引用名（内容不显示）';
    default:
      return '未配置';
  }
}
