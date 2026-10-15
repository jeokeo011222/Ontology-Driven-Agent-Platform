#!/usr/bin/env python3
"""测试用 SPARQL 1.1 Protocol 端点（由 RDFLib 提供真实引擎语义）。

⚠️ 本文件**不是** ODAP 的组成部分，也不得用于生产。它只有一个用途：

    在没有 Fuseki 实例的环境里（例如 docker 镜像层下载受限的开发机/CI），
    仍然能用**真实的 SPARQL 引擎**验证 GraphStore 适配器生成的语句
    是否可解析、语义是否正确（VALUES 连接、GRAPH 作用域、STRSTARTS 过滤、
    ASK {}、空白节点往返等）。

与 `internal/ontology/graphstore/fusekistore_test.go` 的区别：
    Go 单元测试用 HTTP 替身验证"适配器发了什么、怎么解析响应"（协议形状与解析），
    本脚本验证"发出去的语句在真实引擎上跑得通、结果对得上"（语义正确性）。
    两者互补，都不能替代对真实 Fuseki 的验证（见 tools/ci/12-graphstore.sh 的说明）。

用法：
    python3 tools/fake-sparql/rdflib_endpoint.py --port 3030
    # 等价于 Fuseki 的 {base}/query 与 {base}/update
    # 例如 FUSEKI_ENDPOINT=http://127.0.0.1:3030/odap
"""

from __future__ import annotations

import argparse
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Lock

try:
    from rdflib import Dataset
except ImportError:  # pragma: no cover
    sys.stderr.write("需要 rdflib：pip install rdflib\n")
    raise SystemExit(2)


class MemoryStore:
    """进程内 RDF 数据集；rdflib 的 Dataset 支持具名图与 SPARQL Update。"""

    def __init__(self) -> None:
        self._ds = Dataset(default_union=False)
        self._lock = Lock()

    def query(self, sparql: str) -> bytes:
        with self._lock:
            result = self._ds.query(sparql)
            # rdflib 各版本在 str / bytes 之间摇摆（7.6 起 serialize 返回 bytes）。
            serialized = result.serialize(format="json")
            return serialized if isinstance(serialized, bytes) else serialized.encode("utf-8")

    def update(self, sparql: str) -> None:
        with self._lock:
            self._ds.update(sparql)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "rdflib-endpoint/1.0"

    store: MemoryStore
    path_prefix: str

    def _json(self, status: int, payload: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", "application/sparql-results+json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _text(self, status: int, message: str) -> None:
        payload = message.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self) -> None:  # noqa: N802（stdlib 要求的命名）
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode("utf-8")
        ctype = (self.headers.get("Content-Type") or "").split(";")[0].strip()

        if not self.path.startswith(self.path_prefix):
            self._text(404, f"未知端点 {self.path}（期望前缀 {self.path_prefix}）")
            return

        suffix = self.path[len(self.path_prefix):]
        try:
            if suffix == "/query":
                if ctype != "application/sparql-query":
                    self._text(400, f"查询端点期望 application/sparql-query，得到 {ctype!r}")
                    return
                self._json(200, self.store.query(body))
                return
            if suffix == "/update":
                if ctype != "application/sparql-update":
                    self._text(400, f"更新端点期望 application/sparql-update，得到 {ctype!r}")
                    return
                self.store.update(body)
                self.send_response(204)
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            self._text(404, f"未知端点 {self.path}")
        except Exception as exc:  # noqa: BLE001 —— 测试替身需把引擎错误如实回显
            self._text(400, f"{type(exc).__name__}: {exc}")

    def log_message(self, fmt: str, *args) -> None:  # noqa: A003
        sys.stderr.write("[rdflib-endpoint] " + fmt % args + "\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=3030)
    parser.add_argument("--path", default="/odap", help="数据集根路径（与 Fuseki 对齐）")
    opts = parser.parse_args()

    Handler.store = MemoryStore()
    Handler.path_prefix = opts.path.rstrip("/")
    srv = ThreadingHTTPServer((opts.host, opts.port), Handler)
    sys.stderr.write(
        f"[rdflib-endpoint] 监听 http://{opts.host}:{opts.port}{Handler.path_prefix} "
        f"(query/update)\n")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
