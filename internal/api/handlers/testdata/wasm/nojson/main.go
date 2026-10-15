// nojson 是非 JSON 输出失败路径的真实测试模块（GOOS=wasip1 GOARCH=wasm 编译）：
// 正常退出，但 stdout 不是 JSON 对象——执行器必须 fail-closed 报
// INVALID_ARGUMENT，绝不把非结构化输出当成功返回。
//
// 编译（仓库根目录执行）：
//
//	$env:GOOS='wasip1'; $env:GOARCH='wasm'
//	go build -o internal/api/handlers/testdata/wasm/nojson.wasm ./internal/api/handlers/testdata/wasm/nojson
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stdout, "this is not a json object")
	os.Exit(0)
}
