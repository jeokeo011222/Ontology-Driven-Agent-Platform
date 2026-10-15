package store

import "testing"

// TestDeriveProviderID 钉住派生语义：**只在无损映射时成功**。
//
// 回归背景（缺陷 #26）：此前的实现对不可映射字符一律写成 '-'，
// 于是 "E2E 探针供应商" 被静默派生成 "e2e"（中文语义整段丢失、且与任何
// "E2E" 开头的显示名撞主键），而纯中文名又因结果为空而报错——同一函数对
// 两种中文名给出截然不同的行为。本测试对这两种情况都要求 ok=false。
func TestDeriveProviderID(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		wantID      string
		wantOK      bool
	}{
		// ---- 无损映射：应成功 ----
		{"纯 ASCII 单词", "Acme", "acme", true},
		{"空格分词", "Acme Inference", "acme-inference", true},
		{"大小写归一", "ACME-LLM", "acme-llm", true},
		{"下划线折叠为连字符", "acme_llm", "acme-llm", true},
		{"数字参与", "GPT 4o", "gpt-4o", true},
		{"连续分隔符折叠", "Acme   --  LLM", "acme-llm", true},

		// ---- 不可映射：必须 fail-closed，绝不产出丢语义的短 id ----
		{"纯中文名", "阿斯麦推理服务", "", false},
		{"中文为主 + 少量 ASCII", "E2E 探针供应商", "", false},
		{"中文夹在 ASCII 之间", "acme 推理 llm", "", false},
		{"重音字母", "Café", "", false},
		{"emoji", "acme 🚀", "", false},
		{"全分隔符（结果为空）", "---", "", false},
		{"空显示名", "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DeriveProviderID(tc.displayName)
			if ok != tc.wantOK || got != tc.wantID {
				t.Fatalf("DeriveProviderID(%q) = (%q, %v)，期望 (%q, %v)",
					tc.displayName, got, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

// TestValidateProviderID 钉住 provider_id 的规范形状：派生与**显式提供**一视同仁。
// 显式提供此前完全没有校验，中文、空格、'/'、'..' 都能直接写进主键，
// 而契约里 provider_id 是 /model-providers/{id}/verify 的路径参数。
func TestValidateProviderID(t *testing.T) {
	cases := []struct {
		id      string
		wantErr bool
	}{
		{"acme-llm", false},
		{"mock-local", false},    // 种子中的真实取值
		{"openai-compat", false}, // 种子中的真实取值
		{"gpt4o", false},
		{"a2", false},
		{"", true},          // 空
		{"a", true},         // 短于下限
		{"Acme", true},      // 大写
		{"acme_llm", true},  // 下划线
		{"阿斯麦", true},       // 中文
		{"acme llm", true},  // 空格
		{"acme/llm", true},  // 路径分隔符（路径注入面）
		{"..", true},        // 路径上跳
		{"-acme", true},     // 前导连字符
		{"acme-", true},     // 尾随连字符
		{"acme--llm", true}, // 连续连字符
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			err := ValidateProviderID(tc.id)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateProviderID(%q) err=%v，期望 wantErr=%v", tc.id, err, tc.wantErr)
			}
		})
	}
}

// TestValidateProviderID_LengthBounds 单独验证长度上下界（含恰好合规的边界值）。
func TestValidateProviderID_LengthBounds(t *testing.T) {
	just := ""
	for i := 0; i < providerIDMaxLen; i++ {
		just += "a"
	}
	if err := ValidateProviderID(just); err != nil {
		t.Fatalf("长度恰为上限 %d 应通过，却报 %v", providerIDMaxLen, err)
	}
	if err := ValidateProviderID(just + "a"); err == nil {
		t.Fatalf("长度超过上限 %d 应报错", providerIDMaxLen)
	}
	if err := ValidateProviderID("ab"); err != nil {
		t.Fatalf("长度恰为下限 %d 应通过，却报 %v", providerIDMinLen, err)
	}
}
