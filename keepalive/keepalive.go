package keepalive

import (
	"net"
	"time"

	"p2p_tun/logutil"
	"p2p_tun/stun"
)

func Keepalive(conn *net.UDPConn, stunServer string, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastAddr string

	for {
		select {
		case <-stop:
			logutil.Info("keepalive", "保活停止")
			return
		case <-ticker.C:
			logutil.Debug("keepalive", "发送心跳到 %s", stunServer)
			addr, err := stun.BindingRequest(conn, stunServer)
			if err != nil {
				logutil.Warn("keepalive", "心跳失败: %v", err)
				continue
			}
			currentAddr := addr.String()
			if lastAddr != "" && currentAddr != lastAddr {
				logutil.Info("keepalive", "公网地址变更: %s -> %s", lastAddr, currentAddr)
			}
			lastAddr = currentAddr
			logutil.Debug("keepalive", "心跳成功, 公网地址: %s", currentAddr)
		}
	}
}
