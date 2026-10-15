package docingest

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// extractXLSX 解析 xlsx：共享字符串 + workbook 名称/rId + rels 映射 + 各 worksheet。
func extractXLSX(path string, data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("docingest: 打开 xlsx(zip) 失败: %w", err)
	}
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("docingest: 读取 zip 成员 %q 失败: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("docingest: 读取 zip 成员 %q 失败: %w", f.Name, err)
		}
		files[f.Name] = b
	}

	shared, err := parseSharedStrings(files["xl/sharedStrings.xml"])
	if err != nil {
		return nil, err
	}
	refs, err := parseWorkbook(files["xl/workbook.xml"])
	if err != nil {
		return nil, err
	}
	rels, err := parseWorkbookRels(files["xl/_rels/workbook.xml.rels"])
	if err != nil {
		return nil, err
	}

	doc := &Document{Path: path, Kind: "XLSX"}
	var sb strings.Builder
	for _, ref := range refs {
		full := rels[ref.rid]
		if full == "" {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("sheet %q 的 rId %q 未找到对应 worksheet", ref.name, ref.rid))
			continue
		}
		raw, ok := files[full]
		if !ok {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("sheet %q 对应的 worksheet %q 不存在", ref.name, full))
			continue
		}
		rows, err := parseWorksheet(raw, shared)
		if err != nil {
			return nil, fmt.Errorf("docingest: 解析 sheet %q 失败: %w", ref.name, err)
		}
		doc.Sheets = append(doc.Sheets, Sheet{Name: ref.name, Rows: rows})
		if len(rows) == 0 {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("sheet %q 为空", ref.name))
		}
		sb.WriteString("Sheet: " + ref.name + "\n")
		for _, row := range rows {
			sb.WriteString(strings.Join(row, "\t"))
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	doc.Text = sb.String()
	return doc, nil
}

// parseSharedStrings 解析 xl/sharedStrings.xml。<si> 下所有 <t> 拼接（含 <r><t> 富文本）。
// 文件不存在时返回 nil，不报错。
func parseSharedStrings(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docingest: 解析 sharedStrings.xml 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.CharData:
			if inSI && inT {
				cur.WriteString(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				inSI = false
				out = append(out, cur.String())
			}
		}
	}
	return out, nil
}

type sheetRef struct {
	name string
	rid  string
}

// parseWorkbook 解析 xl/workbook.xml 取 sheet 名与 r:id。
func parseWorkbook(data []byte) ([]sheetRef, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("docingest: xlsx 缺少 xl/workbook.xml")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []sheetRef
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docingest: 解析 workbook.xml 失败: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "sheet" {
			continue
		}
		var name, rid string
		for _, a := range se.Attr {
			switch {
			case a.Name.Local == "name":
				name = a.Value
			case a.Name.Local == "id" && strings.Contains(a.Name.Space, "relationships"):
				rid = a.Value
			}
		}
		out = append(out, sheetRef{name: name, rid: rid})
	}
	return out, nil
}

// parseWorkbookRels 解析 rId -> worksheet 实际 zip 路径。
func parseWorkbookRels(data []byte) (map[string]string, error) {
	out := make(map[string]string)
	if len(data) == 0 {
		return out, nil
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docingest: 解析 workbook.xml.rels 失败: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		var id, target string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id == "" || target == "" {
			continue
		}
		out[id] = normalizeZipPath(target)
	}
	return out, nil
}

// normalizeZipPath 把 rels 中的相对 Target 归一为 zip 内路径。
func normalizeZipPath(target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return "xl/" + target
}

// parseWorksheet 解析单个 worksheet，返回按行组织的单元格值。
func parseWorksheet(data []byte, shared []string) ([][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var rows [][]string
	var rowCells []string
	var val strings.Builder
	var cellType string
	col := 0
	inRow := false
	inV, inT := false, false

	flushCell := func() {
		text := cellValue(cellType, val.String(), shared)
		for len(rowCells) <= col {
			rowCells = append(rowCells, "")
		}
		rowCells[col] = text
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docingest: 解析 worksheet 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				inRow = true
				rowCells = nil
			case "c":
				col = 0
				cellType = ""
				val.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						col = colIndex(a.Value)
					case "t":
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.CharData:
			if inV || inT {
				val.WriteString(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				flushCell()
			case "row":
				if inRow {
					rows = append(rows, rowCells)
					inRow = false
				}
			}
		}
	}
	return rows, nil
}

// cellValue 依据单元格类型把原始取值转换为字符串。
func cellValue(t, raw string, shared []string) string {
	switch t {
	case "s": // 共享字符串索引
		idx, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || idx < 0 || idx >= len(shared) {
			return ""
		}
		return shared[idx]
	case "inlineStr": // 内联字符串
		return raw
	case "b": // 布尔
		if strings.TrimSpace(raw) == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "str", "e", "d": // 公式字符串结果 / 错误 / 日期
		return raw
	default: // 数字（t="n" 或无 t）
		return formatNumber(raw)
	}
}

// formatNumber 将数值去掉多余的小数尾零。
func formatNumber(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return raw
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// colIndex 把列字母（A,B,...,Z,AA,AB,...）换算为 0 起始列索引。
func colIndex(ref string) int {
	idx := 0
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		var v int
		switch {
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 1
		case c >= 'a' && c <= 'z':
			v = int(c-'a') + 1
		default:
			// 遇到行号数字，结束
			if idx <= 0 {
				return 0
			}
			return idx - 1
		}
		idx = idx*26 + v
	}
	if idx <= 0 {
		return 0
	}
	return idx - 1
}
