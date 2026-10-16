package knowledge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// repeatChinese 生成 n 个汉字（按 rune 计长，中文场景的分块主用例）。
func repeatChinese(n int) string {
	r := []rune("知识库分块测试内容涵盖企业本体与问答链路。")
	out := make([]rune, 0, n)
	for len(out) < n {
		out = append(out, r...)
	}
	return string(out[:n])
}

func TestChunkTextShortSingle(t *testing.T) {
	chunks := ChunkText("txt", "这是一段很短的文本。")
	if len(chunks) != 1 {
		t.Fatalf("短文本应产出 1 块，实际 %d", len(chunks))
	}
	if chunks[0].Index != 0 || chunks[0].Content != "这是一段很短的文本。" {
		t.Fatalf("分块内容不符：%+v", chunks[0])
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if got := ChunkText("txt", "   \n\n  "); len(got) != 0 {
		t.Fatalf("空白文本应产出 0 块，实际 %d", len(got))
	}
}

func TestChunkTextParagraphPacking(t *testing.T) {
	// 8 个约 200 字的段落 → 应打包成多块，块长落在目标区间附近。
	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteString(repeatChinese(200))
		b.WriteString("\n\n")
	}
	chunks := ChunkText("txt", b.String())
	if len(chunks) < 2 {
		t.Fatalf("应产出多块，实际 %d", len(chunks))
	}
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c.Content); n > 950 {
			t.Fatalf("分块 %d 长度 %d 超出上限", c.Index, n)
		}
	}
}

func TestChunkTextOverlap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString(repeatChinese(220))
		b.WriteString("\n\n")
	}
	chunks := ChunkText("txt", b.String())
	if len(chunks) < 3 {
		t.Fatalf("应产出多块，实际 %d", len(chunks))
	}
	// 相邻块重叠：下一块的开头应出现在上一块的尾部附近（约 100 字符重叠）。
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1].Content
		next := chunks[i].Content
		tail := []rune(prev)
		if len(tail) > chunkOverlapChars+40 {
			tail = tail[len(tail)-(chunkOverlapChars+40):]
		}
		if !strings.Contains(next, string(tail)) {
			t.Fatalf("分块 %d 与 %d 之间缺少重叠", i-1, i)
		}
	}
}

func TestChunkTextMarkdownHeadings(t *testing.T) {
	md := "# 总览\n\n" + repeatChinese(120) + "\n\n" +
		"# 费用政策\n\n" + repeatChinese(120) + "\n\n" +
		"## 审批流程\n\n" + repeatChinese(120)
	chunks := ChunkText("md", md)
	// 每个小节约 125 字符，打包后首块应包含第一个标题。
	if len(chunks) == 0 {
		t.Fatal("md 分块不应为空")
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content + "\n---\n"
	}
	for _, heading := range []string{"# 总览", "# 费用政策", "## 审批流程"} {
		if !strings.Contains(joined, heading) {
			t.Fatalf("标题 %q 在分块中丢失（md 按标题切分应保留标题行）", heading)
		}
	}
}

func TestChunkTextOversizedParagraphHardSplit(t *testing.T) {
	long := repeatChinese(3000)
	chunks := ChunkText("txt", long)
	if len(chunks) < 4 {
		t.Fatalf("3000 字段落应硬切为多块，实际 %d", len(chunks))
	}
	for _, c := range chunks {
		if n := utf8.RuneCountInString(c.Content); n > 800 {
			t.Fatalf("硬切后分块 %d 长度 %d 超过 800", c.Index, n)
		}
	}
	// 覆盖完整性：拼接（去掉重叠）后应覆盖全部输入——至少首块开头与末块结尾吻合。
	if !strings.HasPrefix(chunks[0].Content, string([]rune(long)[:50])) {
		t.Fatal("首块应从文本开头开始")
	}
	last := chunks[len(chunks)-1].Content
	if !strings.HasSuffix(long, string([]rune(last)[utf8.RuneCountInString(last)-30:])) {
		t.Fatal("末块应覆盖到文本结尾")
	}
}

func TestChunkTextDocumentKinds(t *testing.T) {
	// docx/xlsx/txt 走段落路径，pdf 亦然（解析在 docingest/knowledge 层），
	// md 走标题路径：两者对普通段落文本产出等价结果。
	text := strings.Repeat("段落内容一二三四五六七八九十。\n\n", 6)
	a := ChunkText("txt", text)
	b := ChunkText("docx", text)
	if len(a) != len(b) {
		t.Fatalf("txt 与 docx 分块数不一致：%d vs %d", len(a), len(b))
	}
}
