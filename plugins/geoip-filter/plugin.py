#!/usr/bin/env python3
"""p2p-tun 动态插件: GeoIP 国家过滤器

根据 IP 地址所属国家过滤连接。需要 GeoLite2 数据库文件。

下载 GeoLite2-Country.mmdb:
  https://dev.maxmind.com/geoip/geolite2-free-geolocation-data

用法:
  1. 下载 GeoLite2-Country.mmdb 放到本目录
  2. 修改 plugin.json 中的 deny_countries 配置
  3. 启动 signal-server 时添加 -plugin-dir ./plugins
"""

import sys
import json
import struct
import socket

try:
    import geoip2.database
    HAS_GEOIP = True
except ImportError:
    HAS_GEOIP = False
    print("[geoip-filter] WARN: geoip2 模块未安装，请运行: pip install geoip2", file=sys.stderr)


reader = None
deny_countries = []


def get_country_code(ip_str):
    global reader
    if not reader or not HAS_GEOIP:
        return None
    try:
        resp = reader.country(ip_str)
        return resp.country.iso_code
    except Exception:
        return None


def on_accept(params):
    addr = params.get("addr", "")
    ip = addr.split(":")[0] if ":" in addr else addr.split("]")[0].lstrip("[") if "]" in addr else addr

    country = get_country_code(ip)
    if country and country in deny_countries:
        return {"allowed": False, "reason": f"Country {country} blocked (IP: {ip})"}

    return {"allowed": True}


def main():
    global reader, deny_countries

    handshake = {
        "name": "geoip-filter",
        "version": "1.0",
        "hooks": ["on_accept"]
    }
    print(json.dumps(handshake), flush=True)

    config_line = sys.stdin.readline()
    try:
        config = json.loads(config_line)
        data = config.get("data", {})

        db_path = data.get("database", "GeoLite2-Country.mmdb")
        deny_str = data.get("deny_countries", "")
        if deny_str:
            deny_countries = [c.strip().upper() for c in deny_str.split(",") if c.strip()]

        if HAS_GEOIP:
            try:
                reader = geoip2.database.Reader(db_path)
                print(f"[geoip-filter] INFO: 已加载数据库 {db_path}, 拒绝国家: {deny_countries}", file=sys.stderr)
            except Exception as e:
                print(f"[geoip-filter] ERROR: 加载数据库失败: {e}", file=sys.stderr)
    except (json.JSONDecodeError, KeyError):
        pass

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue

        method = req.get("method", "")
        req_id = req.get("id", 0)
        params = req.get("params", {})

        if method == "on_accept" and req_id > 0:
            result = on_accept(params)
            resp = {"id": req_id, "result": result}
            print(json.dumps(resp), flush=True)


if __name__ == "__main__":
    main()
