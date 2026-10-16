// Package knowledge 实现企业知识问答的知识库摄取与检索支撑：
// 文档分块、PDF 文本抽取、批量嵌入、本体约束下的知识抽取回路与摄取任务调度。
package knowledge

import (
	"strings"
	"unicode/utf8"
)

// Chunk 是一个分块结果：Index 为文档内 0 起始的序号，Content 为分块正文。
type Chunk struct {
	Index   int    `json:"index"`
	Content string `json:"content"`
}

const (
	// chunkMinChars 是分块目标下限（字符数，按 rune 计，中文友好）。
	chunkMinChars = 500
	// chunkMaxChars 是分块目标上限；单段超长时按窗口硬切。
	chunkMaxChars = 800
	// chunkOverlapChars 是相邻分块的重叠目标（约 100 字符）。
	chunkOverlapChars = 100
	// hardSplitWindow 是超长段落硬切的窗口长度与步进重叠。
	hardSplitWindow = 600
)

// ChunkText 将文档纯文本按类型分块：
//   - md 按标题（# 行）切成小节，小节内再按空行分段；
//   - docx/xlsx 按解析器给定的段落顺序（doc.go 的 Text 以空行/换行分隔段落）；
//   - pdf/txt 按空行分段。
//
// 目标块长 500-800 字符，相邻块重叠约 100 字符。纯函数，无 IO，可单测。
// 文本无有效内容（裁剪后为空）时返回空切片。
func ChunkText(kind, text string) []Chunk {
	units := unitsFor(kind, text)
	if len(units) == 0 {
		return []Chunk{}
	}
	return packUnits(units)
}

// unitsFor 按文档类型产出"段落单元"列表（每个单元已裁剪、非空、顺序保持）。
func unitsFor(kind, text string) []string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	var sections []string
	if kind == "md" || kind == "markdown" {
		sections = splitMarkdownSections(text)
	} else {
		// docx/xlsx 的 Text 段落以空行分隔；pdf/txt 同理按空行分段。
		sections = []string{text}
	}
	units := make([]string, 0, len(sections))
	for _, sec := range sections {
		for _, p := range splitParagraphs(sec) {
			if p != "" {
				units = append(units, p)
			}
		}
	}
	return units
}

// splitMarkdownSections 按标题行切分 markdown（标题行归属其小节）。
func splitMarkdownSections(text string) []string {
	lines := strings.Split(text, "\n")
	sections := make([]string, 0, 8)
	cur := make([]string, 0, 32)
	for _, line := range lines {
		if isMarkdownHeading(line) && len(cur) > 0 {
			sections = append(sections, strings.Join(cur, "\n"))
			cur = cur[:0]
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		sections = append(sections, strings.Join(cur, "\n"))
	}
	return sections
}

// isMarkdownHeading 判定一行是否为 ATX 标题（# ~ ######，允许前导空白）。
func isMarkdownHeading(line string) bool {
	t := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(t, "#") {
		return false
	}
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	// "# 标题"：# 后必须是空白或行尾，才是标题（避免命中 C 代码注释等）。
	return n <= 6 && (n == len(t) || t[n] == ' ' || t[n] == '\t')
}

// splitParagraphs 把一段文本按空行拆成段落；无空行时整段作为一个单元。
func splitParagraphs(text string) []string {
	parts := strings.Split(text, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// packUnits 把段落单元贪心打包成分块：加入下一单元会超上限且当前已达下限时封块；
// 超长单元先硬切（窗口 hardSplitWindow、重叠 chunkOverlapChars）。
// 相邻块之间携带前一块尾部 ≤chunkOverlapChars 的重叠内容。
func packUnits(units []string) []Chunk {
	// 1) 超长单元硬切（保持顺序）。
	flat := make([]string, 0, len(units))
	for _, u := range units {
		if utf8.RuneCountInString(u) <= chunkMaxChars {
			flat = append(flat, u)
			continue
		}
		flat = append(flat, hardSplit(u)...)
	}
	// 2) 贪心打包。
	chunks := make([]Chunk, 0, len(flat)/2+1)
	var cur []string
	curLen := 0
	flush := func(next []string) {
		if len(cur) == 0 {
			return
		}
		chunks = append(chunks, Chunk{Index: len(chunks), Content: strings.Join(cur, "\n\n")})
		// 重叠：前一块尾部 ≤100 字符作为下一块的前缀（存在后续单元时才有意义）。
		tail := tailRunes(cur[len(cur)-1], chunkOverlapChars)
		cur = nil
		curLen = 0
		if len(next) > 0 && tail != "" {
			cur = append(cur, tail)
			curLen = utf8.RuneCountInString(tail)
		}
	}
	for _, u := range flat {
		l := utf8.RuneCountInString(u)
		if curLen > 0 && curLen+l > chunkMaxChars && curLen >= chunkMinChars {
			flush([]string{u})
		}
		cur = append(cur, u)
		curLen += l
	}
	flush(nil)
	return chunks
}

// hardSplit 把超长段落按固定窗口切分，相邻窗口重叠 chunkOverlapChars。
func hardSplit(s string) []string {
	runes := []rune(s)
	out := make([]string, 0, len(runes)/hardSplitWindow+1)
	for start := 0; start < len(runes); {
		end := start + hardSplitWindow
		if end > len(runes) {
			end = len(runes)
		}
		// 尽量在窗口尾部最近的空白处断开（避免拆词；找不到就硬切）。
		if end < len(runes) {
			lookback := end - 80
			if lookback < start+1 {
				lookback = start + 1
			}
			cut := -1
			for i := end - 1; i >= lookback; i-- {
				c := runes[i]
				if c == ' ' || c == '\n' || c == '\t' || c == '，' || c == '。' || c == '；' {
					cut = i + 1
					break
				}
			}
			if cut > start {
				end = cut
			}
		}
		out = append(out, strings.TrimSpace(string(runes[start:end])))
		// 步进：下一窗口从本窗口尾部回退约 100 字符开始（重叠）。
		next := end - chunkOverlapChars
		if next <= start {
			next = end
		}
		start = next
		if start >= len(runes) {
			break
		}
	}
	return out
}

// tailRunes 取字符串末尾 ≤n 个字符，并在空白处对齐（找不到空白则原样返回）。
func tailRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return strings.TrimSpace(s)
	}
	tail := r[len(r)-n:]
	// 对齐到空白之后（重叠以完整词开头）。
	for i := 0; i < len(tail) && i < n/2; i++ {
		c := tail[i]
		if c == ' ' || c == '\n' || c == '\t' {
			tail = tail[i+1:]
			break
		}
	}
	return strings.TrimSpace(string(tail))
}
