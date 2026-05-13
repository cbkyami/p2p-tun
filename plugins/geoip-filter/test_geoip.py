#!/usr/bin/env python3
"""GeoIP 数据库测试脚本

测试 IP 地址在 GeoLite2 数据库中的国家归属。

用法:
  python3 test_geoip.py                    # 测试你的公网 IP
  python3 test_geoip.py 1.2.3.4            # 测试指定 IP
  python3 test_geoip.py 1.2.3.4 5.6.7.8    # 测试多个 IP
"""

import sys
import urllib.request

try:
    import geoip2.database
except ImportError:
    print("错误: 需要安装 geoip2 库")
    print("运行: pip install geoip2")
    sys.exit(1)

DB_PATH = "GeoLite2-Country.mmdb"

def get_public_ip():
    try:
        return urllib.request.urlopen("https://api.ipify.org", timeout=5).read().decode()
    except Exception:
        try:
            return urllib.request.urlopen("https://ifconfig.me/ip", timeout=5).read().decode()
        except Exception:
            return None

def lookup_ip(reader, ip):
    try:
        resp = reader.country(ip)
        country = resp.country.iso_code or "未知"
        name = resp.country.name or "未知"
        return country, name
    except Exception as e:
        return None, str(e)

def main():
    ips = sys.argv[1:]
    
    if not ips:
        print("正在获取你的公网 IP...")
        ip = get_public_ip()
        if ip:
            print(f"公网 IP: {ip}")
            ips = [ip]
        else:
            print("无法获取公网 IP，请手动指定")
            sys.exit(1)
    
    try:
        reader = geoip2.database.Reader(DB_PATH)
    except Exception as e:
        print(f"错误: 无法打开数据库 {DB_PATH}: {e}")
        sys.exit(1)
    
    print(f"\n数据库: {DB_PATH}")
    print("-" * 50)
    
    for ip in ips:
        country, name = lookup_ip(reader, ip)
        if country:
            print(f"IP: {ip:20} -> 国家: {country} ({name})")
        else:
            print(f"IP: {ip:20} -> 错误: {name}")
    
    reader.close()

if __name__ == "__main__":
    main()
