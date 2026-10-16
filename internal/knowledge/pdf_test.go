package knowledge

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
)

// buildPDF 用字节模板手工构造一个最小合法 PDF（文本型，n 页，每页若干行）。
// 仅覆盖本包测试所需子集：Catalog/Pages/Page/Font/Content stream + 精确 xref。
func buildPDF(pages [][]string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	writeObj := func(num int, body string) {
		offsets[num] = b.Len()
		b.WriteString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", num, body))
	}
	n := len(pages)
	fontObj := 3 + n

	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+i))
	}
	writeObj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n))

	for i, lines := range pages {
		pageObj := 3 + i
		contentObj := 4 + n + i
		var cs strings.Builder
		y := 720
		for _, ln := range lines {
			// 单行文本操作：设置字体、定位、显示文本。
			cs.WriteString(fmt.Sprintf("BT /F1 12 Tf 72 %d Td (%s) Tj ET\n", y, ln))
			y -= 20
		}
		stream := cs.String()
		writeObj(pageObj, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			fontObj, contentObj))
		writeObj(contentObj, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	writeObj(fontObj, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	maxObjNum := 3 + 2*n
	xrefStart := b.Len()
	b.WriteString(fmt.Sprintf("xref\n0 %d\n", maxObjNum+1))
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= maxObjNum; i++ {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	b.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		maxObjNum+1, xrefStart))
	return b.Bytes()
}

func TestExtractPDFTextSinglePage(t *testing.T) {
	data := buildPDF([][]string{{"Hello Knowledge Base", "Odap Chunker"}})
	text, err := extractPDFBytes(data)
	if err != nil {
		t.Fatalf("抽取失败：%v", err)
	}
	if !strings.Contains(text, "Hello Knowledge Base") || !strings.Contains(text, "Odap Chunker") {
		t.Fatalf("抽取文本不完整：%q", text)
	}
}

func TestExtractPDFTextMultiPage(t *testing.T) {
	data := buildPDF([][]string{
		{"first page text"},
		{"second page text"},
	})
	text, err := extractPDFBytes(data)
	if err != nil {
		t.Fatalf("抽取失败：%v", err)
	}
	if !strings.Contains(text, "first page text") || !strings.Contains(text, "second page text") {
		t.Fatalf("多页抽取不完整：%q", text)
	}
	// 页间以空行分隔（分块器按段落切分的依据）。
	if !strings.Contains(text, "\n\n") {
		t.Fatalf("页之间缺少分隔：%q", text)
	}
}

func TestExtractPDFNoTextLayer(t *testing.T) {
	// "扫描件"：合法 PDF 但内容流没有任何文本操作 → 必须明确报错，
	// 绝不返回空内容当成功。
	data := buildPDF([][]string{{""}})
	// 单行内容为空串时仍有一条 BT/ET（无 Tj 文本）→ 视作无文本层。
	text, err := extractPDFBytes(data)
	if err == nil {
		t.Fatalf("无文本层 PDF 应报错，实际返回文本 %q", text)
	}
	if !stderrors.Is(err, ErrPDFNoTextLayer) {
		t.Fatalf("应返回 ErrPDFNoTextLayer 哨兵，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "无文本层") {
		t.Fatalf("错误消息应包含「无文本层」：%v", err)
	}
}

func TestExtractPDFMalformed(t *testing.T) {
	if _, err := extractPDFBytes([]byte("this is not a pdf")); err == nil {
		t.Fatal("非 PDF 输入必须报错")
	}
}

func TestChunkTextFromPDFExtraction(t *testing.T) {
	// 集成：抽取文本 → 分块器（pdf 走段落路径）。
	data := buildPDF([][]string{{
		strings.Repeat("The ontology defines expense policy constraints. ", 30),
	}})
	text, err := extractPDFBytes(data)
	if err != nil {
		t.Fatalf("抽取失败：%v", err)
	}
	chunks := ChunkText("pdf", text)
	if len(chunks) == 0 || chunks[0].Content == "" {
		t.Fatal("PDF 文本分块不应为空")
	}
}
