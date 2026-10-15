package handlers

import (
	"crypto/sha256"
	"encoding/hex"
)

// digestHex 返回 SHA-256 十六进制小写摘要（输入摘要与幂等引用使用）。
func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
