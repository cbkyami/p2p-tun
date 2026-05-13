package plugin

import (
	"fmt"
	"os"
	"sync"
	"time"

	"p2p_tun/dynplugin"
)

type connInfo struct {
	proto      string
	remoteAddr string
	localPort  int
	openTime   time.Time
	bytesRx    int64
	bytesTx    int64
}

type TrafficLog struct {
	mu    sync.Mutex
	file  *os.File
	conns map[uint32]*connInfo
}

func NewTrafficLog(path string) (*TrafficLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open traffic log: %w", err)
	}

	return &TrafficLog{
		file:  f,
		conns: make(map[uint32]*connInfo),
	}, nil
}

func (t *TrafficLog) OnOpen(proto, remoteAddr string, channelID uint32, localPort int) dynplugin.PluginAction {
	t.mu.Lock()
	t.conns[channelID] = &connInfo{
		proto:      proto,
		remoteAddr: remoteAddr,
		localPort:  localPort,
		openTime:   time.Now(),
	}
	t.mu.Unlock()

	fmt.Fprintf(t.file, "[%s] OPEN %s %s -> :%d channel=%d\n",
		time.Now().Format("2006-01-02 15:04:05"), proto, remoteAddr, localPort, channelID)
	t.file.Sync()
	return dynplugin.PluginAction{}
}

func (t *TrafficLog) OnClose(channelID uint32) {
	t.mu.Lock()
	info, ok := t.conns[channelID]
	if ok {
		delete(t.conns, channelID)
	}
	t.mu.Unlock()

	if ok {
		duration := time.Since(info.openTime).Truncate(time.Second)
		fmt.Fprintf(t.file, "[%s] CLOSE channel=%d %s %s -> :%d duration=%s rx=%s tx=%s\n",
			time.Now().Format("2006-01-02 15:04:05"), channelID, info.proto, info.remoteAddr,
			info.localPort, duration, formatBytes(info.bytesRx), formatBytes(info.bytesTx))
		t.file.Sync()
	}
}

func (t *TrafficLog) OnData(channelID uint32, dir string, n int) dynplugin.PluginAction {
	t.mu.Lock()
	info, ok := t.conns[channelID]
	if ok {
		if dir == "rx" {
			info.bytesRx += int64(n)
		} else {
			info.bytesTx += int64(n)
		}
	}
	t.mu.Unlock()
	return dynplugin.PluginAction{}
}

func (t *TrafficLog) OnCheck() []uint32 {
	return nil
}

func (t *TrafficLog) Close() error {
	if t.file != nil {
		return t.file.Close()
	}
	return nil
}

func formatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%dB", b)
	}
	if b < 1048576 {
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(b)/1048576)
}
