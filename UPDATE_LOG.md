# Update Log

## update-2 (2026-05-19)
- Add VERSION file for release identification
- Add UPDATE_LOG.md to track changes across branches
- Add config.go for TOML configuration support
- Add signal-server SO_REUSEADDR support (Linux + other platforms)
- Update go.mod/go.sum dependencies
- Major refactor of main.go, relay/relay.go, signal-server/main.go
- Update README.md and README_CN.md with expanded documentation

## update-1 (2026-05-19)
- Refactor logutil atomic operations (use atomic.SwapInt64)
- Add access-log plugin
- Remove upnp module
- Remove pre-built binary download links from README
