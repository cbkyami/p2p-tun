package plugin

import (
	"net"
	"p2p_tun/dynplugin"
)

type AcceptFilter interface {
	OnAccept(proto string, addr net.Addr) bool
}

type Compressor interface {
	Compress(data []byte) ([]byte, bool)
	Decompress(data []byte) ([]byte, error)
}

type DecompressorOnly struct {
	c *Compression
}

func NewDecompressorOnly(c *Compression) *DecompressorOnly {
	return &DecompressorOnly{c: c}
}

func (d *DecompressorOnly) Decompress(data []byte) ([]byte, error) {
	return d.c.Decompress(data)
}

type TrafficLogger interface {
	OnOpen(proto, remoteAddr string, channelID uint32, localPort int) dynplugin.PluginAction
	OnClose(channelID uint32)
	OnData(channelID uint32, dir string, n int) dynplugin.PluginAction
	OnCheck() []uint32
	Close() error
}

type Manager struct {
	acceptFilters []AcceptFilter
	compressor    Compressor
	decompressor  DecompressorOnly
	logger        TrafficLogger
	connLimiter   *ConnLimit
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) AddAcceptFilter(f AcceptFilter) {
	m.acceptFilters = append(m.acceptFilters, f)
}

func (m *Manager) SetCompressor(c Compressor) {
	m.compressor = c
}

func (m *Manager) SetDecompressor(c *Compression) {
	m.decompressor = DecompressorOnly{c: c}
}

func (m *Manager) SetLogger(l TrafficLogger) {
	m.logger = l
}

func (m *Manager) SetConnLimiter(l *ConnLimit) {
	m.connLimiter = l
	m.AddAcceptFilter(l)
}

func (m *Manager) OnAccept(proto string, addr net.Addr) bool {
	for _, f := range m.acceptFilters {
		if !f.OnAccept(proto, addr) {
			return false
		}
	}
	return true
}

func (m *Manager) HasCompressor() bool {
	return m.compressor != nil
}

func (m *Manager) CanDecompress() bool {
	return m.compressor != nil || m.decompressor.c != nil
}

func (m *Manager) Compress(data []byte) ([]byte, bool) {
	if m.compressor == nil {
		return data, false
	}
	return m.compressor.Compress(data)
}

func (m *Manager) Decompress(data []byte) ([]byte, error) {
	if m.compressor != nil {
		return m.compressor.Decompress(data)
	}
	if m.decompressor.c != nil {
		return m.decompressor.Decompress(data)
	}
	return data, nil
}

func (m *Manager) OnOpen(proto, remoteAddr string, channelID uint32, localPort int) dynplugin.PluginAction {
	if m.connLimiter != nil {
		m.connLimiter.OnOpen()
	}
	if m.logger != nil {
		return m.logger.OnOpen(proto, remoteAddr, channelID, localPort)
	}
	return dynplugin.PluginAction{}
}

func (m *Manager) OnClose(channelID uint32) {
	if m.connLimiter != nil {
		m.connLimiter.OnClose()
	}
	if m.logger != nil {
		m.logger.OnClose(channelID)
	}
}

func (m *Manager) OnData(channelID uint32, dir string, n int) dynplugin.PluginAction {
	if m.logger != nil {
		return m.logger.OnData(channelID, dir, n)
	}
	return dynplugin.PluginAction{}
}

func (m *Manager) OnCheck() []uint32 {
	if m.logger != nil {
		return m.logger.OnCheck()
	}
	return nil
}

func (m *Manager) Close() {
	if m.logger != nil {
		m.logger.Close()
	}
}
