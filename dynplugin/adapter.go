package dynplugin

import (
	"net"
)

type AcceptFilterAdapter struct {
	mgr *Manager
}

func NewAcceptFilterAdapter(mgr *Manager) *AcceptFilterAdapter {
	return &AcceptFilterAdapter{mgr: mgr}
}

func (a *AcceptFilterAdapter) OnAccept(proto string, addr net.Addr) bool {
	allowed, _ := a.mgr.OnAccept(proto, addr.String())
	return allowed
}

type TrafficLoggerAdapter struct {
	mgr *Manager
}

func NewTrafficLoggerAdapter(mgr *Manager) *TrafficLoggerAdapter {
	return &TrafficLoggerAdapter{mgr: mgr}
}

func (t *TrafficLoggerAdapter) OnOpen(proto, remoteAddr string, channelID uint32, localPort int) PluginAction {
	return t.mgr.OnOpen(proto, remoteAddr, channelID, localPort)
}

func (t *TrafficLoggerAdapter) OnClose(channelID uint32) {
	t.mgr.OnClose(channelID)
}

func (t *TrafficLoggerAdapter) OnData(channelID uint32, dir string, n int) PluginAction {
	t.mgr.OnData(channelID, dir, n)
	return PluginAction{}
}

func (t *TrafficLoggerAdapter) OnCheck() []uint32 {
	return t.mgr.OnCheck()
}

func (t *TrafficLoggerAdapter) Close() error {
	return nil
}
