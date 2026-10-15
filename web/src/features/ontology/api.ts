import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiGet, apiPatch, apiPost, apiUpload, fetchList, listFilterQuery, type ListQueryFilters } from '@/api/client';
import { queryKeys } from '@/api/query-keys';
import { newIdempotencyKey } from '@/lib/http';
import type { EnvName } from '@/hooks/useSession';
import type {
  OntologyArtifacts,
  OntologyDetail,
  OntologyPublishResult,
  OntologySummary,
  OntologyValidationReport,
} from '@/types';

/**
 * 本体工作台（§6）。契约：`/api/v1/ontologies`（contracts/03 §2）
 *
 * `validate` 与 `publish` 都是**同步 POST**：后端在一次请求内完成校验（装载 →
 * 反假绿探针 → SHACL → 落库 → 状态流转）或发布门禁判定，并以 200 返回结论。
 * 它们**不返回 202 + 任务引用**，因此这里不能声明为 `AsyncTask`——那样页面会去
 * 轮询一个并不存在的任务号，并把已经确定的结论显示成"进行中"。
 */

export function useOntologies(filters: ListQueryFilters = {}) {
  return useQuery({
    queryKey: [...queryKeys.ontologies.list(), filters],
    queryFn: () => fetchList<OntologySummary>('/ontologies', listFilterQuery(filters)),
  });
}

/**
 * 详情。
 *
 * ⚠️ `version` 目前**只进入 Query key，不进入请求**：`GET /api/v1/ontologies/{id}`
 *   没有版本参数，恒返回最新版本（`Store.GetOntology`）。保留该参数是为了让缓存
 *   按版本分桶，但**不要**据此认为页面展示的是某个历史版本。发布同理，目标是
 *   最新版本（`Store.PublishOntology` 的 UPDATE 也硬约束到最新一条）。
 */
export function useOntology(id: string | undefined, version?: string) {
  return useQuery({
    queryKey: queryKeys.ontologies.detail(id ?? '', version),
    queryFn: () => apiGet<OntologyDetail>(`/ontologies/${encodeURIComponent(String(id))}`),
    enabled: Boolean(id),
  });
}

/** `PATCH /api/v1/ontologies/{id}` 的请求体：元数据 + 本体级启停状态。 */
export interface UpdateOntologyBody {
  name?: string;
  description?: string;
  /** 启停状态：ACTIVE=启用、INACTIVE=停用；省略则不改动。 */
  status?: 'ACTIVE' | 'INACTIVE';
}

/** 更新入参：id 随请求变量传入，便于列表页对任意行调用同一个 mutation。 */
export type UpdateOntologyVars = UpdateOntologyBody & { id: string };

/**
 * 更新本体元数据与启停状态（`PATCH /api/v1/ontologies/{id}`）。
 *
 * 停用只影响本体级 status，**不改变版本生命周期**；停用后本体仍留在列表中
 * （便于重新启用），但不再用于新的运行。服务端对非法 status 直接 400 拒绝。
 */
export function useUpdateOntology() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: UpdateOntologyVars) =>
      apiPatch<OntologySummary>(`/ontologies/${encodeURIComponent(id)}`, body, {
        idempotencyKey: newIdempotencyKey(`onto-update-${id}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.ontologies.all() });
    },
  });
}

/**
 * 执行一次真实的结构校验 + SHACL 验证（§6.3）。
 *
 * 返回**同步结论**。两种情形服务端返回 422 而不是空洞的"通过"，必须原样展示
 * 给用户（它们是诚实性的一部分，不是错误页面）：
 *   - 本体未定义任何 shapes —— 没有形状就没有可执行的约束；
 *   - 图中没有属于目标类的实例 —— SHACL 会返回 conforms=true，但那只是
 *     "没数据可查"（反假绿探针）。
 * 未装配图存储时返回 503，说明校验无法给出结论。
 */
export function useValidateOntology(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      apiPost<OntologyValidationReport>(`/ontologies/${encodeURIComponent(id)}/validate`, undefined, {
        idempotencyKey: newIdempotencyKey(`onto-validate-${id}`),
      }),
    onSuccess: () => {
      // 校验会改变版本状态并写入发现项：列表与详情都必须失效，否则页面会
      // 继续显示校验前的 latest_version_status 与空发现项。
      void queryClient.invalidateQueries({ queryKey: queryKeys.ontologies.all() });
    },
  });
}

/**
 * 本体产物：RDF（Turtle）/ OWL（RDF/XML）/ SHACL（Turtle）三份文本。
 *
 * 服务端由 manifest 派生，**不要求先跑校验**，因此 DRAFT 版本同样可见，也不会
 * 因为"没校验过"而失败。`enabled` 由抽屉开关驱动：抽屉没打开时不应为一个
 * 可能只看一眼的内容发请求。
 */
export function useOntologyArtifacts(id: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.ontologies.artifacts(id ?? ''),
    queryFn: () =>
      apiGet<OntologyArtifacts>(`/ontologies/${encodeURIComponent(String(id))}/artifacts`),
    enabled: Boolean(id) && enabled,
  });
}

/** `POST /api/v1/ontologies/{id}/publish` 的请求体（契约：openapi 该端点）。 */
export interface PublishOntologyBody {
  /** 指定要发布的版本号；缺省发布最新版本。 */
  version?: string;
  /**
   * 目标环境。**必须由调用方显式传入**：省略时后端默认 DEVELOPMENT，
   * 于是在生产环境点"发布"会静默发到开发环境。
   */
  environment?: 'DEVELOPMENT' | 'STAGING' | 'PRODUCTION';
  /**
   * 发布说明。
   *
   * 服务端把它写入审计记录（不可变留痕），当前**没有**可回读的列，
   * 因此不从 `GET /ontologies/{id}` 回显。这里显式保留该字段是为了
   * 不让一个"必填"的输入被静默丢弃。
   */
  release_notes?: string;
}

/** 发布目标环境的契约枚举。 */
export type PublishEnvironment = NonNullable<PublishOntologyBody['environment']>;

/**
 * 顶部栏环境标识（dev/staging/prod）→ 发布目标环境。
 *
 * 写成 `Record<EnvName, …>` 而不是带 `default` 的 switch：后者在将来新增环境值时会
 * **静默**落到默认分支，把一个"未知环境"的发布悄悄改写成 DEVELOPMENT。全键映射下
 * 新增 `EnvName` 会直接编译失败——这正是我们要的失败方向。
 */
export const PUBLISH_ENVIRONMENT: Record<EnvName, PublishEnvironment> = {
  dev: 'DEVELOPMENT',
  staging: 'STAGING',
  prod: 'PRODUCTION',
};

/**
 * 发布：必须绑定不可变版本；不得静默改变运行中任务的语义上下文（§6.3）。
 *
 * 返回**同步结果**。发布门禁只放行 status ∈ {VALID, APPROVED} 的最新版本，
 * 其余返回 409 —— 页面应把它解释为"尚未通过校验"，而不是泛化的失败。
 */
export function usePublishOntology(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: PublishOntologyBody) =>
      apiPost<OntologyPublishResult>(`/ontologies/${encodeURIComponent(id)}/publish`, body, {
        idempotencyKey: newIdempotencyKey(`onto-publish-${id}`),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.ontologies.all() });
    },
  });
}

/** `POST /api/v1/ontologies/generate` 的请求体（contracts/openapi.yaml）。 */
export interface GenerateOntologyBody {
  /** 本地制度文档（.docx/.doc/.xlsx/.md）。与 source_path **二选一**。 */
  source_file?: File;
  /** 相对制度文档根目录（默认 docs/资料）的文件或目录路径；与 source_file 二选一。 */
  source_path?: string;
  ontology_key?: string;
  name?: string;
  /** 业务领域；由所选的业务领域分类派生（category_code），不允许手工录入。 */
  domain?: string;
  granularity?: string;
  description?: string;
  category_id?: string;
  model_id?: string;
  max_source_chars?: number;
}

/** 生成结果：落库后的本体对象 + 生成过程元数据（源文件、字符数、模型修订）。 */
export interface GenerateOntologyResult {
  id: string;
  ontology_key: string;
  name?: string;
  status?: string;
  generation?: {
    source_path: string;
    source_files: { path: string; kind: string; chars: number }[];
    source_chars: number;
    truncated: boolean;
    warnings?: string[];
    model?: Record<string, unknown>;
  };
}

/**
 * 由制度文档自动生成本体（大模型 → DRAFT）。
 *
 * 两种来源二选一：上传本地文件（multipart）或服务端路径（JSON）。
 * 服务端只有在装配了**真实 HTTP 模型端点**时才会执行；未装配返回 503 UNAVAILABLE
 * 并说明原因，文档不可解析返回 404，模型输出不满足结构约束返回 422。
 * 前端不做任何本地兜底或假生成（§6 语义资产治理）。
 */
export function useGenerateOntology() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: GenerateOntologyBody) => {
      if (body.source_file) {
        const file = body.source_file;
        const form = new FormData();
        form.append('file', file);
        if (body.ontology_key) form.append('ontology_key', body.ontology_key);
        if (body.name) form.append('name', body.name);
        if (body.domain) form.append('domain', body.domain);
        if (body.granularity) form.append('granularity', body.granularity);
        if (body.description) form.append('description', body.description);
        if (body.category_id) form.append('category_id', body.category_id);
        if (body.model_id) form.append('model_id', body.model_id);
        if (body.max_source_chars) form.append('max_source_chars', String(body.max_source_chars));
        // 幂等键由文件名+大小+修改时间派生：内容相同的同一份文件重复提交时，
        // 得到稳定的键（无法读取内容摘要时，这是最贴近"同一份输入"的可用标识）。
        return apiUpload<GenerateOntologyResult>('/ontologies/generate', form, {
          idempotencyKey: newIdempotencyKey(`onto-generate-${file.name}-${file.size}-${file.lastModified}`),
        });
      }
      return apiPost<GenerateOntologyResult>('/ontologies/generate', body, {
        idempotencyKey: newIdempotencyKey(`onto-generate-${body.source_path ?? ''}`),
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.ontologies.all() });
    },
  });
}
