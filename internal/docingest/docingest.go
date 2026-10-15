// Package docingest 提供自包含的本地制度文档纯文本抽取能力。
//
// 仅依赖 Go 标准库，不引用项目内其它 internal 包。支持 docx / xlsx /
// doc(Word 97-2003 OLE2) / txt / md / csv / json。
package docingest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupported 表示文件类型不受支持，可用 errors.Is 判定。
var ErrUnsupported = errors.New("docingest: 不支持的文件类型")

// Document 是一次抽取的结果。
type Document struct {
	Path     string
	Kind     string   // "DOCX" | "XLSX" | "DOC" | "TEXT"
	Text     string   // 全文纯文本；XLSX 时是各 sheet 文本按顺序拼接（含 sheet 名标题行）
	Sheets   []Sheet  // 仅 XLSX 非空
	Warnings []string // 非致命问题
}

// Sheet 表示 XLSX 中的一个工作表。
type Sheet struct {
	Name string
	Rows [][]string // 每行各单元格值；共享字符串已解析，数字/布尔原样，公式取缓存值
}

// Extract 从本地文件路径抽取纯文本，依据扩展名分派。
// 不支持的类型直接返回 ErrUnsupported（不读取文件）。
func Extract(path string) (*Document, error) {
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".docx":
		return readAnd(path, extractDOCX)
	case ".xlsx":
		return readAnd(path, extractXLSX)
	case ".doc":
		return readAnd(path, extractDOC)
	case ".txt", ".md", ".csv", ".json":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("docingest: 读取文件失败: %w", err)
		}
		return extractText(path, data), nil
	default:
		return nil, ErrUnsupported
	}
}

// readAnd 读取文件后交给指定的抽取函数。
func readAnd(path string, fn func(string, []byte) (*Document, error)) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("docingest: 读取文件失败: %w", err)
	}
	return fn(path, data)
}

// extractText 直接按 UTF-8 读取纯文本，去除 UTF-8 BOM。
func extractText(path string, data []byte) *Document {
	return &Document{Path: path, Kind: "TEXT", Text: string(stripBOM(data))}
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
