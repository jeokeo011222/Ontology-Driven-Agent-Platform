#!/usr/bin/env python3
"""给 <Button> 动词补前置图标，让"刷新/重试/导出/发布"等动作一眼可辨。

背景：裸文字按钮在高信息密度界面里难以扫读（§3.1 平台管理端），
同一个词在不同页面没有稳定的视觉符号，认知成本高。本脚本按标签词表
（LABEL_ICONS）为按钮补 icon，已显式写了 icon= 的按钮不重复插入。

用法：
  python tools/button-icons.py --list     # 只列出所有按钮标签（用于维护词表）
  python tools/button-icons.py --check    # 报告将要改动的按钮
  python tools/button-icons.py            # 落盘
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

# 标签 → 图标。按"最长前缀优先"匹配，故顺序敏感（长词在前）。
LABEL_ICONS: list[tuple[str, str]] = [
    ("重试实时通道", "refresh"),
    ("加载更多", "plus"),
    ("清除筛选", "x"),
    ("切换运行模式", "arrowLeftRight"),
    ("模拟决策", "scale"),
    ("执行验证", "shieldCheck"),
    ("准备发布", "rocket"),
    ("发起对账", "arrowLeftRight"),
    ("查看执行详情", "arrowRight"),
    ("查看指标", "arrowRight"),
    ("查看全部", "arrowRight"),
    ("发起业务任务", "sparkles"),
    ("提交业务目标", "send"),
    ("测试连接", "plug"),
    ("申请权限", "lock"),
    ("请求取消", "ban"),
    ("停用主体", "ban"),
    ("重试", "refresh"),
    ("刷新", "refresh"),
    ("搜索", "search"),
    ("查询", "search"),
    ("筛选", "filter"),
    ("新建", "plus"),
    ("创建", "plus"),
    ("添加", "plus"),
    ("导入", "upload"),
    ("上传", "upload"),
    ("导出", "download"),
    ("下载", "download"),
    ("保存", "check"),
    ("应用", "check"),
    ("确认", "check"),
    ("提交", "send"),
    ("发布", "rocket"),
    ("回滚", "undo"),
    ("撤销", "undo"),
    ("验证", "shieldCheck"),
    ("校验", "shieldCheck"),
    ("认领", "userCheck"),
    ("清除", "x"),
    ("关闭", "x"),
    ("取消", "x"),
    ("删除", "x"),
    ("查看", "arrowRight"),
    ("执行", "play"),
    ("运行", "play"),
    ("重启", "rotateCw"),
    ("对账", "arrowLeftRight"),
]

SKIP_FILES = {"components/ui/Button.tsx"}
KNOWN_ONLY = {"components/error-state/ErrorState.tsx"}


def find_tag_end(text: str, start: int) -> int:
    """从 '<Button' 起找到标签结束的 '>'（跳过属性表达式里的花括号与字符串）。"""
    depth = 0
    quote: str | None = None
    i = start
    while i < len(text):
        ch = text[i]
        if quote:
            if ch == "\\":
                i += 2
                continue
            if ch == quote:
                quote = None
        elif ch in "\"'`":
            quote = ch
        elif ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
        elif ch == ">" and depth == 0:
            return i
        i += 1
    return -1


def tag_indent(text: str, open_idx: int) -> int:
    line_start = text.rfind("\n", 0, open_idx) + 1
    return open_idx - line_start


def extract_label(text: str, tag_end: int) -> str | None:
    """标签结束后的第一段纯文本子节点；表达式子节点或空节点返回 None。"""
    i = tag_end + 1
    while i < len(text) and text[i] in " \t":
        i += 1
    if i < len(text) and text[i] == "\n":
        # 换行后找第一行有内容的行
        while i < len(text) and text[i] in " \t\n":
            i += 1
    if i >= len(text) or text[i] in "{<":
        return None
    j = i
    while j < len(text) and text[j] not in "\n<{":
        j += 1
    label = text[i:j].strip()
    return label or None


def match_icon(label: str) -> str | None:
    for key, icon in LABEL_ICONS:
        if label.startswith(key):
            return icon
    return None


def iter_buttons(text: str):
    for m in re.finditer(r"<Button\b", text):
        yield m.start()


def process(path: pathlib.Path, mode: str, report: list[str]) -> str | None:
    original = path.read_text(encoding="utf-8")
    if not re.search(r"<Button\b", original):
        return None

    hits: list[tuple[int, str, str]] = []  # (插入位置, 图标, 标签)
    for open_idx in iter_buttons(original):
        tag_end = find_tag_end(original, open_idx)
        if tag_end == -1:
            continue
        tag = original[open_idx : tag_end + 1]
        if re.search(r"\bicon=", tag) or re.search(r"\biconOnly\b", tag):
            continue
        label = extract_label(original, tag_end)
        if not label:
            continue
        icon = match_icon(label)
        if not icon:
            if mode == "list":
                report.append(f"UNMAPPED  {path.as_posix()}: {label}")
            continue
        hits.append((tag_end, icon, label))

    if mode == "list":
        for _, _, label in hits:
            report.append(f"{path.as_posix()}: {label}")
        return None

    if not hits:
        return None

    text = original
    for tag_end, icon, label in sorted(hits, key=lambda h: -h[0]):
        open_idx = text.rfind("<Button", 0, tag_end)
        indent = tag_indent(text, open_idx)
        multiline = "\n" in text[open_idx:tag_end]
        if multiline or (tag_end - open_idx) > 100:
            # 回退到"最后一个换行 + 缩进"之前，避免插出一行只有空格的残行
            cut = tag_end
            while cut > open_idx and text[cut - 1] in " \t":
                cut -= 1
            if cut > open_idx and text[cut - 1] == "\n":
                cut -= 1
            insert = f'\n{" " * (indent + 2)}icon="{icon}"'
            text = text[:cut] + insert + text[tag_end:]
        else:
            text = text[:tag_end] + f' icon="{icon}"' + text[tag_end:]
        report.append(f"{'would set' if mode == 'check' else 'set'} {path.as_posix()}: {label} -> {icon}")

    if mode != "check" and text != original:
        path.write_text(text, encoding="utf-8")
    return text


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--list", action="store_true", help="列出所有按钮标签")
    parser.add_argument("--check", action="store_true", help="只报告")
    parser.add_argument("--root", default="src")
    args = parser.parse_args()

    root = pathlib.Path(args.root)
    if not root.is_dir():
        root = pathlib.Path("web") / args.root

    mode = "list" if args.list else ("check" if args.check else "write")
    report: list[str] = []
    for path in sorted(root.rglob("*.tsx")):
        rel = path.as_posix()
        if any(rel.endswith(s) for s in SKIP_FILES):
            continue
        process(path, mode, report)

    for line in report:
        print(line)
    print(f"\n共 {len(report)} 条。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
