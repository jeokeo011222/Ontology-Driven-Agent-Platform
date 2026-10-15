// echo 是工具脚本执行器的真实测试模块（GOOS=wasip1 GOARCH=wasm 编译）。
//
// 行为契约：从 stdin 读取一个 JSON 对象，原样回显到 stdout：
//
//	输入  {"hello":"世界"}
//	输出  {"echo":{"hello":"世界"}}
//
// 编译（仓库根目录执行）：
//
//	$env:GOOS='wasip1'; $env:GOARCH='wasm'
//	go build -o internal/api/handlers/testdata/wasm/echo.wasm ./internal/api/handlers/testdata/wasm/echo
package main

import (
	"encoding/json"
	"io"
	"os"
)

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		os.Exit(3)
	}
	out, err := json.Marshal(map[string]any{"echo": input})
	if err != nil {
		os.Exit(4)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		os.Exit(5)
	}
}
