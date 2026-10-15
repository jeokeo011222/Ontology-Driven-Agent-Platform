// spin 是超时路径的真实测试模块（GOOS=wasip1 GOARCH=wasm 编译）：
// 读到输入后进入纯计算死循环，永不退出，只能由执行器的 context 超时终止。
//
// 编译（仓库根目录执行）：
//
//	$env:GOOS='wasip1'; $env:GOARCH='wasm'
//	go build -o internal/api/handlers/testdata/wasm/spin.wasm ./internal/api/handlers/testdata/wasm/spin
package main

import (
	"io"
	"os"
)

func main() {
	_, _ = io.ReadAll(os.Stdin)
	sum := 0
	for {
		sum++
		if sum < 0 { // 永不为真，防止编译器把循环优化掉
			break
		}
	}
	os.Exit(1)
}
