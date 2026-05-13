# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Deploy

```bash
# Client (Windows)
go build -o p2p-tun.exe .

# Server (Linux, for VPS)
GOOS=linux GOARCH=amd64 go build -o signal-server ./signal-server/

# Upload server to VPS
scp -P 336 -i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ./signal-server/signal-server root@lizncux0.xyz:/root/
```

Only one external dependency: `github.com/pierrec/lz4/v4` for compression.

## Architecture

Two binaries from the same `p2p_tun` module:
- **Client** (`main.go`): CLI + built-in web GUI on `:19999`
- **Server** (`signal-server/main.go`): runs on VPS, handles STUN + relay

### Three-layer NAT traversal fallback

The client tries layers in order, stopping at first success:

1. **UPnP/NAT-PMP**: map ports directly on the gateway router
2. **STUN Full Cone**: if NAT type detected as `full-cone`, use the STUN-discovered public address with keepalive
3. **VPS Relay**: fallback — all traffic proxies through the VPS

Each layer is attempted in `runTunnel()` (main.go:202). UPnP and FullCone paths use `forward.ForwardTCP`/`ForwardUDP` for direct forwarding. Relay path uses the binary frame protocol.

### Binary frame protocol (single TCP connection)

All relay traffic multiplexed over one TCP connection:
```
[1B type][4B channelID][4B payloadLen][payload]
```
Types: `0x00` control, `0x01` data, `0x02` close, `0x03` compressed data

Control messages are JSON: `register`, `ok`, `new_conn`, `new_udp_conn`, `conn_ready`, `ping`, `pong`, `error`.

### Relay data flow (TCP)

```
External user → VPS TCP listener → Accept → channelID → frameData → client → dial 127.0.0.1:localPort → service
```

Server uses `channelConns` map on `relayClient` (not a local variable) to bridge external connections with client frames. Each external connection gets a `channelID`.

### Relay data flow (UDP)

UDP is connectionless, so uses session-per-source-address instead of per-connection:

```
External UDP → VPS UDP listener → ReadFromUDP → sessionKey=remoteAddr → channelID → frameData → client → dial UDP → service
```

Server `udpReadLoop` creates/updates `udpSession` per source address. Sessions auto-expire after 120s inactivity. Client uses `udpChannels` map (separate from TCP `channels`).

### Proto flag

`-proto` accepts `tcp`, `udp`, or `both`. When `both`, each local port generates two `ServiceMap` entries (one per protocol), creating both listeners on the VPS.

## Key design decisions

- **Pure Go stdlib** (except lz4 for V8 compression). Web UI uses Tailwind CDN (no Go dependency).
- **PSK auth**: `-auth-key` on both client and server. Server validates in `handleRelayClient` before registering services.
- **GUI token auth**: random hex token generated on startup (or set via `-gui-token`). Stored in localStorage. Middleware exempts `/` and `/api/login`.
- **Traffic monitoring**: `logutil` package has atomic counters (`totalBytesIn`, `totalBytesOut`, `activeChans`) updated in relay data paths. `RecordTraffic()` called per-second from GUI goroutine.
- **Graceful shutdown**: `relayClient.stopCh` pattern — close channel to signal cleanup, `select` in accept/read loops.
- **Error log filtering**: `isNormalClose()` prevents logging connection resets/EOF as ERROR.

## Deployment state

- VPS: `lizncux0.xyz`, SSH port 336, ed25519 key auth
- Relay port: 32611, STUN port: 32610
- Cloud firewall: **TCP and UDP must be opened separately** in security groups. Forgot to open UDP for new ports is a known issue.

## Plugin system (V8)

Built-in plugins live in `plugin/` (Go interfaces: `AcceptFilter`, `Compressor`, `TrafficLogger`). Managed by `plugin.Manager`. Dynamic plugins in `dynplugin/` define a JSON-line protocol for external processes (Python, Node, Bash) — see `plugins/PLUGIN_DEV.md`.
