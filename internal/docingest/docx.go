package docingest

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// extractDOCX 解压 docx 并抽取 word/document.xml 正文。
func extractDOCX(path string, data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("docingest: 打开 docx(zip) 失败: %w", err)
	}
	raw, ok, err := readZipFile(zr, "word/document.xml")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("docingest: docx 缺少 word/document.xml")
	}
	text, err := parseDocxDocument(raw)
	if err != nil {
		return nil, err
	}
	return &Document{Path: path, Kind: "DOCX", Text: text}, nil
}

// readZipFile 从 zip 中读取指定名字文件的全部内容。
func readZipFile(zr *zip.Reader, name string) ([]byte, bool, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false, fmt.Errorf("docingest: 读取 zip 成员 %q 失败: %w", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return nil, false, fmt.Errorf("docingest: 读取 zip 成员 %q 失败: %w", name, err)
		}
		return b, true, nil
	}
	return nil, false, nil
}

// parseDocxDocument 用流式 token 遍历 document.xml。
// 段落 <w:p> 以 \n 结尾；表格行 <w:tr> 内单元格 <w:tc> 之间用 \t；
// <w:tab/> -> \t，<w:br/> -> \n。
func parseDocxDocument(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	type frame struct {
		kind  string
		buf   strings.Builder
		cells []string
	}
	var stack []*frame
	top := func() *frame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	pop := func() *frame {
		f := top()
		if f != nil {
			stack = stack[:len(stack)-1]
		}
		return f
	}
	push := func(kind string) { stack = append(stack, &frame{kind: kind}) }
	writeTop := func(s string) {
		if f := top(); f != nil {
			f.buf.WriteString(s)
		}
	}

	var bodyText string
	inT := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("docingest: 解析 docx XML 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "body":
				push("body")
			case "p":
				push("p")
			case "tbl":
				push("tbl")
			case "tr":
				push("row")
			case "tc":
				push("cell")
			case "t":
				inT = true
			case "tab":
				writeTop("\t")
			case "br", "cr":
				writeTop("\n")
			}
		case xml.CharData:
			if inT {
				writeTop(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if f := pop(); f != nil {
					writeTop(f.buf.String() + "\n")
				}
			case "tc":
				if f := pop(); f != nil {
					cell := strings.TrimRight(f.buf.String(), "\n")
					if parent := top(); parent != nil {
						parent.cells = append(parent.cells, cell)
					}
				}
			case "tr":
				if f := pop(); f != nil {
					writeTop(strings.Join(f.cells, "\t") + "\n")
				}
			case "tbl":
				if f := pop(); f != nil {
					writeTop(f.buf.String())
				}
			case "body":
				if f := pop(); f != nil {
					bodyText = f.buf.String()
				}
			}
		}
	}
	return bodyText, nil
}
