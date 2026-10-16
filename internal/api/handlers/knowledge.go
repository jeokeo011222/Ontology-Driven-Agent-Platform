package handlers

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"odap/internal/api/httperr"
	"odap/internal/api/middleware"
	"odap/internal/api/sse"
	"odap/internal/api/store"
	"odap/internal/docingest"
	"odap/internal/knowledge"
	"odap/internal/llm"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

// ---------------------------------------------------------------------------
// Knowledge — 企业知识问答文档管理（五端点）
//
// POST /api/v1/knowledge/documents     上传（multipart）→ 同步解析分块落库 → 建 EMBED/EXTRACT 任务
// GET  /api/v1/knowledge/documents     列表（keyset 分页 + ?q=）
// GET  /api/v1/knowledge/documents/{id} 详情（含分块预览与任务）
// POST /api/v1/knowledge/jobs/{id}/retry 重试失败任务
// DELETE /api/v1/knowledge/documents/{id} 软删文档（分块/任务物理删）
//
// 路由由 router.go 装配；本文件只承载处理逻辑。
// ---------------------------------------------------------------------------

// kbMaxUploadBytes 是知识文档上传上限（50MB，与生成端点的 32MiB 语义分开）。
const kbMaxUploadBytes = 50 << 20

// kbAllowedExts 是允许上传的扩展名白名单（pdf/docx/xlsx/md/txt）。
var kbAllowedExts = map[string]string{
	".pdf": "pdf", ".docx": "docx", ".xlsx": "xlsx", ".md": "md", ".txt": "txt",
}

// CreateKnowledgeDocument 处理知识文档上传：
// 白名单校验 → sha256 去重 → 同步解析 + 分块落库 → 创建 EMBED/EXTRACT 任务。
// 未指明 target_ontology_version_id 时只创建 EMBED 任务，并在响应 explain 说明。
func (h *Handler) CreateKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// fail-closed：嵌入模型未配置就不创建任何任务（否则 EMBED 必然失败）。
	if strings.TrimSpace(h.deps.KBEmbeddingModel) == "" {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument,
			"未配置嵌入模型（ODAP_KB_EMBEDDING_MODEL），无法创建知识文档"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, kbMaxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.writeKBMultipartError(w, r, err)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	var fileHdr *multipart.FileHeader
	if r.MultipartForm != nil && len(r.MultipartForm.File["file"]) > 0 {
		fileHdr = r.MultipartForm.File["file"][0]
	}
	if fileHdr == nil {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "缺少 multipart 字段 file"))
		return
	}
	ext := strings.ToLower(filepath.Ext(fileHdr.Filename))
	kind, ok := kbAllowedExts[ext]
	if !ok {
		// 白名单外明确 415（不是 400：问题出在媒体类型，调用方可据此换格式）。
		h.writeJSON(w, http.StatusUnsupportedMediaType, httperr.Response{
			RequestID: middleware.RequestIDFromContext(r.Context()),
			Error: httperr.ErrorBody{
				Code:     "UNSUPPORTED_MEDIA_TYPE",
				BaseCode: "INVALID_ARGUMENT",
				Message: "不支持的文档类型 " + ext + "（仅支持 pdf/docx/xlsx/md/txt）；" +
					"扫描版 PDF 请转存为 docx/md 后上传",
				Retryable: false,
			},
		})
		return
	}
	if fileHdr.Size > kbMaxUploadBytes {
		h.writeKBMultipartError(w, r, errors.E(errors.CodeInvalidArgument, "文件超过 50MB 上限"))
		return
	}
	// target_ontology_version_id：EXTRACT 任务的必要条件（可缺省）。
	targetVersion := strings.TrimSpace(r.FormValue("target_ontology_version_id"))
	if targetVersion != "" {
		if _, err := ids.Parse(targetVersion); err != nil {
			h.fail(w, r, errors.E(errors.CodeInvalidArgument, "target_ontology_version_id 不是合法 UUID"))
			return
		}
	}

	// 1. 落盘（uploads 根内），保留原件供审计溯源与 PDF 解析。
	full, err := h.persistUpload(fileHdr, filepath.Base(fileHdr.Filename))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 2. 同步解析文本（pdf 无文本层在此明确失败，绝不空内容入库）。
	text, err := extractKnowledgeText(kind, full, fileHdr)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// 3. 分块并校验非空。
	chunks := knowledge.ChunkText(kind, text)
	if len(chunks) == 0 {
		h.fail(w, r, errors.E(errors.CodeInvalidArgument, "文档无可提取文本（内容为空）"))
		return
	}
	contents := make([]string, 0, len(chunks))
	for _, c := range chunks {
		contents = append(contents, c.Content)
	}
	// 4. sha256 去重 + 落库 + 建任务（单租户事务）。
	data, err := readFileHeader(fileHdr)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	doc, err := h.deps.Store.CreateDocument(r.Context(), store.CreateDocumentInput{
		Title:                   strings.TrimSuffix(filepath.Base(fileHdr.Filename), ext),
		SourcePath:              sourcePathRel(h.uploadsRoot(), full),
		FileKind:                kind,
		SizeBytes:               fileHdr.Size,
		Sha256:                  sha256Hex(data),
		UploadedBy:              id.SubjectID,
		EmbeddingModel:          h.deps.KBEmbeddingModel,
		TargetOntologyVersionID: targetVersion,
		Chunks:                  contents,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	docID, _ := doc["id"].(string)
	payload := map[string]any{"document": doc}
	if targetVersion == "" {
		payload["explain"] = "未指定 target_ontology_version_id：已创建 EMBED 任务（向量检索可用），跳过知识抽取"
	}
	h.recordAudit(r, id, "knowledge.document_created", "knowledge.document.upload", "SUCCESS",
		"kb_document", docID, ids.ID{}, map[string]any{
			"file_kind": kind, "chunk_count": len(contents), "sha256": doc["sha256"],
			"target_ontology_version_id": targetVersion,
		})
	h.publish(id.TenantID.String(), "knowledge.document_created", sse.Important, map[string]any{
		"document_id": docID, "title": doc["title"], "chunk_count": len(contents),
	})
	echoIdempotency(w, r)
	h.writeJSON(w, http.StatusCreated, payload)
}

// ListKnowledgeDocuments GET /api/v1/knowledge/documents（cursor 分页 10/页 + ?q=）。
func (h *Handler) ListKnowledgeDocuments(w http.ResponseWriter, r *http.Request) {
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	limit, err := limitParam(r, 10)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.deps.Store.ListDocuments(r.Context(), limit, r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, res)
}

// GetKnowledgeDocument GET /api/v1/knowledge/documents/{id}（含分块预览与任务）。
func (h *Handler) GetKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	if _, err := h.identity(r); err != nil {
		h.fail(w, r, err)
		return
	}
	docID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	doc, err := h.deps.Store.GetDocument(r.Context(), docID.String())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, doc)
}

// RetryKnowledgeJob POST /api/v1/knowledge/jobs/{id}/retry（仅 FAILED 可重试）。
func (h *Handler) RetryKnowledgeJob(w http.ResponseWriter, r *http.Request) {
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	jobID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	job, err := h.deps.Store.RetryJob(r.Context(), jobID.String())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	docID, _ := job["document_id"].(string)
	h.recordAudit(r, id, "knowledge.job_retried", "knowledge.job.retry", "SUCCESS",
		"kb_job", jobID.String(), ids.ID{}, map[string]any{"document_id": docID, "kind": job["kind"]})
	h.publish(id.TenantID.String(), "knowledge.job_retried", sse.Normal, map[string]any{
		"job_id": jobID.String(), "document_id": docID, "kind": job["kind"],
	})
	h.writeJSON(w, http.StatusOK, job)
}

// DeleteKnowledgeDocument DELETE /api/v1/knowledge/documents/{id}（软删，分块与任务物理删）。
func (h *Handler) DeleteKnowledgeDocument(w http.ResponseWriter, r *http.Request) {
	if h.deps.Store == nil {
		h.fail(w, r, unavailable("持久化适配器未装配"))
		return
	}
	id, err := h.identity(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	docID, err := h.pathID(r, "id")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.deps.Store.DeleteDocument(r.Context(), docID.String()); err != nil {
		h.fail(w, r, err)
		return
	}
	h.recordAudit(r, id, "knowledge.document_deleted", "knowledge.document.delete", "SUCCESS",
		"kb_document", docID.String(), ids.ID{}, nil)
	h.publish(id.TenantID.String(), "knowledge.document_deleted", sse.Normal, map[string]any{
		"document_id": docID.String(),
	})
	h.writeJSON(w, http.StatusNoContent, map[string]any{})
}

// ---------------------------------------------------------------------------
// knowledge-retrieval 工具执行器（effect_class=READ）
// ---------------------------------------------------------------------------

// knowledgeEmbeddingClient 是向量检索所需的嵌入能力最小面。Deps.LLM 是
// LLMCompleter 接口（只含 Complete），运行期断言探测 *llm.Gateway 的
// Embed 能力；探测失败时向量腿降级跳过（词项腿仍可用），不阻断检索。
type knowledgeEmbeddingClient interface {
	Embed(ctx context.Context, tenantID ids.ID, req llm.EmbeddingRequest) (llm.EmbeddingResponse, error)
}

// execKnowledgeRetrievalTool 混合检索：
//   - 向量腿：pgvector 余弦 topK（点名模型 + 已回填向量过滤）；
//   - 词项腿：trigram 相似度 + 子串命中 topK；
//   - RRF（k=60）融合去重；两腿皆空返回空集（不报错）。
//
// 输出 hits：{document_id, doc_title, source_path, chunk_index, content, score, rrf}。
func (h *Handler) execKnowledgeRetrievalTool(ctx context.Context, id tenantctx.Identity, args map[string]any) (map[string]any, error) {
	if h.deps.Store == nil {
		return nil, unavailable("持久化适配器未装配（knowledge-retrieval 需要知识库存储）")
	}
	query := strArg(args, "query")
	if query == "" {
		return nil, errors.E(errors.CodeInvalidArgument, "缺少必填参数 query")
	}
	topK := 5
	if v, ok := args["top_k"].(float64); ok && v >= 1 {
		topK = int(v)
	}
	if topK > 20 {
		topK = 20
	}
	docIDs := strSliceArg(args, "document_ids")
	warnings := []string{}

	// 向量腿：模型未配置 / 嵌入能力未装配 / 查询向量失败 → 降级为词项检索。
	var vectorHits []store.KnowledgeHit
	model := strings.TrimSpace(h.deps.KBEmbeddingModel)
	switch {
	case model == "":
		warnings = append(warnings, "未配置嵌入模型（ODAP_KB_EMBEDDING_MODEL），向量腿跳过，仅词项检索")
	default:
		ec, ok := h.deps.LLM.(knowledgeEmbeddingClient)
		if !ok || ec == nil {
			warnings = append(warnings, "LLM 网关未提供 Embed 能力，向量腿跳过，仅词项检索")
		} else {
			resp, err := ec.Embed(ctx, id.TenantID, llm.EmbeddingRequest{
				TenantID: id.TenantID,
				ModelID:  model,
				Texts:    []string{query},
				// 幂等键按查询内容去重：同一问题短时间内重复检索不重复计费。
				IdempotencyKey: "kb-retrieval:" + sha256Hex([]byte(query))[:16],
			})
			switch {
			case err != nil:
				warnings = append(warnings, "查询向量生成失败，向量腿跳过："+err.Error())
			case resp.Dimensions != knowledge.RequiredEmbeddingDims:
				warnings = append(warnings, fmt.Sprintf(
					"查询向量维度 %d 与库内 vector(%d) 不一致，向量腿跳过", resp.Dimensions, knowledge.RequiredEmbeddingDims))
			default:
				vectorHits, err = h.deps.Store.SearchKnowledgeVector(ctx, model,
					knowledge.VectorLiteral(resp.Vectors[0]), topK, docIDs)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	// 词项腿：按查询分词做 trigram/子串打分。
	termHits, err := h.deps.Store.SearchKnowledgeTerms(ctx, tokenize(query), topK, docIDs)
	if err != nil {
		return nil, err
	}

	// RRF 融合（k=60）：score(d) = Σ 1/(k+rank)；按 (document_id, chunk_index) 去重。
	const rrfK = 60.0
	fused := map[string]*store.KnowledgeHit{}
	rrf := map[string]float64{}
	addLeg := func(hits []store.KnowledgeHit) {
		for rank, hit := range hits {
			key := hit.DocumentID + "#" + strconv.Itoa(hit.ChunkIndex)
			if _, ok := fused[key]; !ok {
				cp := hit
				fused[key] = &cp
			}
			rrf[key] += 1.0 / (rrfK + float64(rank+1))
		}
	}
	addLeg(vectorHits)
	addLeg(termHits)
	// 排序：RRF 降序，同分保持稳定（按文档+分块序）。
	keys := make([]string, 0, len(fused))
	for k := range fused {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if rrf[keys[i]] != rrf[keys[j]] {
			return rrf[keys[i]] > rrf[keys[j]]
		}
		a, b := fused[keys[i]], fused[keys[j]]
		if a.DocumentID != b.DocumentID {
			return a.DocumentID < b.DocumentID
		}
		return a.ChunkIndex < b.ChunkIndex
	})
	if len(keys) > topK {
		keys = keys[:topK]
	}
	hits := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		hit := fused[k]
		score := hit.Score
		hits = append(hits, map[string]any{
			"document_id": hit.DocumentID,
			"doc_title":   hit.DocTitle,
			"source_path": hit.SourcePath,
			"chunk_index": hit.ChunkIndex,
			"content":     hit.Content,
			"score":       score,
			"rrf":         rrf[k],
		})
	}
	return map[string]any{
		"query":       query,
		"top_k":       topK,
		"hits":        hits,
		"vector_hits": len(vectorHits),
		"term_hits":   len(termHits),
		"warnings":    warnings,
	}, nil
}

// writeKBMultipartError 把 multipart 解析错误转换为明确响应：
// 超过请求体上限（http.MaxBytesError）→ 413；其余 → 400。
func (h *Handler) writeKBMultipartError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	if stderrors.As(err, &maxErr) {
		h.writeJSON(w, http.StatusRequestEntityTooLarge, httperr.Response{
			RequestID: middleware.RequestIDFromContext(r.Context()),
			Error: httperr.ErrorBody{
				Code:      "PAYLOAD_TOO_LARGE",
				BaseCode:  "INVALID_ARGUMENT",
				Message:   "上传体积超过 50MB 上限",
				Retryable: false,
			},
		})
		return
	}
	h.fail(w, r, errors.E(errors.CodeInvalidArgument, "multipart 解析失败："+err.Error()))
}

// extractKnowledgeText 按扩展名解析出纯文本（pdf 走 knowledge 包的文本型抽取）。
func extractKnowledgeText(kind, full string, fh *multipart.FileHeader) (string, error) {
	switch kind {
	case "pdf":
		return knowledge.ExtractPDFText(full)
	case "docx", "xlsx":
		doc, err := docingest.Extract(full)
		if err != nil {
			return "", errors.Wrap(err, errors.CodeInvalidArgument, "文档解析失败")
		}
		return doc.Text, nil
	default: // md/txt：直接读上传内容（UTF-8 文本）。
		data, err := readFileHeader(fh)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

// readFileHeader 读取 multipart 文件内容（上限内；超出直接报错）。
func readFileHeader(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, "上传文件不可读")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, kbMaxUploadBytes+1))
	if err != nil {
		return nil, errors.E(errors.CodeInvalidArgument, "上传文件读取失败")
	}
	if len(data) > kbMaxUploadBytes {
		return nil, errors.E(errors.CodeInvalidArgument, "文件超过 50MB 上限")
	}
	return data, nil
}

// sourcePathRel 计算上传文件相对 uploads 根的路径（documents.source_path 仅审计溯源用）。
func sourcePathRel(root, full string) string {
	if absRoot, err := filepath.Abs(root); err == nil {
		if rel, err := filepath.Rel(absRoot, full); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(filepath.Base(full))
}
