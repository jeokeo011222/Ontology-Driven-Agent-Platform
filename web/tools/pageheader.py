#!/usr/bin/env python3
"""把各页面里手工拼装的 <header className="page__header"> 收敛为统一的 <PageHeader>。

动机（第十七篇 §3.3 / §4）：22 个页面各自手写 header + h1 + p.page__subtitle，
导致标题层级、间距、图标缺失各处不一致——产品级观感的核心缺口之一。
本脚本一次性把它们替换成 src/components/ui/PageHeader.tsx（自带浅底图标方块、
统一标题字阶与副标题行高、actions 槽对齐），并按路由与侧栏图标保持一致。

用法：python tools/pageheader.py [--check]
"""

from __future__ import annotations

import argparse
import pathlib
import re
import sys

# 文件 → 页面图标（与 layouts/*Layout.tsx 的 NAV_GROUPS 保持同一图标，形成"侧栏—页面"呼应）
PAGE_ICONS: dict[str, str] = {
    # 业务工作台
    "features/workspace/pages/WorkspacePage.tsx": "dashboard",
    "features/assistant/pages/AssistantPage.tsx": "sparkles",
    "features/tasks/pages/TasksPage.tsx": "tasks",
    "features/tasks/pages/TaskDetailPage.tsx": "fileText",
    "features/approvals/pages/ApprovalsPage.tsx": "award",
    "features/approvals/pages/ApprovalDetailPage.tsx": "clipboardCheck",
    "features/business-apps/pages/BusinessAppsPage.tsx": "apps",
    # 管理控制台
    "features/monitoring/pages/AdminOverviewPage.tsx": "gauge",
    "features/policies/pages/IdentityPage.tsx": "shieldCheck",
    "features/ontology/pages/OntologyListPage.tsx": "ontology",
    "features/ontology/pages/OntologyDetailPage.tsx": "ontology",
    "features/data-mapping/pages/DataMappingPage.tsx": "database",
    "features/agents/pages/AgentsPage.tsx": "bot",
    "features/tools/pages/ToolsPage.tsx": "wrench",
    "features/workflows/pages/WorkflowsPage.tsx": "workflow",
    "features/llm-providers/pages/ModelsPage.tsx": "cpu",
    "features/erp-connectors/pages/ConnectorsPage.tsx": "link",
    "features/policies/pages/PoliciesPage.tsx": "scale",
    "features/monitoring/pages/MonitoringPage.tsx": "activity",
    "features/audit/pages/AuditPage.tsx": "fileSearch",
    "features/evaluation/pages/EvaluationPage.tsx": "clipboardCheck",
    "features/evaluation/pages/ReleasesPage.tsx": "package",
}

HEADER_OPEN = '<header className="page__header">'
HEADER_CLOSE = "</header>"

TITLE_RE = re.compile(r"<h1 className=\"page__title\">(?P<body>[\s\S]*?)</h1>")
SUBTITLE_RE = re.compile(r"<p className=\"page__subtitle\">(?P<body>[\s\S]*?)</p>")
ACTIONS_RE = re.compile(r"<div className=\"page__actions\">(?P<body>[\s\S]*?)\n[ \t]*</div>")


def reindent(block: str, base: int) -> list[str]:
    """把 JSX 片段按最小缩进去缩进后，统一重排到 base 缩进。"""
    lines = [ln for ln in block.split("\n")]
    while lines and not lines[0].strip():
        lines.pop(0)
    while lines and not lines[-1].strip():
        lines.pop()
    indents = [len(ln) - len(ln.lstrip(" ")) for ln in lines if ln.strip()]
    drop = min(indents) if indents else 0
    pad = " " * base
    return [(pad + ln[drop:]) if ln.strip() else "" for ln in lines]


def as_prop(name: str, raw: str, base: int) -> str:
    """标题/副标题：纯文本走字符串字面量；含 JSX 时必须包一层片段。

    坑：不能写成 title={{expr}}（双花括号 = 对象字面量），
    也不能把 "文本 <span/> 文本" 直接塞进 {}（多子节点不是合法表达式），
    因此统一用 <>...</> 包裹，与原来在 <p>/<h1> 里的 children 语义完全等价。
    """
    if "<" not in raw and "{" not in raw:
        escaped = " ".join(raw.split()).replace('"', '\\"')
        return f'{name}="{escaped}"'
    body = "\n".join(reindent(raw, base + 4))
    inner = " " * (base + 2)
    close = " " * base
    return f"{name}={{\n{inner}<>\n{body}\n{inner}</>\n{close}}}"


def find_header_block(text: str) -> tuple[int, int, str] | None:
    idx = text.find(HEADER_OPEN)
    if idx == -1:
        return None
    # 行首位置用于取缩进
    line_start = text.rfind("\n", 0, idx) + 1
    indent = idx - line_start
    close_idx = text.find("\n" + " " * indent + HEADER_CLOSE, idx)
    if close_idx == -1:
        return None
    end = close_idx + len("\n" + " " * indent + HEADER_CLOSE)
    return line_start, end, " " * indent


def build_pageheader(indent: str, icon: str, title: str | None, subtitle: str | None, actions: str | None) -> str:
    base = len(indent) + 2
    parts = [f"{indent}<PageHeader", f'{indent}  icon="{icon}"']
    if title is not None:
        parts.append(f"{indent}  {as_prop('title', title, base)}")
    if subtitle is not None:
        parts.append(f"{indent}  {as_prop('subtitle', subtitle, base)}")
    if actions is not None:
        body = "\n".join(reindent(actions, base + 4))
        parts.append(f"{indent}  actions={{")
        parts.append(f"{indent}    <>")
        parts.append(body)
        parts.append(f"{indent}    </>")
        parts.append(f"{indent}  }}")
    parts.append(f"{indent}/>")
    return "\n".join(parts)


def add_pageheader_import(text: str) -> str:
    """把 PageHeader 加入值导入（避开 import type 行）。"""
    pattern = re.compile(r"^import \{(?P<names>[^}]*)\} from '@/components/ui';$", re.M)
    match = pattern.search(text)
    if not match:
        return text
    names = [n.strip() for n in match.group("names").split(",") if n.strip()]
    if "PageHeader" in names:
        return text
    names.append("PageHeader")
    names.sort(key=str.lower)
    if len(", ".join(names)) <= 96:
        replacement = "import { " + ", ".join(names) + " } from '@/components/ui';"
        return text[: match.start()] + replacement + text[match.end() :]

    block = "import {\n" + "".join(f"  {n},\n" for n in names) + "} from '@/components/ui';"
    return text[: match.start()] + block + text[match.end() :]


def process(path: pathlib.Path, key: str, check_only: bool) -> str | None:
    original = path.read_text(encoding="utf-8")
    found = find_header_block(original)
    if not found:
        return None
    start, end, indent = found
    block = original[start:end]

    title_m = TITLE_RE.search(block)
    subtitle_m = SUBTITLE_RE.search(block)
    actions_m = ACTIONS_RE.search(block)

    replacement = build_pageheader(
        indent,
        PAGE_ICONS[key],
        title_m.group("body") if title_m else None,
        subtitle_m.group("body") if subtitle_m else None,
        actions_m.group("body") if actions_m else None,
    )

    text = original[:start] + replacement + original[end:]
    text = add_pageheader_import(text)
    if not check_only:
        path.write_text(text, encoding="utf-8")
    return key


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--root", default="src")
    args = parser.parse_args()

    root = pathlib.Path(args.root)
    if not root.is_dir():
        root = pathlib.Path("web") / args.root

    changed: list[str] = []
    for key, icon in PAGE_ICONS.items():
        path = root / key
        if not path.exists():
            print(f"! 缺少文件：{key}", file=sys.stderr)
            continue
        result = process(path, key, args.check)
        if result:
            changed.append(f"{'would convert' if args.check else 'converted'} {key} (icon={icon})")

    for line in changed:
        print(line)

    expected = len(PAGE_ICONS)
    if args.check and not changed:
        print(f"\n全部 {expected} 个页面均已转换（无待处理项）。")
        return 0
    print(f"\n{len(changed)}/{expected} 个页面已{'待' if args.check else ''}转换。")
    if len(changed) != expected:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
