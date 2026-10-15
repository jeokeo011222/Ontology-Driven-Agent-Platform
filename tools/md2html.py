#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把 docs/ODAP-MVP-设计符合性自查矩阵.md 渲染为自包含 HTML（ODAP 亮色设计令牌）。

仅支持该文档实际用到的 Markdown 子集：标题、表格、围栏代码块、无序列表、
有序列表、引用块、水平线、以及行内 **粗体** / `代码`。
"""
import html
import re
import sys

SRC = sys.argv[1] if len(sys.argv) > 1 else "docs/ODAP-MVP-设计符合性自查矩阵.md"
DST = sys.argv[2] if len(sys.argv) > 2 else "docs/ODAP-MVP-设计符合性自查矩阵.html"


def inline(t: str) -> str:
    t = html.escape(t, quote=False)
    t = re.sub(r"`([^`]+)`", r"<code>\1</code>", t)
    t = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", t)
    return t


def render(md: str) -> str:
    out, i = [], 0
    lines = md.split("\n")
    n = len(lines)
    while i < n:
        ln = lines[i]
        # 围栏代码块
        if ln.strip().startswith("```"):
            i += 1
            buf = []
            while i < n and not lines[i].strip().startswith("```"):
                buf.append(html.escape(lines[i]))
                i += 1
            i += 1
            out.append("<pre><code>" + "\n".join(buf) + "</code></pre>")
            continue
        # 表格
        if ln.startswith("|") and i + 1 < n and re.match(r"^\|[\s:|-]+\|$", lines[i + 1].strip()):
            header = [c.strip() for c in ln.strip().strip("|").split("|")]
            i += 2
            rows = []
            while i < n and lines[i].startswith("|"):
                rows.append([c.strip() for c in lines[i].strip().strip("|").split("|")])
                i += 1
            out.append("<table><thead><tr>" +
                       "".join(f"<th>{inline(h)}</th>" for h in header) +
                       "</tr></thead><tbody>")
            for r in rows:
                out.append("<tr>" + "".join(f"<td>{inline(c)}</td>" for c in r) + "</tr>")
            out.append("</tbody></table>")
            continue
        # 标题
        m = re.match(r"^(#{1,6})\s+(.*)$", ln)
        if m:
            lvl = len(m.group(1))
            out.append(f"<h{lvl}>{inline(m.group(2))}</h{lvl}>")
            i += 1
            continue
        # 水平线
        if re.match(r"^-{3,}$", ln.strip()):
            out.append("<hr/>")
            i += 1
            continue
        # 引用块
        if ln.startswith(">"):
            buf = []
            while i < n and lines[i].startswith(">"):
                buf.append(lines[i].lstrip(">").strip())
                i += 1
            out.append("<blockquote>" + "<br/>".join(inline(b) for b in buf) + "</blockquote>")
            continue
        # 列表
        if re.match(r"^\s*[-*]\s+", ln) or re.match(r"^\s*\d+\.\s+", ln):
            ordered = bool(re.match(r"^\s*\d+\.\s+", ln))
            tag = "ol" if ordered else "ul"
            out.append(f"<{tag}>")
            while i < n and (re.match(r"^\s*[-*]\s+", lines[i]) or re.match(r"^\s*\d+\.\s+", lines[i])):
                item = re.sub(r"^\s*(?:[-*]|\d+\.)\s+", "", lines[i])
                out.append(f"<li>{inline(item)}</li>")
                i += 1
            out.append(f"</{tag}>")
            continue
        if ln.strip() == "":
            i += 1
            continue
        out.append(f"<p>{inline(ln)}</p>")
        i += 1
    return "\n".join(out)


CSS = """
:root{--brand:#2457a7;--text:#172b4d;--text2:#667085;--bg:#f4f6f8;--surface:#fff;
--success:#16803c;--warning:#b54708;--error:#b42318;--info:#175cd3;--border:#d7dce5;}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font-size:15px;line-height:1.7;
font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;}
.wrap{max-width:1180px;margin:0 auto;padding:32px 24px 64px;}
h1{font-size:28px;margin:8px 0 4px;line-height:1.35}
h2{font-size:21px;margin:36px 0 12px;padding-bottom:8px;border-bottom:2px solid var(--border)}
h3{font-size:17px;margin:24px 0 8px;color:var(--brand)}
h4{font-size:15px;margin:18px 0 6px}
p{margin:8px 0}
a{color:var(--brand)}
code{background:#eef1f5;border:1px solid var(--border);border-radius:6px;
padding:1px 6px;font-size:13px;font-family:ui-monospace,Consolas,Menlo,monospace;color:#26364f}
pre{background:#0f172a;color:#e6edf7;padding:16px 18px;border-radius:10px;overflow:auto}
pre code{background:none;border:none;color:inherit;padding:0;font-size:13px;line-height:1.6}
table{border-collapse:collapse;width:100%;margin:14px 0;background:var(--surface);
border-radius:10px;overflow:hidden;box-shadow:0 1px 3px rgba(23,43,77,.08);font-size:14px}
th{background:#eef1f5;text-align:left;font-weight:600;padding:10px 12px;border-bottom:1px solid var(--border)}
td{padding:9px 12px;border-bottom:1px solid #edf0f4;vertical-align:top}
tr:last-child td{border-bottom:none}
blockquote{margin:12px 0;padding:10px 16px;background:#e8eef8;border-left:4px solid var(--brand);
border-radius:0 8px 8px 0;color:#26364f}
hr{border:none;border-top:1px solid var(--border);margin:28px 0}
ul,ol{margin:8px 0;padding-left:24px}
li{margin:4px 0}
"""


def main():
    with open(SRC, encoding="utf-8") as f:
        md = f.read()
    body = render(md)
    doc = ("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\"/>\n"
           "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"/>\n"
           "<title>ODAP MVP 设计符合性自查矩阵与验证报告</title>\n"
           f"<style>{CSS}</style>\n</head>\n<body><div class=\"wrap\">\n"
           f"{body}\n</div></body>\n</html>\n")
    with open(DST, "w", encoding="utf-8") as f:
        f.write(doc)
    print(f"wrote {DST} ({len(doc)} bytes)")


if __name__ == "__main__":
    main()
