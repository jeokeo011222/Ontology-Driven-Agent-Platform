#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""租户上下文注入门禁（静态）。

存在的理由（评测清单 §6 盲区 ②：把"含字符串"当作"可用"）：
缺陷 L1 在静态阅读时"看起来是对的"——SQL 里确实出现了 app.current_tenant_id
这个名字，单测也全绿；但它其实是**RLS 策略函数名**而不是 GUC 名，且写成
`SET LOCAL app.current_tenant_id = $1` 时 PostgreSQL 在解析期就拒绝（SET 不
接受参数占位符）。结果：任何调用它的方法都必然失败（线上表现为 3 个端点 500）。

本门禁把"租户上下文只能有一条注入路径"变成可执行的静态断言：
  R1 禁止写 app.current_tenant_id（它是函数，不是会话变量）
  R2 禁止在 pgxutil 之外调用 set_config('app.tenant_id', ...)
  R3 禁止用参数占位符给会话变量赋值（SET ... = $n）
  R4 禁止重新引入本地 setTenant(...) 辅助函数

判据可证伪：把任一违反项写回代码，本脚本必须变红并指出 file:line。
"""
import io
import os
import re
import sys

REPO = sys.argv[1] if len(sys.argv) > 1 else "."
# 唯一的合法注入点：所有写 app.tenant_id 的代码都必须在这里。
SOLE_INJECTION_POINT = os.path.normpath("internal/platform/pgxutil/pgxutil.go")

# 注释必须剥离后再扫描：代码里"引用反面教材"的说明性注释
# （例如解释为什么不能写 SET LOCAL app.current_tenant_id = $1）
# 不是缺陷，否则门禁会被自己的文档触发，退化成噪声。
BLOCK_COMMENT = re.compile(r"/\*.*?\*/", re.DOTALL)
LINE_COMMENT = re.compile(r"//[^\n]*")

RULES = [
    (
        "R1",
        re.compile(r"app\.current_tenant_id\s*=\s*"),
        "禁止给 app.current_tenant_id 赋值：它是 RLS 策略**函数**，不是会话变量；"
        "GUC 名是 app.tenant_id",
    ),
    (
        "R2",
        re.compile(r"""set_config\(\s*['"]app\.tenant_id['"]"""),
        "禁止在 pgxutil 之外调用 set_config('app.tenant_id', ...)："
        "租户注入必须只有一条实现（pgxutil.WithTenantTx）",
    ),
    (
        "R3",
        re.compile(r"SET\s+(?:LOCAL\s+)?app\.\w+\s*=\s*\$"),
        "禁止 SET <会话变量> = $n：SET 语法不接受参数占位符，"
        "PostgreSQL 会在解析期报 syntax error at or near \"$n\"",
    ),
    (
        "R4",
        re.compile(r"\bsetTenant\s*\("),
        "禁止重新引入本地 setTenant(...) 辅助函数：应统一走 "
        "s.TenantTx / s.TenantTxAs / pgxutil.WithTenantTx",
    ),
]


def go_files(root):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [
            d for d in dirnames
            if d not in (".git", "node_modules", "vendor", "dist")
        ]
        for fn in filenames:
            if fn.endswith(".go"):
                yield os.path.join(dirpath, fn)


def main():
    violations = []
    checked = 0
    for path in go_files(REPO):
        rel = os.path.normpath(os.path.relpath(path, REPO))
        if rel.endswith("_test.go"):
            # 测试可以断言"旧写法不存在"，不参与门禁。
            continue
        checked += 1
        try:
            text = io.open(path, encoding="utf-8").read()
        except (OSError, UnicodeDecodeError):
            continue
        # 保留长度替换注释，既排除文档干扰，又保持行号映射正确。
        text = BLOCK_COMMENT.sub(lambda m: " " * len(m.group(0)), text)
        text = LINE_COMMENT.sub(lambda m: " " * len(m.group(0)), text)
        for line_no, line in enumerate(text.split("\n"), 1):
            for rule_id, pattern, why in RULES:
                if not pattern.search(line):
                    continue
                if rule_id == "R2" and rel == SOLE_INJECTION_POINT:
                    continue  # 唯一合法注入点
                violations.append((rel, line_no, rule_id, why, line.strip()))

    if violations:
        print("租户上下文门禁：不通过（%d 处）\n" % len(violations))
        for rel, line_no, rule_id, why, snippet in violations:
            print("  [%s] %s:%d" % (rule_id, rel, line_no))
            print("       原因：%s" % why)
            print("       代码：%s" % snippet[:160])
        return 1

    print("租户上下文门禁：通过（扫描 %d 个非测试 Go 文件；"
          "唯一注入点 %s）" % (checked, SOLE_INJECTION_POINT))
    return 0


if __name__ == "__main__":
    sys.exit(main())
