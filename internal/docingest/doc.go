package docingest

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// 常量：CFB / FAT 中的特殊标记。
const (
	cfbSig          = "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"
	sectorEndOfChan = 0xFFFFFFFE // 链结束
	sectorFree      = 0xFFFFFFFF // 空闲
)

// extractDOC 解析 Word 97-2003 二进制（OLE2/CFB），按 FIB + Clx 分片表还原正文。
func extractDOC(path string, data []byte) (*Document, error) {
	cfb, err := newCFB(data)
	if err != nil {
		return nil, err
	}
	wd, err := cfb.stream("WordDocument")
	if err != nil {
		return nil, fmt.Errorf("docingest: 缺少 WordDocument 流: %w", err)
	}
	if len(wd) < 0x200 {
		return nil, fmt.Errorf("docingest: WordDocument 流过短(%d)", len(wd))
	}

	// FIB：flags 位于偏移 0x0A，bit 9(0x0200)=fWhichTblStm -> 使用 1Table。
	flags := le16(wd, 0x0A)
	tblName := "0Table"
	if flags&0x0200 != 0 {
		tblName = "1Table"
	}
	// fcMin/fcMac：Word97 已不再权威，正文以 Clx 分片表为准（此处仅读取备用）。
	// fcClx@0x1A2、lcbClx@0x1A6。
	fcClx := le32(wd, 0x1A2)
	lcbClx := le32(wd, 0x1A6)

	// 依据 fWhichTblStm（bit 9）选表；若该标准选择解析失败则回退到另一张表。
	// 实测本机 Word97 .doc 的 flags=0x5014（bit9=0，选择 0Table）符合 MS-DOC 规范。
	preferred := tblName
	if tbl, e := cfb.stream(preferred); e == nil {
		if text, warns, e2 := docTextFromTable(wd, tbl, fcClx, lcbClx); e2 == nil {
			return &Document{Path: path, Kind: "DOC", Text: text, Warnings: warns}, nil
		}
	}
	alt := "1Table"
	if preferred == "1Table" {
		alt = "0Table"
	}
	tbl, err := cfb.stream(alt)
	if err != nil {
		return nil, fmt.Errorf("docingest: 读取 %s 表流失败: %w", preferred, err)
	}
	text, warns, err := docTextFromTable(wd, tbl, fcClx, lcbClx)
	if err != nil {
		return nil, err
	}
	// 若 fWhichTblStm 指示的表解析失败而另一张成功，说明本文件实际存储在 %s 表流中。
	return &Document{Path: path, Kind: "DOC", Text: text, Warnings: warns}, nil
}

// docTextFromTable 在指定表流中定位 Clx，解析 Pcdt 并还原全部文本分片。
func docTextFromTable(wd, tbl []byte, fcClx, lcbClx uint32) (string, []string, error) {
	if lcbClx == 0 {
		return "", nil, fmt.Errorf("docingest: lcbClx 为 0，无分片表")
	}
	end := uint64(fcClx) + uint64(lcbClx)
	if end > uint64(len(tbl)) {
		return "", nil, fmt.Errorf("docingest: Clx 区间越界(%d+%d>%d)", fcClx, lcbClx, len(tbl))
	}
	clx := tbl[fcClx:end]
	cp, pcd, n, err := parseClx(clx)
	if err != nil {
		return "", nil, err
	}
	return extractDocPieces(wd, cp, pcd, n)
}

// parseClx 解析 Clx：跳过 Prc(0x01)，定位 Pcdt(0x02) 并解析 PlcPcd。
// PlcPcd = (n+1) 个 uint32 的 CP 数组 + n 个 8 字节 PCD；n=(lcb-4)/12。
func parseClx(clx []byte) (cp []uint32, pcd []byte, n int, err error) {
	i := 0
	for i < len(clx) {
		switch clx[i] {
		case 0x01: // Prc：0x01 + 2 字节长度 + 数据
			if i+3 > len(clx) {
				return nil, nil, 0, fmt.Errorf("docingest: Clx 中 Prc 越界")
			}
			cb := int(le16(clx, i+1))
			i += 3 + cb
		case 0x02: // Pcdt：0x02 + 4 字节 lcb + PlcPcd
			if i+5 > len(clx) {
				return nil, nil, 0, fmt.Errorf("docingest: Clx 中 Pcdt 越界")
			}
			lcb := int(le32(clx, i+1))
			start := i + 5
			if lcb < 4 || start+lcb > len(clx) {
				return nil, nil, 0, fmt.Errorf("docingest: PlcPcd 长度非法(%d)", lcb)
			}
			plc := clx[start : start+lcb]
			nn := (lcb - 4) / 12
			if nn <= 0 {
				return nil, nil, 0, fmt.Errorf("docingest: PlcPcd 分片数为 0")
			}
			cpArr := make([]uint32, nn+1)
			for k := 0; k <= nn; k++ {
				cpArr[k] = le32(plc, k*4)
			}
			pcdArr := plc[(nn+1)*4 : (nn+1)*4+8*nn]
			return cpArr, pcdArr, nn, nil
		default:
			return nil, nil, 0, fmt.Errorf("docingest: Clx 中出现未知字节 0x%02X", clx[i])
		}
	}
	return nil, nil, 0, fmt.Errorf("docingest: Clx 中未找到 Pcdt")
}

// extractDocPieces 按分片表逐段解码文本。压缩分片(fCompressed)按 Windows-1252 解码，
// 非压缩分片按 UTF-16LE 解码。
func extractDocPieces(wd []byte, cp []uint32, pcd []byte, n int) (string, []string, error) {
	var sb strings.Builder
	var warns []string
	compressedNonASCII := false

	for i := 0; i < n; i++ {
		cch := int64(cp[i+1]) - int64(cp[i])
		if cch < 0 {
			return "", nil, fmt.Errorf("docingest: 分片 %d 的 CP 递减", i)
		}
		fcRaw := le32(pcd, i*8+2)

		if fcRaw&0x40000000 != 0 { // fCompressed：1 字节/字符，真实 fc = (fc&0x3FFFFFFF)/2
			fc := int64(fcRaw&0x3FFFFFFF) / 2
			if fc+cch > int64(len(wd)) {
				return "", nil, fmt.Errorf("docingest: 压缩分片 %d 越界", i)
			}
			raw := wd[fc : fc+cch]
			for _, b := range raw {
				if b > 0x7F {
					compressedNonASCII = true
					break
				}
			}
			for _, b := range raw {
				writeDocRune(&sb, cp1252Decode(b))
			}
		} else { // UTF-16LE：2 字节/字符
			fc := int64(fcRaw)
			if fc+cch*2 > int64(len(wd)) {
				return "", nil, fmt.Errorf("docingest: 分片 %d 越界", i)
			}
			raw := wd[fc : fc+cch*2]
			u := make([]uint16, cch)
			for k := int64(0); k < cch; k++ {
				u[k] = binary.LittleEndian.Uint16(raw[2*k : 2*k+2])
			}
			for _, r := range utf16.Decode(u) {
				writeDocRune(&sb, r)
			}
		}
	}

	if compressedNonASCII {
		warns = append(warns, "doc: 压缩分片含非 ASCII 字节，按 Windows-1252 解码，可能无法精确还原本地编码（如 GBK）")
	}
	return sb.String(), warns, nil
}

// writeDocRune 规范化控制符：\r -> \n，单元格符 0x07 -> \t，其余 <0x20 控制符丢弃。
func writeDocRune(sb *strings.Builder, r rune) {
	switch {
	case r == '\r':
		sb.WriteByte('\n')
	case r == 0x07:
		sb.WriteByte('\t')
	case r == '\n' || r == '\t':
		sb.WriteRune(r)
	case r >= 0x20:
		sb.WriteRune(r)
	}
}

// cp1252Decode 把单个字节按 Windows-1252 映射为 Unicode 码点。
func cp1252Decode(b byte) rune {
	switch {
	case b < 0x80:
		return rune(b)
	case b < 0xA0:
		return cp1252High[b-0x80]
	default:
		return rune(b) // Latin-1(0xA0-0xFF) 与 Unicode 同码点
	}
}

// cp1252High 是 0x80-0x9F 的 Windows-1252 映射表。
var cp1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// ---------------------------------------------------------------------------
// 最小 CFB(OLE2) 读取器
// ---------------------------------------------------------------------------

type cfb struct {
	data       []byte
	sectorSize int
	fat        []uint32
	entries    []dirEntry
}

type dirEntry struct {
	name  string
	typ   byte
	start uint32
	size  uint64
}

func newCFB(data []byte) (*cfb, error) {
	if len(data) < 512 || string(data[:8]) != cfbSig {
		return nil, fmt.Errorf("docingest: 非 OLE2/CFB 文件")
	}
	sshift := le16(data, 0x1E)
	ss := 1 << sshift
	if ss != 512 && ss != 4096 {
		return nil, fmt.Errorf("docingest: 非法扇区大小 2^%d", sshift)
	}
	c := &cfb{data: data, sectorSize: ss}

	csectFat := le32(data, 0x2C)
	csectDif := le32(data, 0x48)

	// DIFAT：头部 109 项 + 后续 DIFAT 扇区链。
	var fatSectors []uint32
	seen := make(map[uint32]bool)
	for i := 0; i < 109; i++ {
		v := le32(data, 0x4C+4*i)
		if v != sectorFree && !seen[v] {
			seen[v] = true
			fatSectors = append(fatSectors, v)
		}
	}
	next := le32(data, 0x44)
	per := ss / 4
	for d := uint32(0); d < csectDif && next != sectorEndOfChan && next != sectorFree; d++ {
		sec, err := c.sector(next)
		if err != nil {
			return nil, err
		}
		for i := 0; i < per-1; i++ {
			v := le32(sec, i*4)
			if v != sectorFree && !seen[v] {
				seen[v] = true
				fatSectors = append(fatSectors, v)
			}
		}
		next = le32(sec, (per-1)*4)
	}
	if len(fatSectors) == 0 {
		return nil, fmt.Errorf("docingest: DIFAT 为空，无法读取 FAT")
	}
	if uint32(len(fatSectors)) < csectFat {
		// 容错：仍以实际收集到的为准。
		_ = csectFat
	}

	// 组装 FAT。
	fat := make([]uint32, 0, len(fatSectors)*per)
	for _, fs := range fatSectors {
		sec, err := c.sector(fs)
		if err != nil {
			return nil, err
		}
		for i := 0; i < per; i++ {
			fat = append(fat, le32(sec, i*4))
		}
	}
	c.fat = fat

	// 目录流。
	dirStart := le32(data, 0x30)
	dir, err := c.chain(dirStart)
	if err != nil {
		return nil, fmt.Errorf("docingest: 读取目录流失败: %w", err)
	}
	for off := 0; off+128 <= len(dir); off += 128 {
		e := dir[off : off+128]
		if e[0x42] == 0 {
			continue
		}
		nameLen := int(le16(e, 0x40))
		if nameLen > 64 {
			nameLen = 64
		}
		if nameLen >= 2 {
			nameLen -= 2 // 去掉结尾 NUL
		}
		c.entries = append(c.entries, dirEntry{
			name:  utf16LEString(e[:nameLen]),
			typ:   e[0x42],
			start: le32(e, 0x74),
			size:  le64(e, 0x78),
		})
	}
	return c, nil
}

// sector 返回扇区 sid 的原始数据（扇区起始偏移 = (sid+1)*sectorSize）。
// 若文件末尾扇区未按扇区大小对齐，则返回现有字节（部分读取）。
func (c *cfb) sector(sid uint32) ([]byte, error) {
	off := (int64(sid) + 1) * int64(c.sectorSize)
	if off < 0 || off >= int64(len(c.data)) {
		return nil, fmt.Errorf("docingest: 扇区 %d 越界", sid)
	}
	end := off + int64(c.sectorSize)
	if end > int64(len(c.data)) {
		end = int64(len(c.data))
	}
	return c.data[off:end], nil
}

// chain 沿 FAT 链读取从 start 起的全部扇区数据。
func (c *cfb) chain(start uint32) ([]byte, error) {
	var out []byte
	sid := start
	for i := 0; i <= len(c.fat); i++ {
		if sid == sectorEndOfChan || sid == sectorFree {
			return out, nil
		}
		if sid >= uint32(len(c.fat)) {
			return nil, fmt.Errorf("docingest: FAT 链越界(扇区 %d)", sid)
		}
		sec, err := c.sector(sid)
		if err != nil {
			return nil, err
		}
		out = append(out, sec...)
		sid = c.fat[sid]
	}
	return nil, fmt.Errorf("docingest: FAT 链存在环")
}

// stream 按名读取常规 FAT 流。位于 mini stream 的流暂不支持，返回明确 error。
func (c *cfb) stream(name string) ([]byte, error) {
	for _, e := range c.entries {
		if e.name != name || e.typ != 2 { // 2 = stream
			continue
		}
		if e.size == 0 {
			return nil, nil
		}
		if e.size < 4096 {
			return nil, fmt.Errorf("docingest: 流 %q 位于 mini stream，暂不支持", name)
		}
		b, err := c.chain(e.start)
		if err != nil {
			return nil, err
		}
		if uint64(len(b)) > e.size {
			b = b[:e.size]
		}
		return b, nil
	}
	return nil, fmt.Errorf("docingest: 未找到流 %q", name)
}

// ---------------------------------------------------------------------------
// 小端读取与 UTF-16LE 解码
// ---------------------------------------------------------------------------

func le16(b []byte, off int) uint16 { return binary.LittleEndian.Uint16(b[off : off+2]) }
func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off : off+4]) }
func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off : off+8]) }

func utf16LEString(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i : 2*i+2])
	}
	return string(utf16.Decode(u))
}
