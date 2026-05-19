# p2p-tun

高性能 NAT 穿透工具，使用 Go 编写 (v2.1)。通过 STUN 或中继服务器将本地服务暴露到公网。

[English](README.md)

## 功能特性

- **多层 NAT 穿透**：STUN（完全锥形 NAT）→ 中继回退
- **多协议支持**：TCP、UDP 或同时支持
- **配置文件模式**：通过 TOML 配置文件启动（类似 frp）
- **实时 Web GUI**：交互式 GUI 或仅监控 GUI
- **安全认证**：PSK 认证、IP 白名单/黑名单
- **流量控制**：连接数限制、带宽限速
- **数据压缩**：LZ4 压缩，减少带宽占用
- **动态插件系统**：加载外部插件（Python、Go、Node.js、Bash）
- **自动重连**：客户端指数退避自动重连；服务端断开后保留端口 30 秒
- **跨平台**：Windows、Linux、macOS

## 架构

```
┌─────────────────┐         ┌─────────────────┐         ┌─────────────────┐
│   目标服务      │ ◄─────► │   p2p-tun       │ ◄─────► │   中继服务器    │
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
./p2p-tun -local 8080,22,3306 -port 32001,32002,32003 -relay your-vps.com:9000

# RDP 同时使用 TCP 和 UDP
./p2p-tun -local 3389 -relay your-vps.com:9000 -proto both

# 带认证和压缩
./p2p-tun -local 8080 -relay your-vps.com:9000 -auth-key your-secret-key -compress

# 启动交互式 GUI
./p2p-tun -gui

# 通过配置文件启动（带监控 GUI）
./p2p-tun -c config.toml
```

## 命令行参数

### 客户端 (p2p-tun)

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-c` | - | 配置文件路径 (.toml)，见[配置文件模式](#配置文件模式) |
| `-local` | `8080` | 本地服务端口（逗号分隔） |
| `-port` | `0` | 中继服务器公网端口（0=随机分配 32000-33000） |
| `-target` | `127.0.0.1` | 目标主机地址（逗号分隔，默认本机） |
| `-stun` | - | STUN 服务器地址（留空=不使用，如 stun.l.google.com:19302） |
| `-stun2` | `stun1.l.google.com:19302` | 第二个 STUN 服务器（用于 NAT 类型检测） |
| `-nat-type` | - | 手动指定 NAT 类型覆盖检测结果 (full-cone/restricted/port-restricted/symmetric) |
| `-relay` | - | 中继服务器地址（ip:port） |
| `-proto` | `tcp` | 协议：`tcp`、`udp` 或 `both` |
| `-auth-key` | - | 认证密钥（需与服务端一致） |
| `-compress` | `false` | 启用 LZ4 压缩 |
| `-ip-allow` | - | IP 白名单（CIDR，逗号分隔） |
| `-ip-deny` | - | IP 黑名单（CIDR，逗号分隔） |
| `-max-conns` | `0` | 最大并发连接数（0=不限） |
| `-rate-limit` | `0` | 带宽限制（字节/秒，0=不限） |
| `-gui` | `false` | 启动交互式 Web GUI（端口 19999） |
| `-gui-token` | - | GUI 认证令牌（留空自动生成） |
| `-verbose` | `false` | 输出调试日志 |

### 服务端 (signal-server)

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-stun-port` | `0` | STUN 服务端口（0=不启动） |
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

## 配置文件模式

通过 TOML 配置文件启动（类似 frp），支持多服务配置：

```bash
./p2p-tun -c config.toml
```

### 配置文件格式

```toml
# 全局配置
local = "8080"                        # 本地服务端口
port = "0"                            # 公网端口，0=随机分配（32000-33000）
target = "127.0.0.1"                  # 目标主机
stun = "stun.l.google.com:19302"      # 启用 STUN
stun2 = "stun1.l.google.com:19302"    # 第二个 STUN 服务器
nat_type = ""                         # 手动指定 NAT 类型
relay = "myvps.com:9000"             # 中继服务器
proto = "tcp"                         # 协议
auth_key = "mysecret123"             # 认证密钥
compress = false                      # 压缩
ip_allow = ""                         # IP 白名单
ip_deny = ""                          # IP 黑名单
max_conns = 0                         # 最大连接数
rate_limit = 0                        # 带宽限制
verbose = false                       # 调试日志
gui = true                            # 启用监控 GUI
gui_port = 19998                      # 监控 GUI 端口（默认 19998）

# 多服务配置（每个 service 可指定不同的 target）
[[service]]
local = "80"
target = "192.168.1.100"
port = "32005"

[[service]]
local = "3306"
target = "192.168.1.200"
port = "33060"
proto = "tcp"
```

### 配置模式行为

- 加载配置后自动建立隧道连接
- 当 `gui = true` 时，启动**仅监控 GUI**（端口 19998，无配置面板）
- 支持 `[[service]]` 数组配置多服务，每个服务可指定不同目标

## Web GUI

### 交互式 GUI（`-gui` 模式）

```bash
./p2p-tun -gui
```

在浏览器打开 http://127.0.0.1:19999，输入控制台显示的令牌。

功能：
- 实时连接监控
- 流量统计和图表
- 连接管理
- 配置面板（启动/停止隧道、修改参数）
- 日志查看

### 监控 GUI（`-c` 配置模式）

```bash
./p2p-tun -c config.toml  # 配置文件中 gui = true
```

在浏览器打开 http://127.0.0.1:19998，输入控制台显示的令牌。

功能：
- 状态卡片（运行状态、模式、NAT 类型、运行时长、端口）
- 实时流量图表
- 活跃连接列表
- 端口映射展示
- 日志查看

### GUI 对比

| 功能 | 交互式 GUI (19999) | 监控 GUI (19998) |
|------|-------------------|-----------------|
| 配置面板 | ✅ | ❌ |
| 启动/停止隧道 | ✅ | ❌ |
| 状态卡片 | ✅ | ✅ |
| 流量图表 | ✅ | ✅ |
| 连接列表 | ✅ | ✅ |
| 端口映射 | ✅ | ✅ |
| 日志查看 | ✅ | ✅ |
| 隧道启动方式 | 手动 | 自动 |

## 重连机制

### 客户端重连
- 连接断开后自动重连，指数退避（3s → 6s → 12s → ... → 最大 60s）
- 若所有端口启动失败，客户端主动断开并重试

### 服务端端口保留
- 客户端断开后，服务端保留端口映射 30 秒
- 同一客户端（通过认证密钥标识）在 30 秒内重连，复用旧端口
- 超过 30 秒后，保留的端口被释放
- 服务端在 Linux 上使用 `SO_REUSEADDR` 处理 TIME_WAIT 端口

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
# 访问：http://your-vps.com:<分配的端口>
```

### 转发到远程主机

```bash
# 转发到其他主机而非本机
./p2p-tun -local 8080 -target 192.168.1.100 -relay your-vps.com:9000
# 访问：http://your-vps.com:<分配的端口> -> 192.168.1.100:8080

# 多端口分别指定不同目标
./p2p-tun -local 8080,3306 -target 192.168.1.100,10.0.0.5 -relay your-vps.com:9000
```

### 指定公网端口

```bash
# 指定公网端口
./p2p-tun -local 8080 -port 32005 -relay your-vps.com:9000
# 访问：http://your-vps.com:32005

# 让服务端自动分配（32000-33000 范围）
./p2p-tun -local 8080 -relay your-vps.com:9000
# 输出：端口映射: VPS:32456 -> 127.0.0.1:8080 (tcp)
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
./p2p-tun -local 3389 -relay your-vps.com:9000 -proto both
# 连接：your-vps.com:<分配的端口>
```

### 多服务

```bash
# Web + SSH + MySQL
./p2p-tun -local 8080,22,3306 -port 32001,32002,32003 -relay your-vps.com:9000
```

### 配置文件模式

```bash
# 单服务
./p2p-tun -c config.toml

# 多服务分别指定不同目标（通过 TOML 配置）
# 见[配置文件模式](#配置文件模式)章节
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
├── config.go            # TOML 配置文件解析
├── go.mod               # Go 模块定义
├── stun/
│   └── stun.go          # STUN 协议实现
├── forward/
│   └── forward.go       # TCP/UDP 端口转发
├── keepalive/
│   └── keepalive.go     # 连接保活
├── relay/
│   └── relay.go         # 中继客户端
├── signal-server/
│   ├── main.go          # 中继服务端
│   ├── reuseaddr_linux.go  # Linux SO_REUSEADDR 支持
│   └── reuseaddr_other.go  # 其他平台默认 TCP 监听
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

客户端配置文件模式的 systemd 服务：

```ini
[Unit]
Description=p2p-tun Client
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/p2p-tun
ExecStart=/opt/p2p-tun/p2p-tun -c /opt/p2p-tun/config.toml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## 许可证

MIT License

## 贡献

欢迎提交 Pull Request。重大改动请先开 Issue 讨论。
