#!/usr/bin/env python3
"""
web-access-log 插件 — 为每个穿透连接记录 HTTP 风格的访问日志。

输出格式 (combined):
  remote_ip remote_port → local_port 请求字节 响应字节 耗时 时间戳
  e.g. 1.2.3.4:56789 → :8080  2.3KB↑ / 15.2KB↓  12s  [2026-05-13 12:00:00]

不解析 HTTP 包体（插件协议只能拿到字节数），不做性能拖累。
"""

import sys
import json
import os
from datetime import datetime, timezone, timedelta

TZ = timezone(timedelta(hours=8))  # UTC+8 中国时区

# ── 全局状态 ──────────────────────────────────────────────
channels = {}          # channel_id → {remote_addr, local_port, proto, start, rx, tx}
log_file = None
log_path = "web-access.log"


def fmt_bytes(n: int) -> str:
    if n < 1024:
        return f"{n}B"
    elif n < 1048576:
        return f"{n / 1024:.1f}KB"
    else:
        return f"{n / 1048576:.1f}MB"


def fmt_duration(seconds: float) -> str:
    if seconds < 1:
        return "<1s"
    elif seconds < 60:
        return f"{int(seconds)}s"
    else:
        m, s = divmod(int(seconds), 60)
        return f"{m}m{s}s"


def write_log(line: str) -> None:
    """写入日志文件并刷新，同时打印到 stderr 供服务端日志可见。"""
    global log_file
    if log_file:
        log_file.write(line + "\n")
        log_file.flush()
    print(line, file=sys.stderr, flush=True)


def open_log() -> None:
    global log_file, log_path
    try:
        log_dir = os.path.dirname(log_path)
        if log_dir and not os.path.isdir(log_dir):
            os.makedirs(log_dir, exist_ok=True)
        log_file = open(log_path, "a", encoding="utf-8")
    except OSError as e:
        print(f"无法打开日志文件 {log_path}: {e}", file=sys.stderr, flush=True)
        log_file = None


def handle_config(cfg: dict) -> None:
    global log_path
    if "log_file" in cfg:
        log_path = cfg["log_file"]
    if log_file:
        log_file.close()
    open_log()


def handle_on_open(params: dict) -> None:
    ch = params["channel_id"]
    channels[ch] = {
        "remote_addr": params.get("remote_addr", "?"),
        "local_port": params.get("local_port", "?"),
        "proto": params.get("proto", "tcp"),
        "start": datetime.now(TZ),
        "rx": 0,
        "tx": 0,
    }
    write_log(f'OPEN  {params.get("remote_addr","?")} → :{params.get("local_port","?")}  '
              f'ch={ch}  proto={params.get("proto","tcp")}')


def handle_on_data(params: dict) -> None:
    ch = params["channel_id"]
    if ch not in channels:
        return
    direction = params.get("dir", "rx")
    n = params.get("bytes", 0)
    if direction == "rx":
        channels[ch]["rx"] += n
    else:
        channels[ch]["tx"] += n


def handle_on_close(params: dict) -> None:
    ch = params["channel_id"]
    if ch not in channels:
        return
    info = channels.pop(ch)
    duration = (datetime.now(TZ) - info["start"]).total_seconds()
    ts = info["start"].strftime("%Y-%m-%d %H:%M:%S")

    # 访问日志行:  remote → :port  req↑ / resp↓  dur  [time]
    line = (f'{info["remote_addr"]} → :{info["local_port"]}  '
            f'{fmt_bytes(info["rx"])}↑ / {fmt_bytes(info["tx"])}↓  '
            f'{fmt_duration(duration)}  [{ts}]')
    write_log(f"CLOSE {line}")


# ── 握手 ──────────────────────────────────────────────────
print(json.dumps({
    "name": "web-access-log",
    "version": "1.0",
    "hooks": ["on_open", "on_close", "on_data"],
}), flush=True)

open_log()
write_log(f"--- web-access-log started at {datetime.now(TZ).strftime('%Y-%m-%d %H:%M:%S')} ---")

# ── 主循环 ────────────────────────────────────────────────
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except json.JSONDecodeError:
        continue

    method = msg.get("method", "")
    req_id = msg.get("id", 0)
    params = msg.get("params", {})

    if method == "config":
        handle_config(params)
        result = {"ok": True}
    elif method == "on_open":
        handle_on_open(params)
        result = None
    elif method == "on_data":
        handle_on_data(params)
        result = None
    elif method == "on_close":
        handle_on_close(params)
        result = None
    else:
        result = None

    # 有 id 的请求才回复
    if req_id > 0:
        resp = {"id": req_id}
        if result is not None:
            resp["result"] = result
        print(json.dumps(resp), flush=True)

# 清理
if log_file:
    log_file.close()
