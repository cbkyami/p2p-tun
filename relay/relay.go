package relay

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"p2p_tun/logutil"
	"p2p_tun/plugin"
)

type ConnInfo struct {
	ChannelID   uint32    `json:"channel_id"`
	RemoteAddr  string    `json:"remote_addr"`
	LocalPort   int       `json:"local_port"`
	Proto       string    `json:"proto"`
	ConnectedAt time.Time `json:"connected_at"`
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
}

type ServiceMap struct {
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Proto      string `json:"proto"`
	TargetHost string `json:"target_host,omitempty"`
	Compress   bool   `json:"compress,omitempty"`
	IPAllow    string `json:"ip_allow,omitempty"`
	IPDeny     string `json:"ip_deny,omitempty"`
	MaxConns   int    `json:"max_conns,omitempty"`
	RateLimit  int64  `json:"rate_limit,omitempty"`
	WebAuth    string `json:"web_auth,omitempty"`
}

type Client struct {
	ServerAddr  string
	AuthKey     string
	Services    []ServiceMap
	Compressor  *plugin.Compression
	IPAllow     string
	IPDeny      string
	MaxConns    int
	RateLimit   int64
	TargetHosts map[int]string

	conn                      net.Conn
	channels                  map[uint32]net.Conn
	udpChannels               map[uint32]net.Conn
	connInfos                 map[uint32]*ConnInfo
	totalConns                int64
	mu                        sync.RWMutex
	writeMu                   sync.Mutex
	nextID                    uint32
	closeCh                   chan struct{}
	disconnectedCh            chan struct{}
	serverSupportsCompression bool
	svcCompress               map[int]bool
}

type controlMsg struct {
	Type             string       `json:"type"`
	ChannelID        uint32       `json:"channel_id,omitempty"`
	LocalPort        int          `json:"local_port,omitempty"`
	RemotePort       int          `json:"remote_port,omitempty"`
	Proto            string       `json:"proto,omitempty"`
	PublicAddr       string       `json:"public_addr,omitempty"`
	RemoteAddr       string       `json:"remote_addr,omitempty"`
	Services         []ServiceMap `json:"services,omitempty"`
	AuthKey          string       `json:"auth_key,omitempty"`
	FailedServices   []ServiceMap `json:"failed_services,omitempty"`
	AssignedServices []ServiceMap `json:"assigned_services,omitempty"`
	Features         []string     `json:"features,omitempty"`
	IPAllow          string       `json:"ip_allow,omitempty"`
	IPDeny           string       `json:"ip_deny,omitempty"`
	MaxConns         int          `json:"max_conns,omitempty"`
	RateLimit        int64        `json:"rate_limit,omitempty"`
}

const (
	frameControl        = 0x00
	frameData           = 0x01
	frameClose          = 0x02
	frameDataCompressed = 0x03

	frameHeaderSize = 1 + 4 + 4
	maxFrameSize    = 65536
)

func frameTypeName(t byte) string {
	switch t {
	case frameControl:
		return "control"
	case frameData:
		return "data"
	case frameClose:
		return "close"
	case frameDataCompressed:
		return "data-compressed"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

func (c *Client) targetAddr(localPort int) string {
	host := "127.0.0.1"
	if c.TargetHosts != nil {
		if h, ok := c.TargetHosts[localPort]; ok && h != "" {
			host = h
		}
	}
	return host + ":" + strconv.Itoa(localPort)
}

func (c *Client) Connect() error {
	logutil.Info("relay", "连接到中继服务器 %s", c.ServerAddr)

	conn, err := net.DialTimeout("tcp", c.ServerAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to relay server: %w", err)
	}
	c.conn = conn
	c.channels = make(map[uint32]net.Conn)
	c.udpChannels = make(map[uint32]net.Conn)
	c.connInfos = make(map[uint32]*ConnInfo)
	c.closeCh = make(chan struct{})
	c.disconnectedCh = make(chan struct{})
	c.svcCompress = make(map[int]bool)
	for _, svc := range c.Services {
		if svc.Compress {
			c.svcCompress[svc.LocalPort] = true
		}
	}

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	var clientFeatures []string
	if c.Compressor != nil {
		clientFeatures = append(clientFeatures, "compression")
	}

	reg := controlMsg{
		Type:      "register",
		Services:  c.Services,
		AuthKey:   c.AuthKey,
		Features:  clientFeatures,
		IPAllow:   c.IPAllow,
		IPDeny:    c.IPDeny,
		MaxConns:  c.MaxConns,
		RateLimit: c.RateLimit,
	}
	logutil.Debug("relay", "发送注册消息, 服务数: %d, 特性: %v", len(c.Services), clientFeatures)
	if err := c.sendControl(reg); err != nil {
		conn.Close()
		return fmt.Errorf("send register: %w", err)
	}

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	resp, err := c.readControl()
	if err != nil {
		conn.Close()
		return fmt.Errorf("read register response: %w", err)
	}
	conn.SetDeadline(time.Time{})

	if resp.Type != "ok" {
		conn.Close()
		if resp.Type == "error" {
			return fmt.Errorf("register rejected: %s", resp.PublicAddr)
		}
		return fmt.Errorf("register rejected: %s", resp.Type)
	}

	for _, f := range resp.Features {
		if f == "compression" {
			c.serverSupportsCompression = true
			break
		}
	}

	if c.Compressor != nil && !c.serverSupportsCompression {
		logutil.Warn("relay", "服务端不支持压缩，客户端压缩已禁用")
		c.Compressor = nil
	}

	if len(resp.AssignedServices) > 0 {
		c.Services = resp.AssignedServices
	}

	logutil.Info("relay", "注册成功, 公网地址: %s", resp.PublicAddr)
	for _, svc := range c.Services {
		targetHost := svc.TargetHost
		if targetHost == "" {
			targetHost = "127.0.0.1"
		}
		logutil.Info("relay", "端口映射: VPS:%d -> %s:%d (%s)", svc.RemotePort, targetHost, svc.LocalPort, svc.Proto)
	}
	if len(resp.FailedServices) > 0 {
		for _, svc := range resp.FailedServices {
			logutil.Error("relay", "服务启动失败: VPS:%d -> 本地:%d (%s) — 服务端无法监听此端口", svc.RemotePort, svc.LocalPort, svc.Proto)
		}
		if len(resp.AssignedServices) == 0 {
			c.conn.Close()
			return fmt.Errorf("所有端口启动失败，需要重连")
		}
	}

	go c.readLoop()

	return nil
}

func (c *Client) Close() error {
	select {
	case <-c.closeCh:
	default:
		close(c.closeCh)
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *Client) Disconnected() <-chan struct{} {
	return c.disconnectedCh
}

func (c *Client) GetConnections() []ConnInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ConnInfo, 0, len(c.connInfos))
	for _, info := range c.connInfos {
		result = append(result, *info)
	}
	return result
}

func (c *Client) GetTotalConns() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.totalConns
}

func (c *Client) sendControl(msg controlMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(frameControl, 0, data)
}

func (c *Client) writeFrame(frameType byte, channelID uint32, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	header := make([]byte, frameHeaderSize)
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:5], channelID)
	binary.BigEndian.PutUint32(header[5:9], uint32(len(payload)))

	logutil.Debug("relay", "发送 frame: type=%s channel_id=%d len=%d", frameTypeName(frameType), channelID, len(payload))

	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := c.conn.Write(payload)
		return err
	}
	return nil
}

func (c *Client) readFrame() (byte, uint32, []byte, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return 0, 0, nil, err
	}

	frameType := header[0]
	channelID := binary.BigEndian.Uint32(header[1:5])
	payloadLen := binary.BigEndian.Uint32(header[5:9])

	if payloadLen > maxFrameSize {
		return 0, 0, nil, fmt.Errorf("frame too large: %d", payloadLen)
	}

	var payload []byte
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err := io.ReadFull(c.conn, payload); err != nil {
			return 0, 0, nil, err
		}
	}

	logutil.Debug("relay", "收到 frame: type=%s channel_id=%d len=%d", frameTypeName(frameType), channelID, len(payload))

	return frameType, channelID, payload, nil
}

func (c *Client) readControl() (*controlMsg, error) {
	frameType, _, payload, err := c.readFrame()
	if err != nil {
		return nil, err
	}
	if frameType != frameControl {
		return nil, fmt.Errorf("expected control frame, got %d", frameType)
	}

	var msg controlMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		for id, conn := range c.channels {
			conn.Close()
			delete(c.channels, id)
			logutil.AddActiveChan(-1)
		}
		for id, conn := range c.udpChannels {
			conn.Close()
			delete(c.udpChannels, id)
			logutil.AddActiveChan(-1)
		}
		for id := range c.connInfos {
			delete(c.connInfos, id)
		}
		c.mu.Unlock()

		select {
		case <-c.closeCh:
		default:
			close(c.disconnectedCh)
		}
	}()

	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		c.conn.SetReadDeadline(time.Now().Add(300 * time.Second))
		frameType, channelID, payload, err := c.readFrame()
		if err != nil {
			if !isClosedErr(err) {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					logutil.Debug("relay", "读取超时, 发送心跳")
					ping := controlMsg{Type: "ping"}
					if pingErr := c.sendControl(ping); pingErr != nil {
						logutil.Error("relay", "心跳发送失败: %v", pingErr)
						return
					}
					continue
				}
				logutil.Error("relay", "读取错误: %v", err)
			}
			logutil.Info("relay", "连接断开")
			return
		}

		switch frameType {
		case frameControl:
			var msg controlMsg
			if err := json.Unmarshal(payload, &msg); err != nil {
				logutil.Error("relay", "无效控制消息: %v", err)
				continue
			}
			c.handleControl(msg)

		case frameData, frameDataCompressed:
			data := payload
			if frameType == frameDataCompressed {
				if c.Compressor != nil {
					decompressed, err := c.Compressor.Decompress(payload)
					if err != nil {
						logutil.Error("relay", "解压 channel %d 失败: %v", channelID, err)
						continue
					}
					data = decompressed
				} else {
					logutil.Error("relay", "收到压缩帧但未启用压缩, channel_id=%d", channelID)
					continue
				}
			}

			c.mu.RLock()
			localConn, ok := c.channels[channelID]
			if !ok {
				localConn, ok = c.udpChannels[channelID]
			}
			c.mu.RUnlock()
			if ok && localConn != nil {
				if _, err := localConn.Write(data); err != nil {
					logutil.Error("relay", "写入本地连接 channel %d 失败: %v", channelID, err)
				} else {
					logutil.AddBytesIn(int64(len(data)))
					c.mu.Lock()
					if info, exists := c.connInfos[channelID]; exists {
						info.BytesIn += int64(len(data))
					}
					c.mu.Unlock()
				}
			}

		case frameClose:
			c.mu.Lock()
			if localConn, ok := c.channels[channelID]; ok {
				localConn.Close()
				delete(c.channels, channelID)
				logutil.AddActiveChan(-1)
			}
			if localConn, ok := c.udpChannels[channelID]; ok {
				localConn.Close()
				delete(c.udpChannels, channelID)
				logutil.AddActiveChan(-1)
			}
			if _, ok := c.connInfos[channelID]; ok {
				delete(c.connInfos, channelID)
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) handleControl(msg controlMsg) {
	switch msg.Type {
	case "new_conn":
		c.handleNewConn(msg.ChannelID, msg.LocalPort, msg.RemoteAddr)
	case "new_udp_conn":
		c.handleNewUDPConn(msg.ChannelID, msg.LocalPort, msg.RemoteAddr)
	case "ping":
		logutil.Debug("relay", "收到服务器心跳")
	case "pong":
		logutil.Debug("relay", "收到服务器心跳回复")
	}
}

func (c *Client) handleNewConn(channelID uint32, localPort int, remoteAddr string) {
	logutil.Debug("relay", "收到 new_conn: channel_id=%d, local_port=%d, remote=%s", channelID, localPort, remoteAddr)

	targetAddr := c.targetAddr(localPort)
	localConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		logutil.Error("relay", "连接本地服务 %s 失败: %v", targetAddr, err)
		c.writeFrame(frameClose, channelID, nil)
		return
	}

	logutil.Debug("relay", "连接本地服务 %s 成功", targetAddr)

	c.mu.Lock()
	c.channels[channelID] = localConn
	c.connInfos[channelID] = &ConnInfo{
		ChannelID:   channelID,
		RemoteAddr:  remoteAddr,
		LocalPort:   localPort,
		Proto:       "tcp",
		ConnectedAt: time.Now(),
	}
	c.totalConns++
	c.mu.Unlock()

	logutil.AddActiveChan(1)

	ready := controlMsg{Type: "conn_ready", ChannelID: channelID}
	if err := c.sendControl(ready); err != nil {
		logutil.Error("relay", "发送 conn_ready 错误: %v", err)
		return
	}

	go c.pipeLocalToRelay(channelID, localConn)
}

func (c *Client) handleNewUDPConn(channelID uint32, localPort int, remoteAddr string) {
	logutil.Debug("relay", "收到 new_udp_conn: channel_id=%d, local_port=%d, remote=%s", channelID, localPort, remoteAddr)

	targetAddr := c.targetAddr(localPort)
	localConn, err := net.Dial("udp", targetAddr)
	if err != nil {
		logutil.Error("relay", "连接本地 UDP %s 失败: %v", targetAddr, err)
		c.writeFrame(frameClose, channelID, nil)
		return
	}

	logutil.Debug("relay", "连接本地 UDP %s 成功", targetAddr)

	c.mu.Lock()
	c.udpChannels[channelID] = localConn
	c.connInfos[channelID] = &ConnInfo{
		ChannelID:   channelID,
		RemoteAddr:  remoteAddr,
		LocalPort:   localPort,
		Proto:       "udp",
		ConnectedAt: time.Now(),
	}
	c.totalConns++
	c.mu.Unlock()

	logutil.AddActiveChan(1)

	ready := controlMsg{Type: "conn_ready", ChannelID: channelID}
	if err := c.sendControl(ready); err != nil {
		logutil.Error("relay", "发送 conn_ready 错误: %v", err)
		return
	}

	go c.pipeUDPFromRelay(channelID, localConn)
}

func (c *Client) pipeLocalToRelay(channelID uint32, localConn net.Conn) {
	defer func() {
		c.writeFrame(frameClose, channelID, nil)
		c.mu.Lock()
		if _, ok := c.channels[channelID]; ok {
			delete(c.channels, channelID)
			logutil.AddActiveChan(-1)
		}
		if _, ok := c.connInfos[channelID]; ok {
			delete(c.connInfos, channelID)
		}
		c.mu.Unlock()
		localConn.Close()
	}()

	buf := make([]byte, 32*1024)
	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		if err := localConn.SetReadDeadline(time.Now().Add(300 * time.Second)); err != nil {
			return
		}
		n, err := localConn.Read(buf)
		if err != nil {
			if err != io.EOF && !isClosedErr(err) {
				logutil.Error("relay", "读取本地 channel %d 错误: %v", channelID, err)
			}
			return
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		ft := byte(frameData)
		shouldCompress := c.Compressor != nil && c.serverSupportsCompression
		if !shouldCompress {
			c.mu.RLock()
			if info, ok := c.connInfos[channelID]; ok {
				if svcCompress, ok2 := c.svcCompress[info.LocalPort]; ok2 && svcCompress {
					shouldCompress = c.serverSupportsCompression
				}
			}
			c.mu.RUnlock()
		}
		if shouldCompress && c.Compressor != nil {
			compressed, ok := c.Compressor.Compress(data)
			if ok {
				data = compressed
				ft = frameDataCompressed
			}
		}

		if err := c.writeFrame(ft, channelID, data); err != nil {
			logutil.Error("relay", "写入 frame channel %d 错误: %v", channelID, err)
			return
		}
		logutil.AddBytesOut(int64(n))

		c.mu.Lock()
		if info, ok := c.connInfos[channelID]; ok {
			info.BytesOut += int64(n)
		}
		c.mu.Unlock()
	}
}

func (c *Client) pipeUDPFromRelay(channelID uint32, localConn net.Conn) {
	defer func() {
		c.writeFrame(frameClose, channelID, nil)
		c.mu.Lock()
		if _, ok := c.udpChannels[channelID]; ok {
			delete(c.udpChannels, channelID)
			logutil.AddActiveChan(-1)
		}
		if _, ok := c.connInfos[channelID]; ok {
			delete(c.connInfos, channelID)
		}
		c.mu.Unlock()
		localConn.Close()
	}()

	buf := make([]byte, 65535)
	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		localConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := localConn.Read(buf)
		if err != nil {
			if err != io.EOF && !isClosedErr(err) {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				logutil.Error("relay", "读取本地 UDP channel %d 错误: %v", channelID, err)
			}
			return
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		ft := byte(frameData)
		if c.Compressor != nil && c.serverSupportsCompression {
			compressed, ok := c.Compressor.Compress(data)
			if ok {
				data = compressed
				ft = frameDataCompressed
			}
		}

		if err := c.writeFrame(ft, channelID, data); err != nil {
			logutil.Error("relay", "写入 UDP frame channel %d 错误: %v", channelID, err)
			return
		}
		logutil.AddBytesOut(int64(n))

		c.mu.Lock()
		if info, ok := c.connInfos[channelID]; ok {
			info.BytesOut += int64(n)
		}
		c.mu.Unlock()
	}
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	if opErr, ok := err.(*net.OpError); ok {
		return opErr.Err.Error() == "use of closed network connection"
	}
	return false
}

var globalChannelID uint32

func NextChannelID() uint32 {
	return atomic.AddUint32(&globalChannelID, 1)
}
