package docingest

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeZip 在 path 处写一个最小 zip，entries: 成员名 -> 内容。
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建成员 %s 失败: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("写入成员 %s 失败: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
}

func TestExtractText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, append([]byte{0xEF, 0xBB, 0xBF}, []byte("你好 world")...), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Extract(p)
	if err != nil {
		t.Fatalf("Extract 出错: %v", err)
	}
	if d.Kind != "TEXT" {
		t.Errorf("Kind = %q, want TEXT", d.Kind)
	}
	if d.Text != "你好 world" {
		t.Errorf("Text = %q, 未去除 BOM 或内容不符", d.Text)
	}
}

func TestExtractUnsupported(t *testing.T) {
	_, err := Extract("whatever.pdf")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestExtractDOCXMinimal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "min.docx")
	doc := `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>
<w:p><w:r><w:t>Hello</w:t></w:r></w:p>
<w:p><w:r><w:t>World</w:t></w:r></w:p>
<w:tbl><w:tr>
<w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc>
<w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc>
</w:tr></w:tbl>
</w:body></w:document>`
	writeZip(t, p, map[string]string{"word/document.xml": doc})

	d, err := Extract(p)
	if err != nil {
		t.Fatalf("Extract docx 出错: %v", err)
	}
	if d.Kind != "DOCX" {
		t.Errorf("Kind = %q, want DOCX", d.Kind)
	}
	want := "Hello\nWorld\nA\tB\n"
	if d.Text != want {
		t.Errorf("Text = %q, want %q", d.Text, want)
	}
}

func TestExtractDOCXMissingDocumentXML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.docx")
	writeZip(t, p, map[string]string{"word/styles.xml": "<x/>"})
	if _, err := Extract(p); err == nil {
		t.Fatal("缺少 word/document.xml 应返回 error")
	}
}

func TestExtractXLSXMinimal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "min.xlsx")
	workbook := `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
 xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="S1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	rels := `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`
	shared := `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<si><t>Name</t></si>
<si><r><t>富</t></r><r><t>文本</t></r></si>
</sst>`
	sheet := `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<sheetData>
<row r="1">
<c r="A1" t="s"><v>0</v></c>
<c r="B1" t="s"><v>1</v></c>
<c r="D1" t="inlineStr"><is><t>内联</t></is></c>
<c r="E1" t="b"><v>1</v></c>
<c r="F1"><v>3.1400</v></c>
</row>
<row r="2">
<c r="A2"><v>10.0</v></c>
<c r="B2"><f>SUM(A2:A2)</f><v>10</v></c>
</row>
</sheetData></worksheet>`
	writeZip(t, p, map[string]string{
		"xl/workbook.xml":            workbook,
		"xl/_rels/workbook.xml.rels": rels,
		"xl/sharedStrings.xml":       shared,
		"xl/worksheets/sheet1.xml":   sheet,
	})

	d, err := Extract(p)
	if err != nil {
		t.Fatalf("Extract xlsx 出错: %v", err)
	}
	if d.Kind != "XLSX" {
		t.Errorf("Kind = %q, want XLSX", d.Kind)
	}
	if len(d.Sheets) != 1 || d.Sheets[0].Name != "S1" {
		t.Fatalf("Sheets = %+v, want 单个名为 S1", d.Sheets)
	}
	rows := d.Sheets[0].Rows
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	want0 := []string{"Name", "富文本", "", "内联", "TRUE", "3.14"}
	if strings.Join(rows[0], "|") != strings.Join(want0, "|") {
		t.Errorf("row0 = %v, want %v", rows[0], want0)
	}
	want1 := []string{"10", "10"}
	if strings.Join(rows[1], "|") != strings.Join(want1, "|") {
		t.Errorf("row1 = %v, want %v", rows[1], want1)
	}
	if !strings.Contains(d.Text, "Sheet: S1") {
		t.Errorf("Text 缺少 sheet 标题: %q", d.Text)
	}
}

// TestExtractRealFiles 仅在非 short 模式且真实文件存在时运行。
func TestExtractRealFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实文件测试")
	}
	base := filepath.Join("..", "..", "docs", "资料")
	cases := []struct {
		rel      string
		wantKind string
		keywords []string
	}{
		{"合同管理制度/合同管理细则.docx", "DOCX", []string{"合同"}},
		{"费用报销制度/日常报销管理细则（2024年版）.doc", "DOC", []string{"报销", "费用", "管理"}},
		{"费用报销制度/附录2：日常费用报销单.xlsx", "XLSX", []string{"费用"}},
	}
	for _, c := range cases {
		p := filepath.Join(base, c.rel)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("真实文件不存在，跳过: %s", p)
		}
		d, err := Extract(p)
		if err != nil {
			t.Fatalf("Extract(%s) 出错: %v", c.rel, err)
		}
		if d.Kind != c.wantKind {
			t.Errorf("%s: Kind = %q, want %q", c.rel, d.Kind, c.wantKind)
		}
		if strings.TrimSpace(d.Text) == "" {
			t.Errorf("%s: 文本为空", c.rel)
		}
		for _, kw := range c.keywords {
			if !strings.Contains(d.Text, kw) {
				t.Errorf("%s: 缺少关键词 %q", c.rel, kw)
			}
		}
	}
}
