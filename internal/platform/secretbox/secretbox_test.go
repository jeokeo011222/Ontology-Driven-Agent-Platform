package secretbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	c, err := New(key)
	if err != nil {
		t.Fatalf("构造 Cipher 失败: %v", err)
	}
	return c
}

func TestEnvelopeRoundTrip(t *testing.T) {
	c := newTestCipher(t)
	secret := []byte("sk-43765f3f9772473783715644f8926e22")
	aad := AADFor("11111111-2222-3333-4444-555555555555")

	env, err := c.Encrypt(secret, aad)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if env[0] != envelopeVersion {
		t.Fatalf("信封首字节应为版本号，实际 0x%02x", env[0])
	}
	if bytes.Contains(env, secret) {
		t.Fatalf("密文中不应出现明文密钥")
	}

	got, err := c.Decrypt(env, aad)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip 不一致: %q", got)
	}
}

func TestDecryptWrongAADFails(t *testing.T) {
	c := newTestCipher(t)
	env, err := c.Encrypt([]byte("secret-value"), AADFor("endpoint-A"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if _, err := c.Decrypt(env, AADFor("endpoint-B")); err == nil {
		t.Fatalf("跨端点搬运密文必须解密失败")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	c := newTestCipher(t)
	aad := AADFor("endpoint-A")
	env, err := c.Encrypt([]byte("secret-value"), aad)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	env[len(env)-1] ^= 0xff
	if _, err := c.Decrypt(env, aad); err == nil {
		t.Fatalf("篡改密文必须解密失败")
	}
	// 改版本号也必须失败（不得静默按新版本解）
	env2, _ := c.Encrypt([]byte("secret-value"), aad)
	env2[0] = 0x09
	if _, err := c.Decrypt(env2, aad); err == nil {
		t.Fatalf("未知信封版本必须解密失败")
	}
}

func TestDecryptTooShortFails(t *testing.T) {
	c := newTestCipher(t)
	if _, err := c.Decrypt([]byte{0x01, 0x02}, AADFor("e")); err == nil {
		t.Fatalf("过短信封必须报错")
	}
}

func TestNonceIsUnique(t *testing.T) {
	c := newTestCipher(t)
	aad := AADFor("e")
	a, _ := c.Encrypt([]byte("same"), aad)
	b, _ := c.Encrypt([]byte("same"), aad)
	if bytes.Equal(a, b) {
		t.Fatalf("同一明文两次加密结果不得相同（nonce 必须随机）")
	}
}

func TestMask(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short", "****"},
		{"abcdefg", "****"}, // 7 < 8
		{"sk-43765f3f9772473783715644f8926e22", "sk-****6e22"},
		{"abcdefgh", "****efgh"},       // 无 '-' 前缀则为空
		{"ab-abcdefgh", "ab-****efgh"}, // 下标 2 处的 '-' 被采用
	}
	for _, tc := range cases {
		if got := Mask(tc.in); got != tc.want {
			t.Errorf("Mask(%q) = %q, 期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestDecodeMasterKey(t *testing.T) {
	raw := make([]byte, keySize)
	for i := range raw {
		raw[i] = byte(i)
	}
	base64Key := base64.StdEncoding.EncodeToString(raw)
	hexKey := hex.EncodeToString(raw)

	for name, s := range map[string]string{"base64": base64Key, "hex": hexKey} {
		got, err := DecodeMasterKey(s)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", name, err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("%s 解析结果不一致", name)
		}
	}

	for _, bad := range []string{"", "not-a-key", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := DecodeMasterKey(bad); err == nil {
			t.Errorf("非法主密钥 %q 必须报错", bad)
		}
	}
}

func TestNewFromEnvAbsentReturnsNil(t *testing.T) {
	c, err := NewFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("未配置主密钥不应报错: %v", err)
	}
	if c != nil {
		t.Fatalf("未配置主密钥应返回 nil（由调用方 fail-closed）")
	}
}

func TestNewFromEnvInvalidFails(t *testing.T) {
	if _, err := NewFromEnv(func(string) string { return "garbage" }); err == nil {
		t.Fatalf("非法主密钥必须报错")
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatalf("非 32 字节密钥必须报错")
	}
}
