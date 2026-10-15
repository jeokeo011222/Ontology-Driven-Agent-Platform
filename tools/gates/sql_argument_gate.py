#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""SQL 绑定门禁（静态，判据力求"可证伪且零噪声"）。

背景（本次 L1 修复中连带发现的一类缺陷）：
  A. BindRole 的 INSERT 写了 6 个占位符（$1..$6）却绑定了 7 个实参
     （第 7 个是重复传入的 tenantID），PostgreSQL 直接报
     "bind message supplies 7 parameters, but prepared statement requires 6"。
  B. 多处语句把同一个占位符同时用于行主键与租户列：
         WHERE tenant_id = $1 AND id = $1
     只绑定 1 个参数，等价于要求 tenant_id = <行主键>，恒命中 0 行。

对 A 类，存在**可靠**的静态判据：同一调用中"不同占位符个数"必须等于"绑定实参个数"。
本门禁强制执行该判据（ENFORCED）。

对 B 类，必须诚实说明：**不存在**可靠的局部静态判据。
    WHERE tenant_id = $1 AND id = $1        ← 缺陷（$1 是 session_id）
    ... m.tenant_id = $1 ... t.id = $1      ← 合法（$1 就是 tenant_id）
两者在词法上完全同形，区分它们需要知道实参的语义角色——即数据流分析。
因此 B 类只做**报告**（ADVISORY，不影响退出码），并明确要求由端到端/集成
断言覆盖（"该端点必须真的改动到行，而不是返回 404"）。
把 B 类做成强制执行，会让门禁在合法 JOIN 上报错、进而被加白名单绕过，
最终退化成噪声——这正是评测清单 §6 警告的门禁失效模式。
"""
import io
import os
import re
import sys

REPO = sys.argv[1] if len(sys.argv) > 1 else "."

BLOCK_COMMENT = re.compile(r"/\*.*?\*/", re.DOTALL)
LINE_COMMENT = re.compile(r"//[^\n]*")
STRING_LITERAL = re.compile(r'`[^`]*`|"(?:[^"\\]|\\.)*"')
CALL = re.compile(r"\.(?:Exec|Query|QueryRow)\(")
PLACEHOLDER = re.compile(r"\$(\d+)")
# 动态拼接占位符时无法静态计数（例如 pgxPlaceholder(len(args)+1)）。
DYNAMIC_MARK = re.compile(r"pgxPlaceholder\(|\+\s*\w+\s*\+|\+\s*itoa\(")
# 同一 $N 与两个不同列比较（仅供人工复核）。
REUSE = re.compile(
    r"(?P<c1>[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)"
    r"\s*=\s*\$(?P<n>\d+)"
    r"(?P<mid>[^;]{0,300}?)"
    r"(?P<c2>[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)"
    r"\s*=\s*\$(?P=n)\b",
    re.DOTALL,
)


def go_files(root):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames
                       if d not in (".git", "node_modules", "vendor", "dist")]
        for fn in filenames:
            if fn.endswith(".go"):
                yield os.path.join(dirpath, fn)


def line_of(text, offset):
    return text.count("\n", 0, offset) + 1


def split_top_level(s):
    """按顶层逗号切分；忽略字符串字面量与括号内部的逗号。"""
    parts, buf, depth, i, n = [], [], 0, 0, len(s)
    while i < n:
        ch = s[i]
        if ch in "`\"":
            quote = ch
            j = i + 1
            while j < n:
                if s[j] == "\\" and quote == '"':
                    j += 2
                    continue
                if s[j] == quote:
                    break
                j += 1
            buf.append(s[i:j + 1])
            i = j + 1
            continue
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append("".join(buf))
            buf = []
        else:
            buf.append(ch)
        i += 1
    parts.append("".join(buf))
    return parts


def match_close(text, open_idx):
    depth, i, n = 0, open_idx, len(text)
    while i < n:
        ch = text[i]
        if ch in "`\"":
            quote = ch
            j = i + 1
            while j < n:
                if text[j] == "\\" and quote == '"':
                    j += 2
                    continue
                if text[j] == quote:
                    break
                j += 1
            i = j + 1
            continue
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth == 0:
                return i
        i += 1
    return -1


def main():
    enforced, advisory, calls_checked = [], [], 0
    for path in go_files(REPO):
        rel = os.path.normpath(os.path.relpath(path, REPO))
        try:
            raw = io.open(path, encoding="utf-8").read()
        except (OSError, UnicodeDecodeError):
            continue
        masked = BLOCK_COMMENT.sub(lambda m: " " * len(m.group(0)), raw)
        masked = LINE_COMMENT.sub(lambda m: " " * len(m.group(0)), masked)

        # ---- ENFORCED：绑定实参个数 == 不同占位符个数 ----
        for m in CALL.finditer(masked):
            close = match_close(masked, m.end() - 1)
            if close < 0:
                continue
            segs = split_top_level(masked[m.end():close])
            # Go 允许实参列表尾随逗号（且本仓风格是多行调用 + 尾随逗号），
            # 切分后会产生一个纯空白段；它不是实参，必须剔除，否则门禁
            # 会在所有尾随逗号调用上误报（实测过：8 处命中里 7 处是它）。
            segs = [s for s in segs if s.strip()]
            if len(segs) < 2:
                continue
            sql_seg = segs[1]
            args = segs[2:]
            if any("..." in a for a in args):
                continue                      # args... 无法静态计数
            if DYNAMIC_MARK.search(sql_seg):
                continue                      # 动态拼接占位符，跳过
            sql = "".join(lit.group(0)[1:-1] for lit in STRING_LITERAL.finditer(sql_seg))
            if not sql.strip():
                continue
            nums = [int(x) for x in PLACEHOLDER.findall(sql)]
            if not nums:
                continue
            calls_checked += 1
            distinct = max(nums)
            if distinct != len(args):
                enforced.append((rel, line_of(masked, m.start()), distinct, len(args)))

        # ---- ADVISORY：同一占位符与两个不同列比较（需人工确认）----
        for lit in list(re.finditer(r"`[^`]*`", masked, re.DOTALL)):
            body = lit.group(0)
            if not re.search(r"\b(?:WHERE|AND)\b", body, re.I):
                continue
            for mm in REUSE.finditer(body):
                c1 = mm.group("c1").split(".")[-1]
                c2 = mm.group("c2").split(".")[-1]
                if c1 == c2:
                    continue
                line_no = line_of(masked, lit.start() + mm.start())
                head = "\n".join(raw.split("\n")[max(0, line_no - 3):line_no])
                if "gate-allow-placeholder-reuse" in head:
                    continue
                advisory.append((rel, line_no, c1, c2, mm.group("n")))

    if enforced:
        print("SQL 绑定门禁：不通过（%d 处绑定实参个数与占位符不匹配）\n" % len(enforced))
        for rel, line_no, distinct, nargs in enforced:
            print("  %s:%d" % (rel, line_no))
            print("       SQL 使用 $1..$%d（%d 个），但绑定了 %d 个实参"
                  % (distinct, distinct, nargs))
            print("       后果：pgx/PG 报 bind message supplies N parameters ... → 500")
        return 1

    print("SQL 绑定门禁：通过（检查 %d 个带参 SQL 调用；"
          "实参个数与占位符个数全部一致）" % calls_checked)
    if advisory:
        print("\n以下 %d 处**需人工确认**（同一占位符与两个不同列比较，"
              "既可能是缺陷也可能是合法的同值复用）：" % len(advisory))
        for rel, line_no, c1, c2, ph in advisory:
            print("  %s:%d  %s = $%s ... %s = $%s" % (rel, line_no, c1, ph, c2, ph))
        print("提示：本类缺陷无法用局部静态判据可靠区分，必须由端到端/集成断言"
              "（\"该端点真的改动了行\"）覆盖；此处仅作清单。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
