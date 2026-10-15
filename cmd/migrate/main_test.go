package main

import "testing"

// TestAppRolePassword 覆盖“单一真值来源”规则：仅当运行时连接串使用 odap_app 时
// 才返回其密码；使用 owner 账号或非法连接串时返回空串（由迁移 000017 回退默认值）。
func TestAppRolePassword(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "app role url",
			url:  "postgres://odap_app:apppw@postgres:5432/odap?sslmode=disable",
			want: "apppw",
		},
		{
			name: "app role with special chars password",
			url:  "postgres://odap_app:p%40ss%2Fword@postgres:5432/odap?sslmode=disable",
			want: "p@ss/word",
		},
		{
			name: "owner url is ignored",
			url:  "postgres://odap:ownerpw@postgres:5432/odap?sslmode=disable",
			want: "",
		},
		{
			name: "app role without password yields empty",
			url:  "postgres://odap_app@postgres:5432/odap?sslmode=disable",
			want: "",
		},
		{
			name: "unparseable url yields empty",
			url:  "://not-a-url",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appRolePassword(tc.url); got != tc.want {
				t.Fatalf("appRolePassword(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
