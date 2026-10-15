// Package ids 提供平台内部统一的标识符类型。
// 标识符采用 UUID v4 格式（与第二篇 DDL 的 UUID 主键对齐），
// 以定长数组承载，避免字符串拼接产生新的分配。
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// ID 是一个 16 字节 UUID（version 4, variant RFC 4122）。
type ID [16]byte

// New 生成一个随机 UUID v4。
func New() ID {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		panic("ids: crypto/rand unavailable: " + err.Error())
	}
	id[6] = (id[6] & 0x0f) | 0x40 // version 4
	id[8] = (id[8] & 0x3f) | 0x80 // variant 10
	return id
}

// Parse 解析标准 UUID 字符串（含连字符的 8-4-4-4-12 形式）。
func Parse(s string) (ID, error) {
	var id ID
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return id, fmt.Errorf("ids: invalid uuid length %d", len(s))
	}
	if _, err := hex.Decode(id[:], []byte(clean)); err != nil {
		return id, fmt.Errorf("ids: decode: %w", err)
	}
	return id, nil
}

// MustParse 与 Parse 相同，失败时 panic（仅限常量与测试使用）。
func MustParse(s string) ID {
	id, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}

// String 返回标准 UUID 字符串形式（8-4-4-4-12）。
func (id ID) String() string {
	var buf [36]byte
	hex.Encode(buf[0:8], id[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], id[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], id[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], id[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], id[10:16])
	return string(buf[:])
}

// IsZero 报告是否为全零标识符（未设置）。
func (id ID) IsZero() bool {
	return id == ID{}
}
