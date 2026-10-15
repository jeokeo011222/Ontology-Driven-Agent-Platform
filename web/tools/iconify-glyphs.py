#!/usr/bin/env python3
"""把前端源码里残留的 Unicode 字形图标批量替换为自研 <Icon> 组件调用。

背景：设计书 §3 / 第十七篇要求"颜色不能成为唯一的状态表达方式"，图标必须与文字、颜色
三重表达。早期实现为省事直接写了裸 Unicode 字形（⚠ ⚑ ⊘ ⇄ ✕ ✓ ▶ ◔ ↻ ‖），
问题：1) 不同字体下字形粗细/基线不一致；2) 尺寸只能靠 font-size 调，与 24×24 描边
图标体系脱节；3) 无法继承 --icon-stroke 线宽。本脚本做一次性收敛。

用法：python tools/iconify-glyphs.py [--check]
    --check  只报告将要修改的位置，不落盘（CI 可用）。
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

# 字形 → 图标名（图标名以 src/components/icon/icons.tsx 为准）
GLYPH_TO_ICON: dict[str, str] = {
    "\u26a0": "alertTriangle",  # ⚠
    "\u2691": "flag",  # ⚑
    "\u2298": "ban",  # ⊘
    "\u21c4": "arrowLeftRight",  # ⇄
    "\u2715": "circleX",  # ✕
    "\u2713": "circleCheck",  # ✓
    "\u25b6": "play",  # ▶
    "\u25d4": "loader",  # ◔
    "\u21bb": "rotateCw",  # ↻
    "\u2016": "userCheck",  # ‖
}

GLYPH_CLASS = "".join(GLYPH_TO_ICON)

# <span className="banner__icon" aria-hidden="true">\n  ⚠\n</span>
SPAN_PATTERN = re.compile(
    r"(?P<indent>[ \t]*)<span className=\"banner__icon\" aria-hidden=\"true\">"
    r"\s*\n\s*(?P<glyph>[" + GLYPH_CLASS + r"])\s*\n\s*</span>"
)

# 裸的字形（Dialog 关闭按钮等），只替换"整行仅有字形"的情形，避免误伤注释与业务文本
BARE_LINE_PATTERN = re.compile(
    r"(?P<indent>[ \t]*)(?P<glyph>[" + GLYPH_CLASS + r"])[ \t]*\n"
)

ICON_IMPORT = "import { Icon } from '@/components/icon';"

# 明确排除：注释里出现的 ⚠️（U+26A0 U+FE0F）属于文档说明，不是 UI 图标
EXCLUDE_FILES = {
    "components/icon/icons.tsx",
}


def has_icon_import(text: str) -> bool:
    return "from '@/components/icon'" in text


def add_icon_import(text: str) -> str:
    """在最后一条 import 之后插入 Icon 导入，保持分组整洁。"""
    if has_icon_import(text):
        return text
    lines = text.split("\n")
    last = -1
    for idx, line in enumerate(lines):
        if line.startswith("import ") or line.startswith("} from '"):
            last = idx
    if last == -1:
        return ICON_IMPORT + "\n" + text
    lines.insert(last + 1, ICON_IMPORT)
    return "\n".join(lines)


def process(path: pathlib.Path, check_only: bool) -> list[str]:
    original = path.read_text(encoding="utf-8")
    text = original
    notes: list[str] = []

    def span_repl(match: re.Match[str]) -> str:
        indent = match.group("indent")
        glyph = match.group("glyph")
        name = GLYPH_TO_ICON[glyph]
        notes.append(f"{path.name}: banner {glyph} -> {name}")
        return (
            f'{indent}<span className="banner__icon" aria-hidden="true">\n'
            f'{indent}  <Icon name="{name}" size={{16}} />\n'
            f"{indent}</span>"
        )

    text = SPAN_PATTERN.sub(span_repl, text)

    # 裸字形整行（如 Dialog 的关闭按钮），仅在确实存在且不是 banner 残留时处理
    def bare_repl(match: re.Match[str]) -> str:
        indent = match.group("indent")
        glyph = match.group("glyph")
        name = GLYPH_TO_ICON[glyph]
        notes.append(f"{path.name}: bare {glyph} -> {name}")
        return f'{indent}<Icon name="{name}" size={{16}} />\n'

    if "banner__icon" not in text:
        text = BARE_LINE_PATTERN.sub(bare_repl, text)

    if text != original:
        text = add_icon_import(text)
        if not check_only:
            path.write_text(text, encoding="utf-8")

    return notes


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="只报告，不落盘")
    parser.add_argument("--root", default="src", help="相对 web/ 的源码根目录")
    args = parser.parse_args()

    root = pathlib.Path(args.root)
    if not root.is_dir():
        root = pathlib.Path("web") / args.root

    targets: list[pathlib.Path] = []
    for pattern in ("**/*.tsx", "**/*.ts"):
        targets.extend(sorted(root.glob(pattern)))

    all_notes: list[str] = []
    for path in targets:
        rel = path.as_posix()
        if any(rel.endswith(ex) for ex in EXCLUDE_FILES):
            continue
        all_notes.extend(process(path, args.check))

    residual: list[str] = []
    for path in targets:
        rel = path.as_posix()
        if any(rel.endswith(ex) for ex in EXCLUDE_FILES):
            continue
        body = path.read_text(encoding="utf-8")
        for lineno, line in enumerate(body.split("\n"), start=1):
            stripped = line.strip()
            # 注释行与非 UI 文本（含字母的说明句）不算残留
            if stripped.startswith(("*", "//", "/*")):
                continue
            if any(g in line for g in GLYPH_CLASS):
                residual.append(f"{rel}:{lineno}: {stripped[:90]}")

    for note in all_notes:
        print(("would change " if args.check else "changed ") + note)

    if residual:
        print("\n仍有字形残留（需人工确认是否为业务文本）：", file=sys.stderr)
        for item in residual:
            print("  " + item, file=sys.stderr)
        return 2

    print(f"\n共处理 {len(all_notes)} 处。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
