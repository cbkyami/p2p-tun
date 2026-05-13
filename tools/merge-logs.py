#!/usr/bin/env python3
"""
日志合并器 — 将 VPS 端 web-access-log（含真实 IP）与本地 http-logger（含 HTTP 详情）
按时间对齐，输出标准 Apache Combined 格式日志。

用法:
  # 先从 VPS 拉取服务端日志
  scp root@lizncux0.xyz:/root/web-access.log ./server-access.log

  # 合并
  python merge-logs.py --server ./server-access.log --http ./http-access.log

输出:
  1.2.3.4 - - [13/May/2026:14:13:48 +0800] "GET / HTTP/1.1" 200 15234
"""

import argparse
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

TZ = timezone(timedelta(hours=8))

# ── 解析服务端日志 ────────────────────────────────────────
# OPEN  1.2.3.4:56789 → :8080  ch=2  proto=tcp  [2026-05-13 12:00:00]
OPEN_RE = re.compile(
    r"^OPEN\s+(\S+)\s+→\s+:(\d+)\s+ch=(\d+)\s+proto=(tcp|udp)"
)
# CLOSE 1.2.3.4:56789 → :8080  2.3KB↑ / 15.2KB↓  12s  [2026-05-13 12:00:12]
CLOSE_RE = re.compile(
    r"^CLOSE\s+(\S+)\s+→\s+:(\d+)\s+[\d.]+[KMGT]?B↑\s+/\s+[\d.]+[KMGT]?B↓\s+(\d+[smhd]+)\s+\[(.+)\]"
)
TS_RE = re.compile(r"\[(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})\]")

# ── 解析本地 HTTP 日志 ────────────────────────────────────
# 127.0.0.1:52341 - - [13/May/2026:14:13:48 +0800] "GET / HTTP/1.1" 200 15234
HTTP_RE = re.compile(
    r'^(\S+) - - \[(.+?)\] "(\S+)\s+(\S+)\s+HTTP/(\S+)"\s+(\d+)\s+(\S+)'
)

# 时间格式: 13/May/2026:14:13:48 +0800
HTTP_TS_FMT = "%d/%b/%Y:%H:%M:%S %z"
# 服务端时间格式: 2026-05-13 12:00:00
SERVER_TS_FMT = "%Y-%m-%d %H:%M:%S"


def parse_server_time(s: str) -> datetime:
    return datetime.strptime(s, SERVER_TS_FMT).replace(tzinfo=TZ)


def parse_http_time(s: str) -> datetime:
    return datetime.strptime(s, HTTP_TS_FMT)


def parse_server_log(path: str) -> list[dict]:
    """解析服务端 OPEN 日志，返回 [{time, addr, local_port, channel_id}, ...]"""
    entries = []
    for line in Path(path).read_text(errors="replace").splitlines():
        # 只取 OPEN 行
        m = OPEN_RE.search(line)
        if not m:
            continue
        addr = m.group(1)
        local_port = m.group(2)
        channel_id = m.group(3)
        proto = m.group(4)

        # 找时间戳
        tm = TS_RE.search(line)
        if not tm:
            continue
        ts = parse_server_time(tm.group(1))

        entries.append({
            "time": ts,
            "addr": addr,
            "local_port": local_port,
            "channel_id": channel_id,
            "proto": proto,
        })
    entries.sort(key=lambda e: e["time"])
    return entries


def parse_http_log(path: str) -> list[dict]:
    """解析本地 HTTP 日志，返回 [{time, method, path, status, bytes}, ...]"""
    entries = []
    for line in Path(path).read_text(errors="replace").splitlines():
        if not line.strip() or line.startswith("---"):
            continue
        m = HTTP_RE.match(line)
        if not m:
            continue
        try:
            ts = parse_http_time(m.group(2))
        except ValueError:
            continue
        entries.append({
            "time": ts,
            "_src": m.group(1),
            "method": m.group(3),
            "path": m.group(4),
            "http_ver": m.group(5),
            "status": m.group(6),
            "bytes": m.group(7),
        })
    entries.sort(key=lambda e: e["time"])
    return entries


def merge(server_entries: list[dict], http_entries: list[dict], max_delta: float = 2.0):
    """按时间窗口（默认 ±2 秒）匹配服务端 IP 和 HTTP 请求。"""
    merged = []
    # 贪心匹配：每个 HTTP 条目找最近的服务端 OPEN 条目
    s_idx = 0
    for h in http_entries:
        best = None
        best_delta = max_delta
        # 向前扫描服务端条目
        while s_idx < len(server_entries) and server_entries[s_idx]["time"] <= h["time"] + timedelta(seconds=max_delta):
            s = server_entries[s_idx]
            delta = abs((s["time"] - h["time"]).total_seconds())
            if delta < best_delta:
                best_delta = delta
                best = s
            s_idx += 1
        if best:
            merged.append({**h, "remote_addr": best["addr"], "local_port": best["local_port"]})
        else:
            merged.append({**h, "remote_addr": h["_src"], "local_port": "?"})
    return merged


def main():
    ap = argparse.ArgumentParser(description="日志合并器")
    ap.add_argument("--server", required=True, help="服务端 web-access 日志路径")
    ap.add_argument("--http", required=True, help="本地 http-logger 日志路径")
    ap.add_argument("--max-delta", type=float, default=2.0, help="时间匹配窗口（秒），默认 2")
    args = ap.parse_args()

    print(f"解析服务端日志: {args.server}", file=sys.stderr)
    server_entries = parse_server_log(args.server)
    print(f"  → {len(server_entries)} 条 OPEN 记录", file=sys.stderr)

    print(f"解析 HTTP 日志: {args.http}", file=sys.stderr)
    http_entries = parse_http_log(args.http)
    print(f"  → {len(http_entries)} 条 HTTP 请求", file=sys.stderr)

    merged = merge(server_entries, http_entries, args.max_delta)

    for m in merged:
        ts = m["time"].strftime(HTTP_TS_FMT)
        print(f'{m["remote_addr"]} - - [{ts}] "{m["method"]} {m["path"]} HTTP/{m["http_ver"]}" {m["status"]} {m["bytes"]}')


if __name__ == "__main__":
    main()
