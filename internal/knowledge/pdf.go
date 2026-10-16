package knowledge

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"

	"odap/internal/platform/errors"
)

// ErrPDFNoTextLayer 是"PDF 无文本层（疑似扫描件）"的哨兵错误。
// fail-closed：扫描件绝不能以"空内容成功"的形式入库（那会让嵌入与抽取
// 全部静默空转），必须让用户转存为可解析格式后重传。
var ErrPDFNoTextLayer = errors.E(errors.CodeInvalidArgument,
	"PDF 无文本层（疑似扫描件），请转存为 docx/md 后上传")

// ExtractPDFText 从磁盘上的 PDF 文件抽取纯文本（仅支持文本型 PDF）。
// 无文本层（疑似扫描件）返回 ErrPDFNoTextLayer；文件损坏/加密返回明确错误。
func ExtractPDFText(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", errors.Wrap(err, errors.CodeInvalidArgument, "PDF 打开失败（文件可能损坏或已加密）")
	}
	defer f.Close()
	return extractPDFText(r)
}

// extractPDFText 从已打开的 PDF Reader 逐页抽取文本并拼接（页间以空行分隔）。
// 单独抽出以便测试直接用内存中的最小 PDF fixture（无需落盘）。
func extractPDFText(r *pdf.Reader) (string, error) {
	if r == nil {
		return "", errors.E(errors.CodeInvalidArgument, "PDF 解析失败：Reader 为空")
	}
	n := r.NumPage()
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		pageText, err := r.Page(i).GetPlainText(nil)
		if err != nil {
			return "", errors.Wrap(err, errors.CodeInvalidArgument,
				fmt.Sprintf("PDF 第 %d 页文本抽取失败", i))
		}
		b.WriteString(pageText)
		if i < n {
			b.WriteString("\n\n")
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		// 明确失败：没有文本层绝不当作成功（调用方据此向用户报错）。
		return "", ErrPDFNoTextLayer
	}
	return text, nil
}

// extractPDFBytes 供测试与内部调用：从内存字节抽取 PDF 文本。
func extractPDFBytes(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.Wrap(err, errors.CodeInvalidArgument, "PDF 解析失败（文件可能损坏或已加密）")
	}
	return extractPDFText(r)
}
