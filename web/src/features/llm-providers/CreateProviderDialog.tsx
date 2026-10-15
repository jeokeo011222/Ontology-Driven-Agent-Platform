import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Button, Dialog, Field, Input, Select } from '@/components/ui';
import { Icon } from '@/components/icon';
import { describeError } from '@/api/errors';
import { useCreateModelProvider, useProbeModelProvider, type CreateModelProviderInput, type ProbeResult } from './api';
import { createModelProviderSchema, type CreateModelProviderFormValues } from './schema';

/**
 * 新建模型供应商（含首个端点）—— 契约 CreateModelProviderRequest。
 *
 * 凭据安全：本窗口提供**两条互斥的凭据来源** ——
 *   ① 「API 密钥」明文输入框（**只入不出**）：提交后由服务端 AES-256-GCM 加密落库，
 *      响应与审计绝不回显；列表只显示打码值。
 *   ② 「环境变量引用名」：秘密仍留在服务端进程环境里。
 * 窗口附「测试连接」：用**当前表单参数**真实试调一次（不落库、不写账本、不记密钥）。
 */
type FormValues = CreateModelProviderFormValues;

export interface CreateProviderDialogProps {
  open: boolean;
  onClose: () => void;
  /** 创建成功后回调（页面据此关闭对话框并展示成功提示） */
  onCreated: (providerId: string) => void;
}

export function CreateProviderDialog({ open, onClose, onCreated }: CreateProviderDialogProps) {
  const create = useCreateModelProvider();
  const probe = useProbeModelProvider();
  const [formError, setFormError] = useState<string | null>(null);
  const [probeResult, setProbeResult] = useState<ProbeResult | null>(null);
  const {
    register,
    handleSubmit,
    getValues,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(createModelProviderSchema),
    defaultValues: {
      display_name: '',
      provider_id: '',
      provider_type: 'OPENAI_COMPATIBLE',
      endpoint_name: '',
      deployment_type: 'HTTP',
      region_code: '',
      base_url: '',
      credential_ref: '',
      api_key: '',
    },
  });

  const close = () => {
    reset();
    setFormError(null);
    setProbeResult(null);
    create.reset();
    probe.reset();
    onClose();
  };

  /** 「测试连接」：用当前表单参数真实试调一次；结果以内联 banner 呈现，不阻塞录入。 */
  const onTest = () => {
    setProbeResult(null);
    const v = getValues();
    probe.mutate(
      {
        deployment_type: v.deployment_type,
        ...(v.base_url ? { base_url: v.base_url } : {}),
        ...(v.credential_ref ? { credential_ref: v.credential_ref } : {}),
        ...(v.api_key ? { api_key: v.api_key } : {}),
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

  const onSubmit = handleSubmit(async (values) => {
    setFormError(null);
    const payload: CreateModelProviderInput = {
      display_name: values.display_name,
      provider_type: values.provider_type,
      endpoint: {
        endpoint_name: values.endpoint_name,
        deployment_type: values.deployment_type,
        ...(values.region_code ? { region_code: values.region_code } : {}),
        ...(values.base_url ? { base_url: values.base_url } : {}),
        ...(values.credential_ref ? { credential_ref: values.credential_ref } : {}),
        ...(values.api_key ? { api_key: values.api_key } : {}),
      },
    };
    if (values.provider_id) payload.provider_id = values.provider_id;
    try {
      const res = await create.mutateAsync(payload);
      reset();
      setProbeResult(null);
      onCreated(res.provider_id);
    } catch (e) {
      // 服务端错误（409 provider_id 已存在 / 400 参数不合法 …）按统一呈现层展示，
      // 不在这里自行编造文案。
      const d = describeError(e);
      setFormError(`${d.title}：${d.message}（${d.guidance}）`);
    }
  });

  return (
    <Dialog
      open={open}
      onClose={close}
      title="新建模型供应商"
      wide
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={close} disabled={isSubmitting}>
            取消
          </Button>
          <Button
            variant="secondary"
            size="sm"
            icon="plug"
            loading={probe.isPending}
            disabled={isSubmitting}
            onClick={onTest}
            title="用当前表单参数真实试调一次（不落库、不写账本）"
          >
            测试连接
          </Button>
          <Button
            variant="primary"
            size="sm"
            icon="plus"
            loading={isSubmitting}
            onClick={() => void onSubmit()}
          >
            创建
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void onSubmit();
        }}
      >
        <div className="banner banner--neutral" style={{ marginBottom: 16 }} role="note">
          <span className="banner__icon" aria-hidden="true">
            <Icon name="info" size={16} />
          </span>
          <span>
            供应商与<strong>首个端点</strong>会在同一次调用中创建：地址与凭据都在端点上，
            只建供应商会得到一个无法被调用的空壳。凭据二选一：填 <strong>API 密钥</strong>
            （服务端加密存储，界面只回显打码值），或填<strong>环境变量引用名</strong>
            （如 <span className="mono">ODAP_LLM_API_KEY</span>）。
          </span>
        </div>

        {formError ? (
          <div className="banner banner--warning" style={{ marginBottom: 16 }} role="alert">
            <span className="banner__icon" aria-hidden="true">
              <Icon name="alertTriangle" size={16} />
            </span>
            <span>{formError}</span>
          </div>
        ) : null}

        {probeResult ? (
          <div
            className={probeResult.ok ? 'banner banner--success' : 'banner banner--warning'}
            style={{ marginBottom: 16 }}
            role="status"
          >
            <span className="banner__icon" aria-hidden="true">
              <Icon name={probeResult.ok ? 'check' : 'alertTriangle'} size={16} />
            </span>
            <span>
              <strong>{probeResult.ok ? '连接成功' : '连接失败'}</strong>
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
          <Field label="供应商显示名" htmlFor="mp-display-name" required error={errors.display_name?.message}>
            <Input
              id="mp-display-name"
              placeholder="例如：Acme 推理服务"
              invalid={!!errors.display_name}
              {...register('display_name')}
            />
          </Field>

          <Field
            label="供应商标识（可选）"
            htmlFor="mp-provider-id"
            hint="留空则由显示名派生（仅小写字母/数字/连字符）。显示名含中文等非 ASCII 字符时无法派生，须在此显式填写，如 acme-llm。它是主键，重复会返回 409。"
            error={errors.provider_id?.message}
          >
            <Input id="mp-provider-id" placeholder="例如：acme-llm" {...register('provider_id')} />
          </Field>

          <Field label="供应商类型" htmlFor="mp-provider-type" required error={errors.provider_type?.message}>
            <Select
              id="mp-provider-type"
              options={[{ value: 'OPENAI_COMPATIBLE', label: 'OPENAI_COMPATIBLE —— OpenAI 兼容接口' }]}
              {...register('provider_type')}
            />
          </Field>

          <Field label="端点名" htmlFor="mp-endpoint-name" required error={errors.endpoint_name?.message}>
            <Input
              id="mp-endpoint-name"
              placeholder="例如：acme-default"
              invalid={!!errors.endpoint_name}
              {...register('endpoint_name')}
            />
          </Field>

          <Field label="区域（可选）" htmlFor="mp-region" error={errors.region_code?.message}>
            <Input id="mp-region" placeholder="例如：cn-north-1" {...register('region_code')} />
          </Field>

          <Field
            label="端点地址 base_url"
            htmlFor="mp-base-url"
            required
            hint="HTTP 端点必须可寻址；缺失时装配期会直接拒绝该端点。"
            error={errors.base_url?.message}
          >
            <Input
              id="mp-base-url"
              placeholder="https://api.example.com/v1"
              invalid={!!errors.base_url}
              {...register('base_url')}
            />
          </Field>

          <Field
            label="API 密钥"
            htmlFor="mp-api-key"
            hint="只入不出：服务端以 AES-256-GCM 加密存储，界面与审计永不回显明文。与下项「环境变量引用名」二选一。"
            error={errors.api_key?.message}
          >
            <Input
              id="mp-api-key"
              type="password"
              autoComplete="off"
              placeholder="sk-..."
              invalid={!!errors.api_key}
              {...register('api_key')}
            />
          </Field>

          <Field
            label="环境变量引用名（可选）"
            htmlFor="mp-credential-ref"
            hint="秘密存储中的变量名，不是密钥本身。填了它就不要填上面的密钥。"
            error={errors.credential_ref?.message}
          >
            <Input
              id="mp-credential-ref"
              placeholder="ODAP_LLM_API_KEY"
              invalid={!!errors.credential_ref}
              {...register('credential_ref')}
            />
          </Field>
        </div>
      </form>
    </Dialog>
  );
}
