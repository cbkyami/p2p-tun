# p2p-tun

高性能 NAT 穿透工具，使用 Go 编写。通过 UPnP、STUN 或中继服务器将本地服务暴露到公网。

[English](README.md)

## 功能特性

- **多层 NAT 穿透**：UPnP → STUN（完全锥形 NAT）→ 中继回退
- **多协议支持**：TCP、UDP 或同时支持
- **实时 Web GUI**：实时监控连接、流量和状态
- **安全认证**：PSK 认证、IP 白名单/黑名单
- **流量控制**：连接数限制、带宽限速
- **数据压缩**：LZ4 压缩，减少带宽占用
- **动态插件系统**：加载外部插件（Python、Go、Node.js、Bash）
- **跨平台**：Windows、Linux、macOS

## 架构

```
┌─────────────────┐         ┌─────────────────┐         ┌─────────────────┐
│   本地服务      │ ◄─────► │   p2p-tun       │ ◄─────► │   中继服务器    │
│ (127.0.0.1)     │         │   (客户端)      │         │   (VPS)         │
└─────────────────┘         └─────────────────┘         └─────────────────┘
                                    │
                                    ▼
                            ┌─────────────────┐
                            │   公网用户      │
                            │   (Internet)    │
                            └─────────────────┘
```

## 安装

### 从源码编译

```bash
# 克隆仓库
git clone https://github.com/cbkyami/p2p-tun.git
cd p2p-tun

# 编译客户端
go build -o p2p-tun .

# 编译服务端
go build -o signal-server ./signal-server/

# 交叉编译 Linux 版本
GOOS=linux GOARCH=amd64 go build -o signal-server-linux ./signal-server/
```

### 下载预编译二进制

从 [Releases](https://github.com/cbkyami/p2p-tun/releases) 页面下载。

## 快速开始

### 1. 启动中继服务器（在 VPS 上）

```bash
# 基本用法
./signal-server -relay-port 9000 -public-addr your-domain.com

# 带认证
./signal-server -relay-port 9000 -auth-key your-secret-key -public-addr your-domain.com

# 加载动态插件
./signal-server -relay-port 9000 -plugin-dir ./plugins

# 带全局限制
./signal-server -relay-port 9000 -max-conns 1000 -rate-limit 1048576
```

### 2. 启动客户端

```bash
# 基本用法 - 暴露本地 8080 端口
./p2p-tun -local 8080 -relay your-vps.com:9000

# 多端口
./p2p-tun -local 8080,22,3306 -port 8080,22022,23306 -relay your-vps.com:9000

# RDP 同时使用 TCP 和 UDP
./p2p-tun -local 3389 -port 3389 -relay your-vps.com:9000 -proto both

# 带认证和压缩
./p2p-tun -local 8080 -relay your-vps.com:9000 -auth-key your-secret-key -compress

# 启动 GUI
./p2p-tun -gui
```

## 命令行参数

### 客户端 (p2p-tun)

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-local` | `8080` | 本地服务端口（逗号分隔） |
| `-port` | `0` | 中继服务器公网端口（0=自动匹配本地端口） |
| `-upnp` | `false` | 启用 UPnP 端口映射（默认关闭） |
| `-stun` | - | STUN 服务器地址（留空=不使用，如 stun.l.google.com:19302） |
| `-relay` | - | 中继服务器地址（ip:port） |
| `-proto` | `tcp` | 协议：`tcp`、`udp` 或 `both` |
| `-auth-key` | - | 认证密钥（需与服务端一致） |
| `-compress` | `false` | 启用 LZ4 压缩 |
| `-ip-allow` | - | IP 白名单（CIDR，逗号分隔） |
| `-ip-deny` | - | IP 黑名单（CIDR，逗号分隔） |
| `-max-conns` | `0` | 最大并发连接数（0=不限） |
| `-rate-limit` | `0` | 带宽限制（字节/秒，0=不限） |
| `-gui` | `false` | 启动 Web GUI |
| `-gui-token` | - | GUI 认证令牌（留空自动生成） |
| `-verbose` | `false` | 输出调试日志 |

### 服务端 (signal-server)

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-stun-port` | `3478` | STUN 服务端口 |
| `-relay-port` | `9000` | 中继控制端口 |
| `-public-addr` | - | 公网地址（用于显示） |
| `-auth-key` | - | 客户端认证密钥 |
| `-ip-allow` | - | 全局 IP 白名单 |
| `-ip-deny` | - | 全局 IP 黑名单 |
| `-max-conns` | `0` | 全局最大连接数 |
| `-rate-limit` | `0` | 全局带宽限制 |
| `-compress` | `false` | 启用压缩 |
| `-traffic-log` | - | 流量日志文件路径 |
| `-plugin-dir` | - | 动态插件目录 |
| `-plugin-timeout` | `5s` | 插件调用超时 |
| `-verbose` | `false` | 输出调试日志 |

## Web GUI

启动 GUI：

```bash
./p2p-tun -gui
```

然后在浏览器打开 http://127.0.0.1:19999，输入控制台显示的令牌。

### GUI 功能

- 实时连接监控
- 流量统计和图表
- 连接管理
- 配置面板
- 日志查看

## 插件系统

### 内置插件

| 插件 | 说明 |
|------|------|
| **IPFilter** | IP 白名单/黑名单过滤 |
| **ConnLimit** | 并发连接数限制 |
| **RateLimit** | 带宽限速 |
| **Compression** | LZ4 数据压缩 |
| **TrafficLog** | 流量日志记录 |

### 动态插件

p2p-tun 支持运行时加载外部插件。插件可以用任何能读写 stdin/stdout 的语言编写（Python、Go、Node.js、Bash 等）。

#### 加载插件

```bash
# 加载目录下所有插件
./signal-server -relay-port 9000 -plugin-dir ./plugins

# 加载单个插件
./signal-server -relay-port 9000 -plugin-dir ./plugins/geoip-filter
```

#### 插件配置

每个插件目录包含 `plugin.json`：

```json
{
  "name": "geoip-filter",
  "version": "1.0",
  "type": "filter",
  "hooks": ["on_accept"],
  "exec": "python3 plugin.py",
  "enabled": true,
  "config": {
    "database": "GeoLite2-Country.mmdb",
    "deny_countries": "CN,RU,KP"
  }
}
```

| 字段 | 类型 | 必需 | 默认值 | 说明 |
|------|------|------|--------|------|
| `name` | string | ✅ | - | 插件名称 |
| `version` | string | ✅ | - | 插件版本 |
| `type` | string | ✅ | - | 插件类型：`filter`/`logger`/`alerting` |
| `hooks` | []string | ✅ | - | 支持的 Hook |
| `exec` | string | ✅ | - | 执行命令 |
| `enabled` | bool | ❌ | `true` | 是否启用 |
| `config` | object | ❌ | `{}` | 插件配置 |

#### 可用 Hook

| Hook | 触发时机 | 参数 |
|------|----------|------|
| `on_accept` | 新连接接入 | `proto`, `addr` |
| `on_open` | 通道建立 | `proto`, `remote_addr`, `channel_id`, `local_port` |
| `on_close` | 通道关闭 | `channel_id` |
| `on_data` | 数据传输 | `channel_id`, `dir`, `bytes` |

#### 示例插件

见 `plugins/` 目录：

- `geoip-filter/` - GeoIP 国家过滤（Python）
- `conn-timeout/` - 连接空闲超时监控（Python）

详细插件开发指南见 [plugins/PLUGIN_DEV.md](plugins/PLUGIN_DEV.md)。

## 使用示例

### 暴露 Web 服务器

```bash
# 本地 Web 服务在 8080 端口
./p2p-tun -local 8080 -relay your-vps.com:9000
# 访问：http://your-vps.com:8080
```

### SSH 远程访问

```bash
# 暴露 SSH 端口
./p2p-tun -local 22 -port 22022 -relay your-vps.com:9000
# 连接：ssh -p 22022 user@your-vps.com
```

### 远程桌面（RDP）

```bash
# RDP 同时使用 TCP + UDP 以获得更好性能
./p2p-tun -local 3389 -port 3389 -relay your-vps.com:9000 -proto both
# 连接：your-vps.com:3389
```

### 多服务

```bash
# Web + SSH + MySQL
./p2p-tun -local 8080,22,3306 -port 80,22022,23306 -relay your-vps.com:9000
```

### 安全配置

```bash
# 服务端
./signal-server -relay-port 9000 -auth-key mySecretKey123 -max-conns 100

# 客户端
./p2p-tun -local 8080 -relay your-vps.com:9000 -auth-key mySecretKey123 -ip-allow 10.0.0.0/8,192.168.0.0/16 -max-conns 10
```

### 使用 GeoIP 过滤

```bash
# 安装 geoip2
pip install geoip2

# 下载 GeoLite2 数据库
wget https://git.io/GeoLite2-Country.mmdb -O plugins/geoip-filter/GeoLite2-Country.mmdb

# 编辑插件配置
vim plugins/geoip-filter/plugin.json  # 设置 deny_countries

# 启动服务端并加载插件
./signal-server -relay-port 9000 -plugin-dir ./plugins/geoip-filter
```

## 项目结构

```
p2p-tun/
├── main.go              # 主程序入口
├── go.mod               # Go 模块定义
├── stun/
│   └── stun.go          # STUN 协议实现
├── upnp/
│   └── upnp.go          # UPnP/NAT-PMP 端口映射
├── forward/
│   └── forward.go       # TCP/UDP 端口转发
├── keepalive/
│   └── keepalive.go     # 连接保活
├── relay/
│   └── relay.go         # 中继客户端
├── signal-server/
│   └── main.go          # 中继服务端
├── logutil/
│   └── logutil.go       # 日志工具
├── plugin/
│   ├── plugin.go        # 插件接口
│   ├── compression.go   # LZ4 压缩
│   ├── connlimit.go     # 连接限制
│   ├── ipfilter.go      # IP 过滤
│   ├── ratelimit.go     # 速率限制
│   └── trafficlog.go    # 流量日志
├── dynplugin/
│   ├── protocol.go      # 动态插件协议
│   ├── process.go       # 插件进程管理
│   ├── manager.go       # 插件管理器
│   ├── adapter.go       # 插件适配器
│   └── sdk.go           # Go 插件 SDK
└── plugins/             # 示例插件
    ├── PLUGIN_DEV.md    # 插件开发指南
    ├── geoip-filter/    # GeoIP 过滤（Python）
    ├── ip-blacklist/    # IP 黑名单（Python）
    └── ...
```

## Systemd 服务（Linux）

创建 `/etc/systemd/system/signal-server.service`：

```ini
[Unit]
Description=p2p-tun Signal Server
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/p2p-tun
ExecStart=/opt/p2p-tun/signal-server -relay-port 9000 -public-addr your-domain.com -auth-key your-secret-key
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable signal-server
systemctl start signal-server
```

## 许可证

MIT License

## 贡献

欢迎提交 Pull Request。重大改动请先开 Issue 讨论。
