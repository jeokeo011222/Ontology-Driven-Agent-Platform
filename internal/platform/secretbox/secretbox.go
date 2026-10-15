// Package secretbox 提供平台级对称加密信封，用于把模型端点凭据以密文形式落库。
//
// 设计要点：
//
//   - 算法 AES-256-GCM：同时提供机密性（加密）与完整性（认证标签）。篡改密文必然
//     在解密时报错，不会"解出垃圾却当成有效密钥"用。
//   - 信封格式 `version(1B) || nonce(12B) || ciphertext||tag`：首字节是格式版本，
//     为将来换算法/换参数留出升级位；nonce 每次加密随机生成，绝不复用。
//   - AAD（附加认证数据）绑定 endpoint_id：把密文与其所属端点绑定，防止
//     "把 A 端点的密文搬到 B 端点"的跨行搬运——GCM 认证会直接失败。
//   - **明文绝不出现在 SQL 参数里**：加密发生在调用方（store 层），加密后的信封才是
//     唯一进入数据库的形态。PostgreSQL 会记录语句参数（log_statement /
//     pg_stat_statements / pg_stat_activity），故明文必须先于 SQL 消失。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// MasterKeyEnv 是主密钥的环境变量名（32 字节，base64 或 hex 编码）。
const MasterKeyEnv = "ODAP_SECRET_MASTER_KEY"

// Envelope 布局常量。
const (
	envelopeVersion byte = 0x01
	nonceSize            = 12 // GCM 标准 nonce 长度
	tagSize              = 16 // GCM 认证标签长度
	keySize              = 32 // AES-256
	headerSize           = 1 + nonceSize
)

// aadPrefix 是 AAD 的固定前缀，与迁移注释中的列名保持一致。
const aadPrefix = "llm.endpoints.credential_ciphertext\x00"

// Cipher 是信封加解密器。它只持有 AEAD，不含主密钥副本（aes.Cipher 内部已派生密钥）。
type Cipher struct {
	aead cipher.AEAD
}

// DecodeMasterKey 解析主密钥字符串：接受 base64（标准/RawStd/URL）或 64 位 hex。
func DecodeMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("主密钥为空")
	}
	if len(s) == keySize*2 {
		if key, err := hex.DecodeString(s); err == nil && len(key) == keySize {
			return key, nil
		}
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if key, err := enc.DecodeString(s); err == nil && len(key) == keySize {
			return key, nil
		}
	}
	return nil, fmt.Errorf("主密钥须为 %d 字节（base64 或 64 位 hex 编码）", keySize)
}

// New 用给定密钥构造 Cipher。密钥长度必须为 32 字节。
func New(key []byte) (*Cipher, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("主密钥长度须为 %d 字节，实际 %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// NewFromEnv 从环境读取主密钥并构造 Cipher。
//
// 未配置（空值）时返回 (nil, nil)：这表示"服务端没有主密钥"，由调用方
// fail-closed 处理（拒绝写入密文凭据），而不是让整个进程起不来。
func NewFromEnv(getenv func(string) string) (*Cipher, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := getenv(MasterKeyEnv)
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	key, err := DecodeMasterKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%s 配置无效: %w", MasterKeyEnv, err)
	}
	return New(key)
}

// AADFor 返回某端点密文的附加认证数据。绑定 endpoint_id 使密文不可跨端点搬运。
func AADFor(endpointID string) []byte {
	return AADForScope("llm.endpoints.credential_ciphertext", endpointID)
}

// AADForScope 返回指定列作用域下某行凭据密文的附加认证数据。
//
// 不同表的凭据密文必须使用不同作用域（如 ERP 连接用
// "integration.erp_connections.credential_ciphertext"）：跨表搬运密文时
// AAD 认证直接失败，防止"把 A 表的密文当 B 表的凭据解"。
func AADForScope(scope, rowID string) []byte {
	return []byte(scope + "\x00" + rowID)
}

// Encrypt 加密明文，返回信封字节。aad 一般取 AADFor(endpointID)。
func (c *Cipher) Encrypt(plaintext, aad []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, fmt.Errorf("加密器未初始化")
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, 0, headerSize+len(ct))
	out = append(out, envelopeVersion)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Decrypt 解密信封字节。aad 必须与加密时一致（否则认证失败）。
func (c *Cipher) Decrypt(envelope, aad []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, fmt.Errorf("加密器未初始化")
	}
	if len(envelope) < headerSize+tagSize {
		return nil, fmt.Errorf("密文信封过短（%d 字节）", len(envelope))
	}
	if envelope[0] != envelopeVersion {
		return nil, fmt.Errorf("未知的信封版本 0x%02x", envelope[0])
	}
	nonce := envelope[1:headerSize]
	ct := envelope[headerSize:]
	pt, err := c.aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("解密失败（密文被篡改，或密钥/AAD 不匹配）")
	}
	return pt, nil
}

// Mask 生成供界面展示的打码值，**不可反推**原密钥。
//
// 规则：空 → ""；长度 < 8 → "****"；否则 `前缀 + "****" + 末 4 位`，
// 其中前缀 = 下标 1..4 范围内首个 '-' 之前（含该 '-'）的部分。
// 例：`sk-4376...6e22` → `sk-****6e22`。
func Mask(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) < 8 {
		return "****"
	}
	prefix := ""
	limit := len(secret)
	if limit > 5 {
		limit = 5 // 覆盖下标 1..4
	}
	for i := 1; i < limit; i++ {
		if secret[i] == '-' {
			prefix = secret[:i+1]
			break
		}
	}
	return prefix + "****" + secret[len(secret)-4:]
}
