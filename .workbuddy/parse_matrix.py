import io, sys

p = "docs/第六篇：Harness 执行引擎与状态机详细设计.md"
lines = io.open(p, encoding="utf-8").read().splitlines()
for i, l in enumerate(lines):
    if l.startswith("| 当前状态") and i + 1 < len(lines) and set(lines[i + 1].replace("|", "").replace("-", "").strip()) == set():
        header = [c.strip() for c in l.strip().strip("|").split("|")]
        print("COLS(%d):" % len(header))
        for j, h in enumerate(header):
            print("  %2d %s" % (j, h))
        for row in lines[i + 2:]:
            if not row.startswith("|"):
                break
            cells = [c.strip() for c in row.strip().strip("|").split("|")]
            print("ROW %-26s ncells=%d" % (cells[0], len(cells) - 1))
            for j, c in enumerate(cells[1:], start=1):
                if c != "-":
                    print("    %-28s -> %s" % (header[j] if j < len(header) else "??%d" % j, c))
        break
else:
    print("matrix not found")
