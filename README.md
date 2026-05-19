# p2p-tun

A high-performance NAT traversal tool written in Go (v2.1). Expose your local services to the public internet through STUN, or relay server.

[中文文档](README_CN.md)

## Features

- **Multi-layer NAT Traversal**: STUN (Full Cone NAT) → Relay fallback
- **Multi-protocol Support**: TCP, UDP, or both simultaneously
- **Configuration File Mode**: Launch via TOML config file (like frp)
- **Real-time Web GUI**: Interactive GUI or monitor-only GUI
- **Security**: PSK authentication, IP whitelist/blacklist
- **Traffic Control**: Connection limits, bandwidth throttling
- **Data Compression**: LZ4 compression for reduced bandwidth usage
- **Dynamic Plugin System**: Load external plugins (Python, Go, Node.js, Bash)
- **Auto Reconnect**: Client auto-reconnects with exponential backoff; server preserves ports for 30s
- **Cross-platform**: Windows, Linux, macOS

## Architecture

```
┌─────────────────┐         ┌─────────────────┐         ┌─────────────────┐
│  Target Service │ ◄─────► │   p2p-tun       │ ◄─────► │  Relay Server   │
│  (127.0.0.1)    │         │   (Client)      │         │   (VPS)         │
└─────────────────┘         └─────────────────┘         └─────────────────┘
                                    │
                                    ▼
                            ┌─────────────────┐
                            │  Public Users   │
                            │  (Internet)     │
                            └─────────────────┘
```

## Installation

### Build from Source

```bash
# Clone the repository
git clone https://github.com/cbkyami/p2p-tun.git
cd p2p-tun

# Build client
go build -o p2p-tun .

# Build server
go build -o signal-server ./signal-server/

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o signal-server-linux ./signal-server/
```

## Quick Start

### 1. Start Relay Server (on your VPS)

```bash
# Basic usage
./signal-server -relay-port 9000 -public-addr your-domain.com

# With authentication
./signal-server -relay-port 9000 -auth-key your-secret-key -public-addr your-domain.com

# With dynamic plugins
./signal-server -relay-port 9000 -plugin-dir ./plugins

# With global limits
./signal-server -relay-port 9000 -max-conns 1000 -rate-limit 1048576
```

### 2. Start Client

```bash
# Basic usage - expose local port 8080
./p2p-tun -local 8080 -relay your-vps.com:9000

# Multiple ports
./p2p-tun -local 8080,22,3306 -port 32001,32002,32003 -relay your-vps.com:9000

# RDP with both TCP and UDP
./p2p-tun -local 3389 -relay your-vps.com:9000 -proto both

# With authentication and compression
./p2p-tun -local 8080 -relay your-vps.com:9000 -auth-key your-secret-key -compress

# Launch interactive GUI
./p2p-tun -gui

# Launch via config file (with monitor GUI)
./p2p-tun -c config.toml
```

## Command Line Options

### Client (p2p-tun)

| Option | Default | Description |
|--------|---------|-------------|
| `-c` | - | Config file path (.toml), see [Config File Mode](#config-file-mode) |
| `-local` | `8080` | Local service ports (comma-separated) |
| `-port` | `0` | Public ports on relay server (0=random in 32000-33000) |
| `-target` | `127.0.0.1` | Target host addresses (comma-separated, default localhost) |
| `-stun` | - | STUN server address (empty=disabled, e.g. stun.l.google.com:19302) |
| `-stun2` | `stun1.l.google.com:19302` | Second STUN server for NAT type detection |
| `-nat-type` | - | Override NAT type detection (full-cone/restricted/port-restricted/symmetric) |
| `-relay` | - | Relay server address (ip:port) |
| `-proto` | `tcp` | Protocol: `tcp`, `udp`, or `both` |
| `-auth-key` | - | Authentication key (must match server) |
| `-compress` | `false` | Enable LZ4 compression |
| `-ip-allow` | - | IP whitelist (CIDR, comma-separated) |
| `-ip-deny` | - | IP blacklist (CIDR, comma-separated) |
| `-max-conns` | `0` | Max concurrent connections (0=unlimited) |
| `-rate-limit` | `0` | Bandwidth limit in bytes/sec (0=unlimited) |
| `-gui` | `false` | Launch interactive web GUI (port 19999) |
| `-gui-token` | - | GUI authentication token (auto-generated if empty) |
| `-verbose` | `false` | Enable debug logging |

### Server (signal-server)

| Option | Default | Description |
|--------|---------|-------------|
| `-stun-port` | `0` | STUN service port (0=disabled) |
| `-relay-port` | `9000` | Relay control port |
| `-public-addr` | - | Public address for display |
| `-auth-key` | - | Client authentication key |
| `-ip-allow` | - | Global IP whitelist |
| `-ip-deny` | - | Global IP blacklist |
| `-max-conns` | `0` | Global max connections |
| `-rate-limit` | `0` | Global bandwidth limit |
| `-compress` | `false` | Enable compression |
| `-traffic-log` | - | Traffic log file path |
| `-plugin-dir` | - | Dynamic plugin directory |
| `-plugin-timeout` | `5s` | Plugin call timeout |
| `-verbose` | `false` | Enable debug logging |

## Config File Mode

Launch via TOML config file (like frp), supporting multi-service configuration:

```bash
./p2p-tun -c config.toml
```

### Config File Format

```toml
# Global settings
local = "8080"                        # Local service port
port = "0"                            # Public port, 0=random (32000-33000)
target = "127.0.0.1"                  # Target host
stun = "stun.l.google.com:19302"      # Enable STUN
stun2 = "stun1.l.google.com:19302"    # Second STUN server
nat_type = ""                         # Override NAT type
relay = "myvps.com:9000"             # Relay server
proto = "tcp"                         # Protocol
auth_key = "mysecret123"             # Authentication key
compress = false                      # Compression
ip_allow = ""                         # IP whitelist
ip_deny = ""                          # IP blacklist
max_conns = 0                         # Max connections
rate_limit = 0                        # Bandwidth limit
verbose = false                       # Debug logging
gui = true                            # Enable monitor GUI
gui_port = 19998                      # Monitor GUI port (default 19998)

# Multi-service configuration (each service can have its own target)
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

### Config Mode Behavior

- Auto-starts tunnel connection after loading config
- When `gui = true`, launches **monitor-only GUI** on port 19998 (no configuration panel)
- Supports `[[service]]` arrays for multi-service with individual targets

## Web GUI

### Interactive GUI (`-gui` mode)

```bash
./p2p-tun -gui
```

Open http://127.0.0.1:19999 in your browser and enter the token displayed in the console.

Features:
- Real-time connection monitoring
- Traffic statistics and charts
- Connection management
- Configuration panel (start/stop tunnel, modify settings)
- Log viewer

### Monitor GUI (`-c` config mode)

```bash
./p2p-tun -c config.toml  # with gui = true in config
```

Open http://127.0.0.1:19998 in your browser and enter the token displayed in the console.

Features:
- Status cards (running state, mode, NAT type, uptime, ports)
- Real-time traffic chart
- Active connection list
- Port mapping display
- Log viewer

### GUI Comparison

| Feature | Interactive GUI (19999) | Monitor GUI (19998) |
|---------|------------------------|---------------------|
| Configuration panel | ✅ | ❌ |
| Start/Stop tunnel | ✅ | ❌ |
| Status cards | ✅ | ✅ |
| Traffic chart | ✅ | ✅ |
| Connection list | ✅ | ✅ |
| Port mapping | ✅ | ✅ |
| Log viewer | ✅ | ✅ |
| Tunnel startup | Manual | Auto |

## Reconnect Mechanism

### Client Reconnect
- Auto-reconnects on connection loss with exponential backoff (3s → 6s → 12s → ... → max 60s)
- If all ports fail on reconnect, client disconnects and retries

### Server Port Preservation
- When a client disconnects, the server preserves port mappings for 30 seconds
- If the same client (identified by auth key) reconnects within 30s, ports are reused
- After 30s, preserved ports are released
- Server uses `SO_REUSEADDR` on Linux to handle TIME_WAIT ports

## Plugin System

### Built-in Plugins

| Plugin | Description |
|--------|-------------|
| **IPFilter** | IP whitelist/blacklist filtering |
| **ConnLimit** | Concurrent connection limiting |
| **RateLimit** | Bandwidth throttling |
| **Compression** | LZ4 data compression |
| **TrafficLog** | Traffic logging to file |

### Dynamic Plugins

p2p-tun supports loading external plugins at runtime. Plugins can be written in any language that can read stdin and write stdout (Python, Go, Node.js, Bash, etc.).

#### Loading Plugins

```bash
# Load all plugins in directory
./signal-server -relay-port 9000 -plugin-dir ./plugins

# Load single plugin
./signal-server -relay-port 9000 -plugin-dir ./plugins/geoip-filter
```

#### Plugin Configuration

Each plugin directory contains a `plugin.json`:

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

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | ✅ | - | Plugin name |
| `version` | string | ✅ | - | Plugin version |
| `type` | string | ✅ | - | Plugin type: `filter`/`logger`/`alerting` |
| `hooks` | []string | ✅ | - | Supported hooks |
| `exec` | string | ✅ | - | Execute command |
| `enabled` | bool | ❌ | `true` | Enable/disable plugin |
| `config` | object | ❌ | `{}` | Plugin configuration |

#### Available Hooks

| Hook | Trigger | Params |
|------|---------|--------|
| `on_accept` | New connection | `proto`, `addr` |
| `on_open` | Channel opened | `proto`, `remote_addr`, `channel_id`, `local_port` |
| `on_close` | Channel closed | `channel_id` |
| `on_data` | Data transferred | `channel_id`, `dir`, `bytes` |

#### Example Plugins

See `plugins/` directory for examples:

- `geoip-filter/` - GeoIP country filtering (Python)
- `conn-timeout/` - Connection idle timeout monitoring (Python)

For detailed plugin development guide, see [plugins/PLUGIN_DEV.md](plugins/PLUGIN_DEV.md).

## Examples

### Expose a Web Server

```bash
# Local web server on port 8080
./p2p-tun -local 8080 -relay your-vps.com:9000
# Access via: http://your-vps.com:<assigned_port>
```

### Forward to Remote Host

```bash
# Forward to a remote host instead of localhost
./p2p-tun -local 8080 -target 192.168.1.100 -relay your-vps.com:9000
# Access via: http://your-vps.com:<assigned_port> -> 192.168.1.100:8080

# Multiple ports with different targets
./p2p-tun -local 8080,3306 -target 192.168.1.100,10.0.0.5 -relay your-vps.com:9000
```

### Specify Public Port

```bash
# Specify exact public port
./p2p-tun -local 8080 -port 32005 -relay your-vps.com:9000
# Access via: http://your-vps.com:32005

# Let server auto-assign (32000-33000 range)
./p2p-tun -local 8080 -relay your-vps.com:9000
# Output: 端口映射: VPS:32456 -> 127.0.0.1:8080 (tcp)
```

### SSH Access

```bash
# Expose SSH port
./p2p-tun -local 22 -port 22022 -relay your-vps.com:9000
# Connect via: ssh -p 22022 user@your-vps.com
```

### Remote Desktop (RDP)

```bash
# RDP with TCP + UDP for better performance
./p2p-tun -local 3389 -relay your-vps.com:9000 -proto both
# Connect via: your-vps.com:<assigned_port>
```

### Multiple Services

```bash
# Web + SSH + MySQL
./p2p-tun -local 8080,22,3306 -port 32001,32002,32003 -relay your-vps.com:9000
```

### Config File Mode

```bash
# Single service
./p2p-tun -c config.toml

# Multi-service with different targets (config in TOML)
# See [Config File Mode](#config-file-mode) section
```

### Secure Setup

```bash
# Server side
./signal-server -relay-port 9000 -auth-key mySecretKey123 -max-conns 100

# Client side
./p2p-tun -local 8080 -relay your-vps.com:9000 -auth-key mySecretKey123 -ip-allow 10.0.0.0/8,192.168.0.0/16 -max-conns 10
```

### With GeoIP Filtering

```bash
# Install geoip2
pip install geoip2

# Download GeoLite2 database
wget https://git.io/GeoLite2-Country.mmdb -O plugins/geoip-filter/GeoLite2-Country.mmdb

# Edit plugin config
vim plugins/geoip-filter/plugin.json  # Set deny_countries

# Start server with plugin
./signal-server -relay-port 9000 -plugin-dir ./plugins/geoip-filter
```

## Project Structure

```
p2p-tun/
├── main.go              # Main entry point
├── config.go            # TOML config file parsing
├── go.mod               # Go module definition
├── stun/
│   └── stun.go          # STUN protocol implementation
├── forward/
│   └── forward.go       # TCP/UDP port forwarding
├── keepalive/
│   └── keepalive.go     # Connection keepalive
├── relay/
│   └── relay.go         # Relay client
├── signal-server/
│   ├── main.go          # Relay server
│   ├── reuseaddr_linux.go  # SO_REUSEADDR for Linux
│   └── reuseaddr_other.go  # Default TCP listen for other OS
├── logutil/
│   └── logutil.go       # Logging utilities
├── plugin/
│   ├── plugin.go        # Plugin interfaces
│   ├── compression.go   # LZ4 compression
│   ├── connlimit.go     # Connection limiting
│   ├── ipfilter.go      # IP filtering
│   ├── ratelimit.go     # Rate limiting
│   └── trafficlog.go    # Traffic logging
├── dynplugin/
│   ├── protocol.go      # Dynamic plugin protocol
│   ├── process.go       # Plugin process management
│   ├── manager.go       # Plugin manager
│   ├── adapter.go       # Plugin adapter
│   └── sdk.go           # Go plugin SDK
└── plugins/             # Example plugins
    ├── PLUGIN_DEV.md    # Plugin development guide
    ├── geoip-filter/    # GeoIP filtering (Python)
    ├── ip-blacklist/    # IP blacklist (Python)
    └── ...
```

## Systemd Service (Linux)

Create `/etc/systemd/system/signal-server.service`:

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

Client config file mode with systemd:

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

## License

MIT License

## Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.
