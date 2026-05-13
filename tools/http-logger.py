#!/usr/bin/env python3
"""
HTTP 访问日志嗅探器 — 夹在 p2p-tun 和本地 web 服务之间，输出 Apache Combined 格式日志。

用法:
  python http-logger.py --listen 8081 --target 8080
  p2p-tun.exe -local 8081 ...   (代替 -local 8080)

日志格式:
  来源IP - - [日/月/年:时:分:秒 时区] "METHOD /path HTTP/1.x" 状态码 响应字节数
"""

import argparse
import asyncio
import re
import socket
import sys
import time
from datetime import datetime, timezone, timedelta

# ── 配置 ──────────────────────────────────────────────────
parser = argparse.ArgumentParser(description="HTTP 访问日志嗅探器")
parser.add_argument("--listen", type=int, default=8081, help="监听端口 (p2p-tun 的 -local 参数)")
parser.add_argument("--target", type=int, default=8080, help="目标 web 服务端口")
parser.add_argument("--log", type=str, default="http-access.log", help="日志文件路径")
parser.add_argument("--no-stdout", action="store_true", help="不输出到终端")
args = parser.parse_args()

LISTEN_HOST = "127.0.0.1"
TARGET_HOST = "127.0.0.1"
TZ = timezone(timedelta(hours=8))  # UTC+8 中国时区

log_fp = open(args.log, "a", encoding="utf-8")


def log(msg: str) -> None:
    """同时写入文件和终端。"""
    log_fp.write(msg + "\n")
    log_fp.flush()
    if not args.no_stdout:
        print(msg, flush=True)


# ── HTTP 请求/响应解析 ────────────────────────────────────

# HTTP 请求行: METHOD /path HTTP/1.x\r\n
REQ_LINE_RE = re.compile(rb"^(\S+)\s+(\S+)\s+HTTP/(\d+\.\d+)\r\n")

# HTTP 响应行: HTTP/1.x STATUS_CODE Reason\r\n
RESP_LINE_RE = re.compile(rb"^HTTP/(\d+\.\d+)\s+(\d{3})\s")


async def pipe_and_log(
    reader_from: asyncio.StreamReader,
    writer_to: asyncio.StreamWriter,
) -> int:
    """单向转发数据，返回转发的总字节数。"""
    total = 0
    try:
        while True:
            data = await reader_from.read(65536)
            if not data:
                break
            writer_to.write(data)
            await writer_to.drain()
            total += len(data)
    except (ConnectionResetError, BrokenPipeError, OSError):
        pass
    return total


async def handle_conn(
    client_reader: asyncio.StreamReader,
    client_writer: asyncio.StreamWriter,
) -> None:
    """处理一个客户端连接。"""
    peer = client_writer.get_extra_info("peername")
    client_addr = f"{peer[0]}:{peer[1]}" if peer else "?"

    # ── 连接目标服务 ───────────────────────────────────
    try:
        target_reader, target_writer = await asyncio.wait_for(
            asyncio.open_connection(TARGET_HOST, args.target), timeout=5
        )
    except (ConnectionRefusedError, asyncio.TimeoutError, OSError):
        client_writer.close()
        return

    # ── 读 HTTP 请求行 ─────────────────────────────────
    request_line = b""
    try:
        request_line = await asyncio.wait_for(
            client_reader.readline(), timeout=10
        )
    except asyncio.TimeoutError:
        client_writer.close()
        target_writer.close()
        return

    if not request_line:
        client_writer.close()
        target_writer.close()
        return

    # 转发请求行到目标
    target_writer.write(request_line)

    # 解析请求行
    method, path, http_ver = "-", "-", "-"
    m = REQ_LINE_RE.match(request_line)
    if m:
        method = m.group(1).decode("ascii", errors="replace")
        path = m.group(2).decode("ascii", errors="replace")
        http_ver = m.group(3).decode("ascii", errors="replace")

    # ── 读并转发请求头 ─────────────────────────────────
    header_bytes = b""
    content_length = 0
    while True:
        try:
            line = await asyncio.wait_for(client_reader.readline(), timeout=5)
        except asyncio.TimeoutError:
            break
        header_bytes += line
        target_writer.write(line)
        if line == b"\r\n":
            break
        # 检查 Content-Length
        if line.lower().startswith(b"content-length:"):
            try:
                content_length = int(line.split(b":", 1)[1].strip())
            except ValueError:
                pass

    await target_writer.drain()

    # ── 读并转发请求体 ─────────────────────────────────
    if content_length > 0:
        body_read = 0
        while body_read < content_length:
            chunk = await client_reader.read(min(content_length - body_read, 65536))
            if not chunk:
                break
            target_writer.write(chunk)
            body_read += len(chunk)
        await target_writer.drain()

    # ── 读 HTTP 响应（状态行 + 头 + 体）───────────────
    response_line = b""
    status_code = "-"
    try:
        response_line = await asyncio.wait_for(
            target_reader.readline(), timeout=30
        )
    except asyncio.TimeoutError:
        pass

    if not response_line:
        client_writer.close()
        target_writer.close()
        return

    client_writer.write(response_line)

    # 解析状态码
    m = RESP_LINE_RE.match(response_line)
    if m:
        status_code = m.group(2).decode()

    # 读并转发响应头
    resp_content_length = 0
    is_chunked = False
    while True:
        try:
            line = await asyncio.wait_for(target_reader.readline(), timeout=10)
        except asyncio.TimeoutError:
            break
        client_writer.write(line)
        if line == b"\r\n":
            break
        low = line.lower()
        if low.startswith(b"content-length:"):
            try:
                resp_content_length = int(line.split(b":", 1)[1].strip())
            except ValueError:
                pass
        elif low.startswith(b"transfer-encoding:") and b"chunked" in low:
            is_chunked = True

    await client_writer.drain()

    # ── 双向转发剩余数据 ───────────────────────────────
    # 先用一个 task 读取所有响应数据并计数
    async def relay_response() -> int:
        total = 0
        try:
            while True:
                data = await target_reader.read(65536)
                if not data:
                    break
                client_writer.write(data)
                await client_writer.drain()
                total += len(data)
        except (ConnectionResetError, BrokenPipeError, OSError):
            pass
        return total

    # 同时不阻塞地处理客户端到目标的残余数据
    async def relay_request() -> int:
        total = 0
        try:
            while True:
                data = await client_reader.read(65536)
                if not data:
                    break
                target_writer.write(data)
                await target_writer.drain()
                total += len(data)
        except (ConnectionResetError, BrokenPipeError, OSError):
            pass
        return total

    # 并发转发
    resp_task = asyncio.create_task(relay_response())
    req_task = asyncio.create_task(relay_request())

    resp_bytes = await resp_task
    # req_task 可能仍在跑，取消它
    req_task.cancel()
    try:
        await req_task
    except asyncio.CancelledError:
        pass

    # ── 写日志 ─────────────────────────────────────────
    now = datetime.now(TZ)
    ts = now.strftime("%d/%b/%Y:%H:%M:%S %z")
    body_size = resp_bytes if resp_bytes > 0 else "-"

    log(f'{client_addr} - - [{ts}] "{method} {path} HTTP/{http_ver}" {status_code} {body_size}')

    # 清理
    try:
        client_writer.close()
        target_writer.close()
    except OSError:
        pass


async def main() -> None:
    server = await asyncio.start_server(handle_conn, LISTEN_HOST, args.listen)
    addr = server.sockets[0].getsockname()
    log(f"--- HTTP Logger 启动: 监听 {addr[0]}:{addr[1]} → {TARGET_HOST}:{args.target} ---")

    async with server:
        await server.serve_forever()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        log("--- HTTP Logger 已停止 ---")
        log_fp.close()
