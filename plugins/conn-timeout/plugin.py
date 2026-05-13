#!/usr/bin/env python3
"""p2p-tun 动态插件: 连接空闲超时

监控连接的空闲时间，超过阈值后告警，告警后继续空闲则断开连接。

工作流程:
  1. 连接建立 (on_open) -> 记录连接信息
  2. 数据传输 (on_data) -> 更新最后活跃时间
  3. 空闲超时 -> 告警 (WARN 日志)
  4. 告警后继续空闲 timeout_seconds -> 通过 on_check 返回 action:close 断开
  5. 告警后有数据传输 -> 重置状态，继续监控

用法:
  1. 修改 plugin.json 中的 timeout_seconds 配置
  2. 启动 signal-server 时添加 -plugin-dir ./plugins
"""

import sys
import json
import time
import threading


timeout_seconds = 300
connections = {}
warned = {}
lock = threading.RLock()


def on_open(params):
    ch = params.get("channel_id", 0)
    with lock:
        connections[ch] = {
            "open_time": time.time(),
            "last_data": time.time(),
            "proto": params.get("proto", "?"),
            "addr": params.get("remote_addr", "?")
        }
        if ch in warned:
            del warned[ch]
    print(f"[conn-timeout] INFO: on_open channel={ch} {params.get('proto','')} {params.get('remote_addr','')} (追踪中: {len(connections)}个连接)", file=sys.stderr)


def on_close(params):
    ch = params.get("channel_id", 0)
    with lock:
        if ch in connections:
            idle = int(time.time() - connections[ch]["last_data"])
            del connections[ch]
            print(f"[conn-timeout] INFO: on_close channel={ch} 最后空闲={idle}s (追踪中: {len(connections)}个连接)", file=sys.stderr)
        if ch in warned:
            del warned[ch]


def on_data(params):
    ch = params.get("channel_id", 0)
    with lock:
        if ch in connections:
            connections[ch]["last_data"] = time.time()
        if ch in warned:
            del warned[ch]


def on_check(params):
    now = time.time()
    to_close = []
    with lock:
        for ch, info in list(connections.items()):
            idle = now - info["last_data"]
            if ch in warned:
                warn_time = warned[ch]
                if now - warn_time >= timeout_seconds:
                    ts = time.strftime("%Y-%m-%d %H:%M:%S")
                    print(f"[{ts}] [conn-timeout] CLOSE: channel={ch} idle={int(idle)}s, 告警后继续空闲 {int(now - warn_time)}s, 断开连接 ({info['proto']} {info['addr']})", file=sys.stderr)
                    to_close.append(ch)
                    del warned[ch]
            elif idle >= timeout_seconds:
                warned[ch] = now
                ts = time.strftime("%Y-%m-%d %H:%M:%S")
                print(f"[{ts}] [conn-timeout] WARN: channel={ch} idle={int(idle)}s >= {timeout_seconds}s, {timeout_seconds}s 后将断开 ({info['proto']} {info['addr']})", file=sys.stderr)
    result = {}
    if to_close:
        result = {"action": "close", "channels": to_close}
    return result


def main():
    global timeout_seconds

    handshake = {
        "name": "conn-timeout",
        "version": "1.0",
        "hooks": ["on_open", "on_close", "on_data", "on_check"]
    }
    print(json.dumps(handshake), flush=True)

    config_line = sys.stdin.readline()
    try:
        config = json.loads(config_line)
        data = config.get("data", {})
        timeout_seconds = data.get("timeout_seconds", 300)
    except (json.JSONDecodeError, KeyError):
        pass

    print(f"[conn-timeout] INFO: 超时时间 {timeout_seconds}s, 服务端将周期性调用 on_check", file=sys.stderr)

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            print(f"[conn-timeout] WARN: 收到无效JSON: {line[:80]}", file=sys.stderr)
            continue

        method = req.get("method", "")
        params = req.get("params", {})
        req_id = req.get("id", 0)

        if method == "on_open":
            on_open(params)
        elif method == "on_close":
            on_close(params)
        elif method == "on_data":
            on_data(params)
        elif method == "on_check":
            result = on_check(params)
            if req_id > 0:
                resp = {"id": req_id, "result": result}
                print(json.dumps(resp), flush=True)
            continue

        if req_id > 0:
            resp = {"id": req_id, "result": {}}
            print(json.dumps(resp), flush=True)


if __name__ == "__main__":
    main()
