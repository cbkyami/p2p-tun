package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"p2p_tun/dynplugin"
	"p2p_tun/plugin"
)

//go:embed auth_bg.webp
var authBgData []byte

type Config struct {
	STUNPort   int
	RelayPort  int
	PublicAddr string
	AuthKey    string
}

type udpSession struct {
	channelID  uint32
	localPort  int
	remoteAddr *net.UDPAddr
	udpConn    *net.UDPConn
	lastActive time.Time
}

type relayClient struct {
	conn         net.Conn
	services     []serviceMapping
	channelConns map[uint32]net.Conn
	chanMu       sync.RWMutex
	writeMu      sync.Mutex
	stopCh       chan struct{}
	stopOnce     sync.Once
	cid          uint32
	udpConns     map[int]*net.UDPConn
	udpSessions  map[string]*udpSession
	udpMu        sync.RWMutex
	features     []string
	ipFilter     *plugin.IPFilter
	connLimiter  *plugin.ConnLimit
	rateLimiter  *plugin.RateLimit
	clientKey    string
	svcFilters   map[int]*svcFilter
	chanSvcPort  map[uint32]int
}

type svcFilter struct {
	ipFilter    *plugin.IPFilter
	connLimiter *plugin.ConnLimit
	rateLimiter *plugin.RateLimit
	compress    bool
	webAuth     *webAuthConfig
}

type webAuthConfig struct {
	password string
	port     int
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (bc *bufferedConn) Read(b []byte) (int, error) {
	return bc.reader.Read(b)
}

func (c *relayClient) stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

type serviceMapping struct {
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

type controlMsg struct {
	Type             string           `json:"type"`
	ChannelID        uint32           `json:"channel_id,omitempty"`
	LocalPort        int              `json:"local_port,omitempty"`
	RemotePort       int              `json:"remote_port,omitempty"`
	Proto            string           `json:"proto,omitempty"`
	PublicAddr       string           `json:"public_addr,omitempty"`
	RemoteAddr       string           `json:"remote_addr,omitempty"`
	Services         []serviceMapping `json:"services,omitempty"`
	AuthKey          string           `json:"auth_key,omitempty"`
	FailedServices   []serviceMapping `json:"failed_services,omitempty"`
	AssignedServices []serviceMapping `json:"assigned_services,omitempty"`
	ClientKey        string           `json:"client_key,omitempty"`
	Features         []string         `json:"features,omitempty"`
	IPAllow          string           `json:"ip_allow,omitempty"`
	IPDeny           string           `json:"ip_deny,omitempty"`
	MaxConns         int              `json:"max_conns,omitempty"`
	RateLimit        int64            `json:"rate_limit,omitempty"`
}

const (
	frameControl        = 0x00
	frameData           = 0x01
	frameClose          = 0x02
	frameDataCompressed = 0x03

	frameHeaderSize = 1 + 4 + 4
	maxFrameSize    = 65536

	stunMagicCookie = 0x2112A442
	bindingRequest  = 0x0001

	randomPortMin = 32000
	randomPortMax = 33000

	portHoldDuration = 30 * time.Second
)

type heldPort struct {
	Port       int
	Proto      string
	ReleasedAt time.Time
	ClientKey  string
}

var (
	clients           = make(map[uint32]*relayClient)
	clientsMu         sync.RWMutex
	nextCID           uint32
	listeners         = make(map[int]net.Listener)
	listenMu          sync.Mutex
	heldPorts         []heldPort
	heldMu            sync.Mutex
	serverCfg         Config
	verbose           bool
	pluginMgr         *plugin.Manager
	dynPluginMgr      *dynplugin.Manager
	globalRateLimiter *plugin.RateLimit
	stunConn          *net.UDPConn
	relayListener     net.Listener
	stunStopCh        = make(chan struct{})
	relayStopCh       = make(chan struct{})
	stunWg            sync.WaitGroup
	relayWg           sync.WaitGroup
)

func isNormalClose(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "broken pipe")
}

func allocateRandomPort() int {
	for i := 0; i < 100; i++ {
		port := randomPortMin + rand.Intn(randomPortMax-randomPortMin+1)
		listenMu.Lock()
		if _, exists := listeners[port]; !exists {
			listenMu.Unlock()
			return port
		}
		listenMu.Unlock()
	}
	return randomPortMin
}

func holdPortsForClient(clientKey string, svcs []serviceMapping) {
	heldMu.Lock()
	defer heldMu.Unlock()
	now := time.Now()
	for _, svc := range svcs {
		heldPorts = append(heldPorts, heldPort{
			Port:       svc.RemotePort,
			Proto:      svc.Proto,
			ReleasedAt: now,
			ClientKey:  clientKey,
		})
	}
}

func findHeldPort(clientKey string, localPort int, proto string) (int, bool) {
	heldMu.Lock()
	defer heldMu.Unlock()
	cleanExpiredHeldLocked()
	for i, hp := range heldPorts {
		if hp.ClientKey == clientKey && hp.Proto == proto {
			heldPorts = append(heldPorts[:i], heldPorts[i+1:]...)
			return hp.Port, true
		}
	}
	return 0, false
}

func cleanExpiredHeldLocked() {
	now := time.Now()
	i := 0
	for i < len(heldPorts) {
		if now.Sub(heldPorts[i].ReleasedAt) > portHoldDuration {
			heldPorts = append(heldPorts[:i], heldPorts[i+1:]...)
		} else {
			i++
		}
	}
}

func logDebug(module, format string, args ...interface{}) {
	if !verbose {
		return
	}
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] DEBUG %s", module, msg)
}

func logInfo(module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] INFO %s", module, msg)
}

func logWarn(module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] WARN %s", module, msg)
}

func logError(module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] ERROR %s", module, msg)
}

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

func clientSupportsCompression(client *relayClient) bool {
	for _, f := range client.features {
		if f == "compression" {
			return true
		}
	}
	return false
}

func main() {
	rand.Seed(time.Now().UnixNano())

	flag.Usage = func() {
		fmt.Print(serverHelpText)
	}

	stunPort := flag.Int("stun-port", 0, "STUN 服务端口，0=不启动")
	relayPort := flag.Int("relay-port", 9000, "中继控制端口")
	publicAddr := flag.String("public-addr", "", "服务器公网地址 (用于显示)")
	authKey := flag.String("auth-key", "", "客户端认证密钥，留空则不验证")
	ipAllow := flag.String("ip-allow", "", "IP 白名单 (CIDR, 逗号分隔)")
	ipDeny := flag.String("ip-deny", "", "IP 黑名单 (CIDR, 逗号分隔)")
	trafficLogFile := flag.String("traffic-log", "", "流量日志文件路径")
	compressFlag := flag.Bool("compress", false, "启用数据压缩 (lz4)")
	maxConns := flag.Int("max-conns", 0, "全局最大并发连接数，0=不限")
	rateLimit := flag.Int64("rate-limit", 0, "全局带宽限制 (字节/秒)，0=不限")
	pluginDir := flag.String("plugin-dir", "", "动态插件目录，留空则不加载")
	pluginTimeout := flag.Duration("plugin-timeout", 5*time.Second, "动态插件调用超时")
	flag.BoolVar(&verbose, "verbose", false, "输出详细调试日志")
	flag.Parse()

	serverCfg = Config{
		STUNPort:   *stunPort,
		RelayPort:  *relayPort,
		PublicAddr: *publicAddr,
		AuthKey:    *authKey,
	}

	pluginMgr = plugin.NewManager()

	if *ipAllow != "" || *ipDeny != "" {
		ipf, err := plugin.NewIPFilter(*ipAllow, *ipDeny)
		if err != nil {
			log.Fatalf("IP 过滤器初始化失败: %v", err)
		}
		pluginMgr.AddAcceptFilter(ipf)
		logInfo("server", "插件已加载: ip-filter (allow=%s, deny=%s)", *ipAllow, *ipDeny)
	}

	if *trafficLogFile != "" {
		tl, err := plugin.NewTrafficLog(*trafficLogFile)
		if err != nil {
			log.Fatalf("流量日志初始化失败: %v", err)
		}
		pluginMgr.SetLogger(tl)
		logInfo("server", "插件已加载: traffic-log (file=%s)", *trafficLogFile)
	}

	if *compressFlag {
		comp := plugin.NewCompression(1, 128)
		pluginMgr.SetCompressor(comp)
		logInfo("server", "插件已加载: compression (lz4, min_size=128)")
	} else {
		pluginMgr.SetDecompressor(plugin.NewCompression(1, 128))
		logInfo("server", "压缩: 仅解压模式 (客户端压缩帧可解压)")
	}

	if *maxConns > 0 {
		pluginMgr.SetConnLimiter(plugin.NewConnLimit(*maxConns))
		logInfo("server", "插件已加载: conn-limit (max=%d)", *maxConns)
	}

	globalRateLimiter = plugin.NewRateLimit(*rateLimit)
	if globalRateLimiter != nil {
		logInfo("server", "插件已加载: rate-limit (%d bytes/s)", *rateLimit)
	}

	if *pluginDir != "" {
		dynPluginMgr = dynplugin.NewManager(*pluginTimeout)
		if err := dynPluginMgr.LoadDir(*pluginDir); err != nil {
			logWarn("server", "动态插件加载失败: %v", err)
		} else {
			adapter := dynplugin.NewAcceptFilterAdapter(dynPluginMgr)
			pluginMgr.AddAcceptFilter(adapter)
			logAdapter := dynplugin.NewTrafficLoggerAdapter(dynPluginMgr)
			pluginMgr.SetLogger(logAdapter)
			logInfo("server", "动态插件已加载: %v", dynPluginMgr.PluginNames())
		}
	}

	logInfo("server", "=== 信令/中继服务器 ===")
	if serverCfg.STUNPort > 0 {
		logInfo("server", "STUN 端口: %d (UDP)", serverCfg.STUNPort)
	}
	logInfo("server", "中继端口: %d (TCP)", serverCfg.RelayPort)
	logInfo("server", "详细日志: %v", verbose)
	if serverCfg.PublicAddr != "" {
		logInfo("server", "公网地址: %s", serverCfg.PublicAddr)
	}

	sigCh := make(chan os.Signal, 1)
	fatalCh := make(chan error, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if serverCfg.STUNPort > 0 {
		stunWg.Add(1)
		go runSTUNServer(serverCfg.STUNPort, fatalCh)
	}
	relayWg.Add(1)
	go runRelayServer(serverCfg.RelayPort, fatalCh)
	go runPluginChecker()

	select {
	case <-sigCh:
		logInfo("server", "收到退出信号，正在关闭...")
	case err := <-fatalCh:
		logError("server", "服务启动失败: %v", err)
	}

	if serverCfg.STUNPort > 0 {
		close(stunStopCh)
		if stunConn != nil {
			stunConn.Close()
		}
		stunWg.Wait()
	}

	close(relayStopCh)
	if relayListener != nil {
		relayListener.Close()
	}
	relayWg.Wait()

	clientsMu.Lock()
	for _, client := range clients {
		client.stop()
		client.conn.Close()
	}
	clients = make(map[uint32]*relayClient)
	clientsMu.Unlock()

	if dynPluginMgr != nil {
		dynPluginMgr.Stop()
		logInfo("server", "动态插件已停止")
	}

	os.Exit(0)
}

func runPluginChecker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		channels := pluginMgr.OnCheck()
		if len(channels) > 0 {
			logDebug("server", "OnCheck 返回需关闭的 channels: %v", channels)
		}
		for _, chID := range channels {
			closed := false
			clientsMu.RLock()
			for _, client := range clients {
				client.chanMu.Lock()
				if extConn, ok := client.channelConns[chID]; ok {
					delete(client.channelConns, chID)
					extConn.Close()
					closed = true
				}
				client.chanMu.Unlock()
				if closed {
					break
				}
				client.udpMu.Lock()
				for key, sess := range client.udpSessions {
					if sess.channelID == chID {
						delete(client.udpSessions, key)
						closed = true
						break
					}
				}
				client.udpMu.Unlock()
				if closed {
					break
				}
			}
			clientsMu.RUnlock()
			if closed {
				pluginMgr.OnClose(chID)
				logInfo("server", "插件检查关闭连接: channel=%d", chID)
			}
		}
	}
}

func runSTUNServer(port int, fatalCh chan<- error) error {
	defer stunWg.Done()

	addr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(port))
	if err != nil {
		fatalCh <- err
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		logError("stun-server", "监听失败: %v", err)
		fatalCh <- err
		return err
	}
	stunConn = conn
	defer conn.Close()

	logInfo("stun-server", "STUN 服务启动, 监听 :%d", port)

	buf := make([]byte, 1500)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-stunStopCh:
				return nil
			default:
				logError("stun-server", "读取错误: %v", err)
				continue
			}
		}

		if n < 20 {
			continue
		}

		msgType := binary.BigEndian.Uint16(buf[0:2])
		if msgType != bindingRequest {
			continue
		}

		cookie := binary.BigEndian.Uint32(buf[4:8])
		if cookie != stunMagicCookie {
			continue
		}

		logDebug("stun-server", "收到 STUN Binding Request, 来源: %s", clientAddr)

		resp := buildSTUNResponse(buf[:n], clientAddr)
		if _, err := conn.WriteToUDP(resp, clientAddr); err != nil {
			logError("stun-server", "写入响应错误: %v", err)
		} else {
			logDebug("stun-server", "返回 STUN Response 到 %s", clientAddr)
		}
	}
}

func buildSTUNResponse(req []byte, clientAddr *net.UDPAddr) []byte {
	txnID := make([]byte, 12)
	copy(txnID, req[8:20])

	xorPort := uint16(clientAddr.Port) ^ uint16(stunMagicCookie>>16)

	xorIP := binary.BigEndian.Uint32(clientAddr.IP.To4()) ^ stunMagicCookie

	attrValue := make([]byte, 8)
	attrValue[0] = 0x00
	attrValue[1] = 0x01
	binary.BigEndian.PutUint16(attrValue[2:4], xorPort)
	binary.BigEndian.PutUint32(attrValue[4:8], xorIP)

	attrLen := uint16(len(attrValue))

	attr := make([]byte, 4)
	binary.BigEndian.PutUint16(attr[0:2], 0x0020)
	binary.BigEndian.PutUint16(attr[2:4], attrLen)

	msgLen := uint16(4 + len(attrValue))

	resp := make([]byte, 20+len(attr)+len(attrValue))
	binary.BigEndian.PutUint16(resp[0:2], 0x0101)
	binary.BigEndian.PutUint16(resp[2:4], msgLen)
	binary.BigEndian.PutUint32(resp[4:8], stunMagicCookie)
	copy(resp[8:20], txnID)
	copy(resp[20:], attr)
	copy(resp[24:], attrValue)

	return resp
}

func runRelayServer(port int, fatalCh chan<- error) error {
	defer relayWg.Done()

	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		logError("server", "中继监听失败: %v", err)
		fatalCh <- err
		return err
	}
	relayListener = ln

	logInfo("server", "中继服务启动, 监听 :%d", port)

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-relayStopCh:
				return nil
			default:
				logError("server", "Accept 错误: %v", err)
				continue
			}
		}
		go handleRelayClient(conn)
	}
}

func handleRelayClient(conn net.Conn) {
	cid := atomic.AddUint32(&nextCID, 1)
	client := &relayClient{
		conn:         conn,
		channelConns: make(map[uint32]net.Conn),
		stopCh:       make(chan struct{}),
		cid:          cid,
		udpConns:     make(map[int]*net.UDPConn),
		udpSessions:  make(map[string]*udpSession),
		svcFilters:   make(map[int]*svcFilter),
		chanSvcPort:  make(map[uint32]int),
	}

	logInfo("server", "新客户端连接, client_id=%d, 来源: %s", cid, conn.RemoteAddr())

	clientsMu.Lock()
	clients[cid] = client
	clientsMu.Unlock()

	conn.SetDeadline(time.Now().Add(15 * time.Second))
	frameType, _, payload, err := readFrame(conn)
	if err != nil {
		if isNormalClose(err) {
			logWarn("server", "读取注册消息超时/断开 client_id=%d: %v", cid, err)
		} else {
			logError("server", "读取注册消息失败 client_id=%d: %v", cid, err)
		}
		clientsMu.Lock()
		delete(clients, cid)
		clientsMu.Unlock()
		conn.Close()
		return
	}
	conn.SetDeadline(time.Time{})

	if frameType != frameControl {
		logError("server", "期望控制帧, client_id=%d", cid)
		clientsMu.Lock()
		delete(clients, cid)
		clientsMu.Unlock()
		conn.Close()
		return
	}

	var msg controlMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		logError("server", "无效注册消息 client_id=%d: %v", cid, err)
		clientsMu.Lock()
		delete(clients, cid)
		clientsMu.Unlock()
		conn.Close()
		return
	}

	if msg.Type != "register" {
		logError("server", "期望 register, client_id=%d, 收到 %s", cid, msg.Type)
		clientsMu.Lock()
		delete(clients, cid)
		clientsMu.Unlock()
		conn.Close()
		return
	}

	if serverCfg.AuthKey != "" {
		if msg.AuthKey != serverCfg.AuthKey {
			logWarn("server", "客户端 %d 认证失败: auth-key 不匹配", cid)
			errResp := controlMsg{Type: "error", PublicAddr: "auth failed"}
			sendControl(conn, errResp)
			clientsMu.Lock()
			delete(clients, cid)
			clientsMu.Unlock()
			conn.Close()
			return
		}
		logDebug("server", "客户端 %d 认证通过", cid)
	}

	client.services = msg.Services
	client.features = msg.Features

	clientKey := msg.ClientKey
	if clientKey == "" {
		clientKey = msg.AuthKey + "@" + conn.RemoteAddr().String()
	}
	client.clientKey = clientKey

	if msg.IPAllow != "" || msg.IPDeny != "" {
		ipf, err := plugin.NewIPFilter(msg.IPAllow, msg.IPDeny)
		if err != nil {
			logWarn("server", "客户端 %d IP 过滤规则无效: %v", cid, err)
		} else {
			client.ipFilter = ipf
			logInfo("server", "客户端 %d 自定义 IP 过滤: allow=%s, deny=%s", cid, msg.IPAllow, msg.IPDeny)
		}
	}

	if msg.MaxConns > 0 {
		client.connLimiter = plugin.NewConnLimit(msg.MaxConns)
		logInfo("server", "客户端 %d 自定义连接数限制: %d", cid, msg.MaxConns)
	}

	if msg.RateLimit > 0 {
		client.rateLimiter = plugin.NewRateLimit(msg.RateLimit)
		logInfo("server", "客户端 %d 自定义带宽限制: %d bytes/s", cid, msg.RateLimit)
	}

	for _, svc := range msg.Services {
		sf := &svcFilter{}
		hasOverride := false

		if svc.IPAllow != "" || svc.IPDeny != "" {
			ipf, err := plugin.NewIPFilter(svc.IPAllow, svc.IPDeny)
			if err != nil {
				logWarn("server", "客户端 %d 服务 :%d IP 过滤规则无效: %v", cid, svc.RemotePort, err)
			} else {
				sf.ipFilter = ipf
				hasOverride = true
			}
		}

		if svc.MaxConns > 0 {
			sf.connLimiter = plugin.NewConnLimit(svc.MaxConns)
			hasOverride = true
		}

		if svc.RateLimit > 0 {
			sf.rateLimiter = plugin.NewRateLimit(svc.RateLimit)
			hasOverride = true
		}

		if svc.Compress {
			sf.compress = true
			hasOverride = true
		}

		if svc.WebAuth != "" {
			sf.webAuth = &webAuthConfig{
				password: svc.WebAuth,
				port:     svc.RemotePort,
			}
			hasOverride = true
		}

		if hasOverride {
			client.svcFilters[svc.RemotePort] = sf
		}
	}

	logInfo("server", "客户端注册, client_id=%d, 服务列表:", cid)
	for _, svc := range msg.Services {
		logInfo("server", "  remote_port=%d -> local_port=%d (%s)", svc.RemotePort, svc.LocalPort, svc.Proto)
	}
	if len(client.features) > 0 {
		logInfo("server", "客户端特性: %v", client.features)
	}

	publicIP := getPublicIP(conn, serverCfg)

	var clientListeners []net.Listener
	var failedServices []serviceMapping
	var assignedServices []serviceMapping
	for _, svc := range msg.Services {
		if svc.RemotePort == 0 {
			if heldPort, found := findHeldPort(clientKey, svc.LocalPort, svc.Proto); found {
				svc.RemotePort = heldPort
				logInfo("server", "复用保留端口: %d (client_key=%s, local_port=%d, proto=%s)", heldPort, clientKey, svc.LocalPort, svc.Proto)
			}
		}
		ln, assigned, err := startServiceListener(svc, client, publicIP)
		if err != nil {
			failedServices = append(failedServices, svc)
			logWarn("server", "服务启动失败: :%d/%s -> :%d, 错误: %v", svc.RemotePort, svc.Proto, svc.LocalPort, err)
		} else {
			assignedServices = append(assignedServices, assigned)
			if ln != nil {
				clientListeners = append(clientListeners, ln)
			}
		}
	}

	var serverFeatures []string
	if pluginMgr.HasCompressor() || pluginMgr.CanDecompress() {
		serverFeatures = append(serverFeatures, "compression")
	}

	okResp := controlMsg{
		Type:             "ok",
		PublicAddr:       publicIP,
		FailedServices:   failedServices,
		AssignedServices: assignedServices,
		Features:         serverFeatures,
	}
	if err := sendControl(conn, okResp); err != nil {
		logError("server", "发送 ok 失败 client_id=%d: %v", cid, err)
		client.stop()
		for _, l := range clientListeners {
			l.Close()
			listenMu.Lock()
			for port, ll := range listeners {
				if ll == l {
					delete(listeners, port)
				}
			}
			listenMu.Unlock()
		}
		clientsMu.Lock()
		delete(clients, cid)
		clientsMu.Unlock()
		conn.Close()
		return
	}

	logInfo("server", "客户端 %d 注册成功, 公网地址: %s", cid, publicIP)

	relayReadLoop(conn, client, cid)

	client.stop()
	holdPortsForClient(client.clientKey, client.services)
	for _, l := range clientListeners {
		l.Close()
		listenMu.Lock()
		for port, ll := range listeners {
			if ll == l {
				delete(listeners, port)
			}
		}
		listenMu.Unlock()
	}
	client.chanMu.Lock()
	for _, c := range client.channelConns {
		c.Close()
	}
	client.chanMu.Unlock()
	client.udpMu.Lock()
	for _, conn := range client.udpConns {
		conn.Close()
	}
	client.udpConns = nil
	client.udpSessions = nil
	client.udpMu.Unlock()
	clientsMu.Lock()
	delete(clients, cid)
	clientsMu.Unlock()
	conn.Close()
	logInfo("server", "客户端断开, client_id=%d", cid)
}

func startServiceListener(svc serviceMapping, client *relayClient, publicIP string) (net.Listener, serviceMapping, error) {
	if svc.Proto == "udp" {
		result, err := startUDPService(svc, client)
		return nil, result, err
	}
	if svc.Proto == "tcp" {
		return startTCPService(svc, client)
	}
	return nil, svc, fmt.Errorf("unsupported proto: %s", svc.Proto)
}

const webAuthCookieName = "p2p_auth"
const webAuthMaxAge = 10800

func generateAuthCookieValue(password string, port int) string {
	ts := time.Now().Unix()
	tsStr := strconv.FormatInt(ts, 16)
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte(tsStr))
	mac.Write([]byte(strconv.Itoa(port)))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return tsStr + "." + sig
}

func validateAuthCookieValue(cookieValue, password string, port int) bool {
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return false
	}
	ts, err := strconv.ParseInt(parts[0], 16, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix()-ts > webAuthMaxAge {
		return false
	}
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte(parts[0]))
	mac.Write([]byte(strconv.Itoa(port)))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expectedSig))
}

func handleWebAuth(extConn net.Conn, wa *webAuthConfig) (net.Conn, bool) {
	extConn.SetReadDeadline(time.Now().Add(10 * time.Second))

	var rawBuf bytes.Buffer
	teeReader := io.TeeReader(extConn, &rawBuf)
	br := bufio.NewReaderSize(teeReader, 4096)

	req, err := http.ReadRequest(br)
	if err != nil {
		extConn.SetReadDeadline(time.Time{})
		extConn.Close()
		return nil, false
	}

	extConn.SetReadDeadline(time.Time{})

	if cookie, err := req.Cookie(webAuthCookieName); err == nil {
		if validateAuthCookieValue(cookie.Value, wa.password, wa.port) {
			var replay bytes.Buffer
			req.Write(&replay)
			if req.Body != nil {
				io.Copy(&replay, req.Body)
				req.Body.Close()
			}
			if br.Buffered() > 0 {
				extra, _ := br.Peek(br.Buffered())
				replay.Write(extra)
			}
			return &bufferedConn{
				Conn:   extConn,
				reader: io.MultiReader(bytes.NewReader(replay.Bytes()), extConn),
			}, true
		}
	}

	if req.Method == "POST" && req.URL.Path == "/__auth__" {
		req.ParseForm()
		password := req.FormValue("password")
		redirect := req.FormValue("redirect")
		if redirect == "" {
			redirect = "/"
		}
		if redirectVal, err := url.Parse(redirect); err == nil {
			if redirectVal.IsAbs() || !strings.HasPrefix(redirect, "/") {
				redirect = "/"
			}
		} else {
			redirect = "/"
		}

		if password == wa.password {
			cookieValue := generateAuthCookieValue(wa.password, wa.port)
			resp := fmt.Sprintf("HTTP/1.1 302 Found\r\nSet-Cookie: %s=%s; Path=/; Max-Age=%d; HttpOnly; SameSite=Strict\r\nLocation: %s\r\nContent-Length: 0\r\n\r\n",
				webAuthCookieName, cookieValue, webAuthMaxAge, redirect)
			extConn.Write([]byte(resp))
			extConn.Close()
			return nil, false
		}

		resp := buildLoginPage(redirect, true)
		extConn.Write([]byte(resp))
		extConn.Close()
		return nil, false
	}

	redirect := req.URL.RequestURI()
	resp := buildLoginPage(redirect, false)
	extConn.Write([]byte(resp))
	extConn.Close()
	return nil, false
}

func buildLoginPage(redirect string, wrongPassword bool) string {
	errorMsg := ""
	if wrongPassword {
		errorMsg = `<p style="color:#ef4444;font-size:13px;margin:8px 0 0">密码错误，请重试</p>`
	}
	bgURI := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(authBgData)
	return "HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n" +
		`<!DOCTYPE html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"><title>认证</title><style>body{font-family:-apple-system,sans-serif;background:#0f172a url(` + bgURI + `) center/cover no-repeat;color:#f8fafc;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0}.card{background:rgba(30,41,59,0.85);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid #334155;border-radius:12px;padding:32px;width:320px}h2{margin:0 0 8px;font-size:20px}p{color:#94a3b8;font-size:14px;margin:0 0 24px}input{width:100%;padding:10px 12px;background:#020617;border:1px solid #334155;border-radius:8px;color:#f8fafc;font-size:14px;box-sizing:border-box;outline:none}input:focus{border-color:#22c55e}button{width:100%;padding:10px;background:#22c55e;color:#020617;border:none;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer;margin-top:16px}button:hover{background:#16a34a}</style></head><body><div class="card"><h2>&#x1f512; 访问认证</h2><p>此服务需要密码验证</p>` +
		errorMsg +
		`<form method="POST" action="/__auth__"><input type="hidden" name="redirect" value="` + redirect + `"><input type="password" name="password" placeholder="输入密码" autofocus><button type="submit">登录</button></form></div></body></html>`
}

func startTCPService(svc serviceMapping, client *relayClient) (net.Listener, serviceMapping, error) {
	actualPort := svc.RemotePort
	if actualPort == 0 {
		actualPort = allocateRandomPort()
	}

	listenMu.Lock()
	if _, exists := listeners[actualPort]; exists {
		listenMu.Unlock()
		logWarn("server", "端口 %d 已被占用", actualPort)
		return nil, svc, fmt.Errorf("port %d already in use", actualPort)
	}

	ln, err := listenTCP(":" + strconv.Itoa(actualPort))
	if err != nil {
		listenMu.Unlock()
		logError("server", "监听 :%d 失败: %v", actualPort, err)
		return nil, svc, err
	}
	listeners[actualPort] = ln
	listenMu.Unlock()

	result := svc
	result.RemotePort = actualPort

	logInfo("server", "TCP 服务监听启动: :%d -> 客户端本地 :%d", actualPort, svc.LocalPort)

	go func() {
		defer func() {
			listenMu.Lock()
			delete(listeners, actualPort)
			listenMu.Unlock()
			ln.Close()
		}()

		for {
			select {
			case <-client.stopCh:
				return
			default:
			}

			extConn, err := ln.Accept()
			if err != nil {
				select {
				case <-client.stopCh:
					return
				default:
					logError("server", "Accept :%d 错误: %v", actualPort, err)
					return
				}
			}

			if !pluginMgr.OnAccept("tcp", extConn.RemoteAddr()) {
				logWarn("server", "IP 过滤拒绝(全局): %s", extConn.RemoteAddr())
				extConn.Close()
				continue
			}
			ipf := client.ipFilter
			cl := client.connLimiter
			if sf, ok := client.svcFilters[actualPort]; ok {
				if sf.ipFilter != nil {
					ipf = sf.ipFilter
				}
				if sf.connLimiter != nil {
					cl = sf.connLimiter
				}
			}

			if ipf != nil && !ipf.OnAccept("tcp", extConn.RemoteAddr()) {
				logWarn("server", "IP 过滤拒绝(客户端规则): %s", extConn.RemoteAddr())
				extConn.Close()
				continue
			}

			if cl != nil && !cl.OnAccept("tcp", extConn.RemoteAddr()) {
				logWarn("server", "连接数限制拒绝: %s (当前 %d/%d)", extConn.RemoteAddr(), cl.Current(), cl.Max())
				extConn.Close()
				continue
			}

			if sf, ok := client.svcFilters[actualPort]; ok && sf.webAuth != nil {
				newConn, ok := handleWebAuth(extConn, sf.webAuth)
				if !ok {
					continue
				}
				extConn = newConn
			}

			channelID := atomic.AddUint32(&nextCID, 1)

			logDebug("server", "外部连接 accept, channel_id=%d, 来源: %s", channelID, extConn.RemoteAddr())

			client.chanMu.Lock()
			client.channelConns[channelID] = extConn
			client.chanSvcPort[channelID] = actualPort
			client.chanMu.Unlock()

			action := pluginMgr.OnOpen("tcp", extConn.RemoteAddr().String(), channelID, svc.LocalPort)
			if action.Close {
				logInfo("server", "插件要求断开连接: channel=%d, 原因: %s", channelID, action.Reason)
				pluginMgr.OnClose(channelID)
				if cl != nil {
					cl.OnClose()
				}
				extConn.Close()
				return
			}
			if cl != nil {
				cl.OnOpen()
			}

			newConnMsg := controlMsg{
				Type:       "new_conn",
				ChannelID:  channelID,
				LocalPort:  svc.LocalPort,
				RemoteAddr: extConn.RemoteAddr().String(),
			}
			logDebug("server", "发送 new_conn 给客户端, channel_id=%d, local_port=%d", channelID, svc.LocalPort)
			if err := client.sendControl(newConnMsg); err != nil {
				logError("server", "发送 new_conn 失败: %v", err)
				client.chanMu.Lock()
				delete(client.channelConns, channelID)
				client.chanMu.Unlock()
				pluginMgr.OnClose(channelID)
				if client.connLimiter != nil {
					client.connLimiter.OnClose()
				}
				extConn.Close()
				return
			}

			go pipeExternalToClient(extConn, client, channelID)
		}
	}()

	return ln, result, nil
}

func startUDPService(svc serviceMapping, client *relayClient) (serviceMapping, error) {
	actualPort := svc.RemotePort
	if actualPort == 0 {
		actualPort = allocateRandomPort()
	}

	addr, err := net.ResolveUDPAddr("udp", ":"+strconv.Itoa(actualPort))
	if err != nil {
		return svc, err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		logError("server", "UDP 监听 :%d 失败: %v", actualPort, err)
		return svc, err
	}

	result := svc
	result.RemotePort = actualPort

	client.udpMu.Lock()
	client.udpConns[actualPort] = conn
	client.udpMu.Unlock()

	logInfo("server", "UDP 服务监听启动: :%d -> 客户端本地 :%d", actualPort, svc.LocalPort)

	go udpReadLoop(conn, client, result)
	go udpSessionCleanup(client)

	return result, nil
}

func udpReadLoop(conn *net.UDPConn, client *relayClient, svc serviceMapping) {
	defer conn.Close()

	buf := make([]byte, 65535)
	for {
		select {
		case <-client.stopCh:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if isNormalClose(err) {
				return
			}
			logError("server", "UDP 读取错误 :%d: %v", svc.RemotePort, err)
			return
		}

		if !pluginMgr.OnAccept("udp", remoteAddr) {
			logDebug("server", "IP 过滤拒绝 UDP(全局): %s", remoteAddr)
			continue
		}

		ipf := client.ipFilter
		cl := client.connLimiter
		if sf, ok := client.svcFilters[svc.RemotePort]; ok {
			if sf.ipFilter != nil {
				ipf = sf.ipFilter
			}
			if sf.connLimiter != nil {
				cl = sf.connLimiter
			}
		}

		if ipf != nil && !ipf.OnAccept("udp", remoteAddr) {
			logDebug("server", "IP 过滤拒绝 UDP(服务规则): %s", remoteAddr)
			continue
		}

		sessionKey := remoteAddr.String()

		client.udpMu.Lock()
		session, exists := client.udpSessions[sessionKey]
		if !exists {
			if cl != nil && !cl.OnAccept("udp", remoteAddr) {
				logDebug("server", "连接数限制拒绝 UDP: %s (当前 %d/%d)", remoteAddr, cl.Current(), cl.Max())
				client.udpMu.Unlock()
				continue
			}
			session = &udpSession{
				channelID:  atomic.AddUint32(&nextCID, 1),
				localPort:  svc.LocalPort,
				remoteAddr: remoteAddr,
				udpConn:    conn,
				lastActive: time.Now(),
			}
			client.udpSessions[sessionKey] = session
			client.chanSvcPort[session.channelID] = svc.RemotePort

			action := pluginMgr.OnOpen("udp", sessionKey, session.channelID, svc.LocalPort)
			if action.Close {
				logInfo("server", "插件要求断开连接: channel=%d, 原因: %s", session.channelID, action.Reason)
				delete(client.udpSessions, sessionKey)
				delete(client.chanSvcPort, session.channelID)
				if cl != nil {
					cl.OnClose()
				}
				continue
			}
			if cl != nil {
				cl.OnOpen()
			}

			newConnMsg := controlMsg{
				Type:       "new_udp_conn",
				ChannelID:  session.channelID,
				LocalPort:  svc.LocalPort,
				Proto:      "udp",
				RemoteAddr: remoteAddr.String(),
			}
			client.sendControl(newConnMsg)
			logDebug("server", "新 UDP session: %s, channel_id=%d", sessionKey, session.channelID)
		}
		session.lastActive = time.Now()
		client.udpMu.Unlock()

		action := pluginMgr.OnData(session.channelID, "rx", n)
		if action.Close {
			logInfo("server", "插件要求断开连接: channel=%d, 原因: %s", session.channelID, action.Reason)
			client.udpMu.Lock()
			delete(client.udpSessions, sessionKey)
			delete(client.chanSvcPort, session.channelID)
			client.udpMu.Unlock()
			pluginMgr.OnClose(session.channelID)
			if cl != nil {
				cl.OnClose()
			}
			continue
		}

		if globalRateLimiter != nil {
			globalRateLimiter.Wait(n)
		}
		rl := client.rateLimiter
		if svcPort, ok := client.chanSvcPort[session.channelID]; ok {
			if sf, ok2 := client.svcFilters[svcPort]; ok2 && sf.rateLimiter != nil {
				rl = sf.rateLimiter
			}
		}
		if rl != nil {
			rl.Wait(n)
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		ft := byte(frameData)
		if pluginMgr.HasCompressor() && clientSupportsCompression(client) {
			compressed, ok := pluginMgr.Compress(data)
			if ok {
				data = compressed
				ft = frameDataCompressed
			}
		}

		if err := client.writeFrameLocked(ft, session.channelID, data); err != nil {
			if !isNormalClose(err) {
				logError("server", "写入 UDP data frame channel %d 错误: %v", session.channelID, err)
			}
			return
		}
	}
}

func udpSessionCleanup(client *relayClient) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-client.stopCh:
			return
		case <-ticker.C:
			client.udpMu.Lock()
			now := time.Now()
			for key, sess := range client.udpSessions {
				if now.Sub(sess.lastActive) > 120*time.Second {
					client.writeFrameLocked(frameClose, sess.channelID, nil)
					pluginMgr.OnClose(sess.channelID)
					svcPort, _ := client.chanSvcPort[sess.channelID]
					cl := client.connLimiter
					if sf, ok := client.svcFilters[svcPort]; ok && sf.connLimiter != nil {
						cl = sf.connLimiter
					}
					if cl != nil {
						cl.OnClose()
					}
					delete(client.udpSessions, key)
					delete(client.chanSvcPort, sess.channelID)
					logDebug("server", "UDP session 超时清理: %s", key)
				}
			}
			client.udpMu.Unlock()
		}
	}
}

func pipeExternalToClient(extConn net.Conn, client *relayClient, channelID uint32) {
	defer func() {
		extConn.Close()
		client.chanMu.Lock()
		_, existed := client.channelConns[channelID]
		delete(client.channelConns, channelID)
		svcPort, _ := client.chanSvcPort[channelID]
		delete(client.chanSvcPort, channelID)
		client.chanMu.Unlock()
		if existed {
			pluginMgr.OnClose(channelID)
			cl := client.connLimiter
			if sf, ok := client.svcFilters[svcPort]; ok && sf.connLimiter != nil {
				cl = sf.connLimiter
			}
			if cl != nil {
				cl.OnClose()
			}
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := extConn.Read(buf)
		if err != nil {
			if isNormalClose(err) {
				logDebug("server", "外部连接 channel %d 关闭: %v", channelID, err)
			} else {
				logWarn("server", "读取外部连接 channel %d 错误: %v", channelID, err)
			}
			client.writeFrameLocked(frameClose, channelID, nil)
			return
		}

		action := pluginMgr.OnData(channelID, "rx", n)
		if action.Close {
			logInfo("server", "插件要求断开连接: channel=%d, 原因: %s", channelID, action.Reason)
			return
		}

		if globalRateLimiter != nil {
			globalRateLimiter.Wait(n)
		}
		rl2 := client.rateLimiter
		if svcPort, ok := client.chanSvcPort[channelID]; ok {
			if sf, ok2 := client.svcFilters[svcPort]; ok2 && sf.rateLimiter != nil {
				rl2 = sf.rateLimiter
			}
		}
		if rl2 != nil {
			rl2.Wait(n)
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		ft := byte(frameData)
		if pluginMgr.HasCompressor() && clientSupportsCompression(client) {
			compressed, ok := pluginMgr.Compress(data)
			if ok {
				data = compressed
				ft = frameDataCompressed
			}
		}

		if err := client.writeFrameLocked(ft, channelID, data); err != nil {
			if isNormalClose(err) {
				logDebug("server", "写入客户端 channel %d 关闭: %v", channelID, err)
			} else {
				logError("server", "写入客户端 channel %d 错误: %v", channelID, err)
			}
			return
		}
	}
}

func relayReadLoop(conn net.Conn, client *relayClient, cid uint32) {
	for {
		frameType, channelID, payload, err := readFrame(conn)
		if err != nil {
			if isNormalClose(err) {
				logDebug("server", "中继读取关闭 client_id=%d: %v", cid, err)
			} else {
				logWarn("server", "中继读取错误 client_id=%d: %v", cid, err)
			}
			return
		}

		logDebug("server", "收到 frame: type=%s channel_id=%d len=%d", frameTypeName(frameType), channelID, len(payload))

		switch frameType {
		case frameControl:
			var msg controlMsg
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			if msg.Type == "conn_ready" {
				logDebug("server", "收到 conn_ready, channel_id=%d", channelID)
			}
			if msg.Type == "ping" {
				pong := controlMsg{Type: "pong"}
				client.sendControl(pong)
				logDebug("server", "收到 ping, 回复 pong")
			}

		case frameData, frameDataCompressed:
			data := payload
			if frameType == frameDataCompressed {
				if pluginMgr.CanDecompress() {
					decompressed, err := pluginMgr.Decompress(payload)
					if err != nil {
						logError("server", "解压 channel %d 失败: %v", channelID, err)
						continue
					}
					data = decompressed
				} else {
					logError("server", "收到压缩帧但无法解压, channel_id=%d", channelID)
					continue
				}
			}

			action := pluginMgr.OnData(channelID, "tx", len(data))
			if action.Close {
				logInfo("server", "插件要求断开连接: channel=%d, 原因: %s", channelID, action.Reason)
				client.chanMu.Lock()
				if ec, ok := client.channelConns[channelID]; ok {
					ec.Close()
				}
				svcPort, _ := client.chanSvcPort[channelID]
				delete(client.channelConns, channelID)
				delete(client.chanSvcPort, channelID)
				client.chanMu.Unlock()
				pluginMgr.OnClose(channelID)
				cl := client.connLimiter
				if sf, ok := client.svcFilters[svcPort]; ok && sf.connLimiter != nil {
					cl = sf.connLimiter
				}
				if cl != nil {
					cl.OnClose()
				}
				continue
			}

			if globalRateLimiter != nil {
				globalRateLimiter.Wait(len(data))
			}
			rl3 := client.rateLimiter
			if svcPort, ok := client.chanSvcPort[channelID]; ok {
				if sf, ok2 := client.svcFilters[svcPort]; ok2 && sf.rateLimiter != nil {
					rl3 = sf.rateLimiter
				}
			}
			if rl3 != nil {
				rl3.Wait(len(data))
			}

			client.chanMu.RLock()
			extConn, ok := client.channelConns[channelID]
			client.chanMu.RUnlock()
			if ok && extConn != nil {
				if _, err := extConn.Write(data); err != nil {
					if isNormalClose(err) {
						logDebug("server", "写入外部连接 channel %d 关闭: %v", channelID, err)
					} else {
						logError("server", "写入外部连接 channel %d 错误: %v", channelID, err)
					}
				}
			} else {
				client.udpMu.RLock()
				var targetAddr *net.UDPAddr
				var targetConn *net.UDPConn
				for _, sess := range client.udpSessions {
					if sess.channelID == channelID {
						targetAddr = sess.remoteAddr
						targetConn = sess.udpConn
						break
					}
				}
				client.udpMu.RUnlock()
				if targetAddr != nil && targetConn != nil {
					targetConn.WriteToUDP(data, targetAddr)
				} else {
					logWarn("server", "收到未知 channel_id=%d 的数据帧", channelID)
				}
			}

		case frameClose:
			client.chanMu.Lock()
			extConn, tcpExisted := client.channelConns[channelID]
			svcPort, svcPortOk := client.chanSvcPort[channelID]
			if tcpExisted {
				extConn.Close()
				delete(client.channelConns, channelID)
				delete(client.chanSvcPort, channelID)
			}
			client.chanMu.Unlock()

			if tcpExisted {
				pluginMgr.OnClose(channelID)
				cl := client.connLimiter
				if svcPortOk {
					if sf, ok := client.svcFilters[svcPort]; ok && sf.connLimiter != nil {
						cl = sf.connLimiter
					}
				}
				if cl != nil {
					cl.OnClose()
				}
			}

			client.udpMu.Lock()
			var udpExisted bool
			for key, sess := range client.udpSessions {
				if sess.channelID == channelID {
					delete(client.udpSessions, key)
					delete(client.chanSvcPort, channelID)
					udpExisted = true
					logDebug("server", "UDP session 关闭: %s, channel_id=%d", key, channelID)
					break
				}
			}
			client.udpMu.Unlock()

			if udpExisted {
				pluginMgr.OnClose(channelID)
				cl := client.connLimiter
				if svcPortOk {
					if sf, ok := client.svcFilters[svcPort]; ok && sf.connLimiter != nil {
						cl = sf.connLimiter
					}
				}
				if cl != nil {
					cl.OnClose()
				}
			}
		}
	}
}

func (rc *relayClient) sendControl(msg controlMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return rc.writeFrameLocked(frameControl, 0, data)
}

func (rc *relayClient) writeFrameLocked(frameType byte, channelID uint32, payload []byte) error {
	rc.writeMu.Lock()
	defer rc.writeMu.Unlock()
	return writeFrame(rc.conn, frameType, channelID, payload)
}

func sendControl(conn net.Conn, msg controlMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return writeFrame(conn, frameControl, 0, data)
}

func writeFrame(conn net.Conn, frameType byte, channelID uint32, payload []byte) error {
	header := make([]byte, frameHeaderSize)
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:5], channelID)
	binary.BigEndian.PutUint32(header[5:9], uint32(len(payload)))

	logDebug("server", "发送 frame: type=%s channel_id=%d len=%d", frameTypeName(frameType), channelID, len(payload))

	if _, err := conn.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := conn.Write(payload)
		return err
	}
	return nil
}

func readFrame(conn net.Conn) (byte, uint32, []byte, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(conn, header); err != nil {
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
		if _, err := io.ReadFull(conn, payload); err != nil {
			return 0, 0, nil, err
		}
	}

	return frameType, channelID, payload, nil
}

func getPublicIP(conn net.Conn, cfg Config) string {
	if cfg.PublicAddr != "" {
		return cfg.PublicAddr
	}
	addr := conn.LocalAddr().(*net.TCPAddr)
	return addr.IP.String()
}

var serverHelpText = `signal-server - p2p-tun 中继/信令服务器

将内网服务暴露到公网的中继服务器，支持 STUN 探测和 TCP/UDP 中继。

用法:
  signal-server [选项]

基本选项:
  -stun-port int        STUN 服务端口，0=不启动 (默认 0)
  -relay-port int       中继控制端口 (默认 9000)
  -public-addr string   服务器公网地址，用于客户端显示 (如 example.com)
  -auth-key string      客户端认证密钥，客户端需使用相同密钥才能连接
  -verbose              输出详细调试日志

安全与限流:
  -ip-allow string      IP 白名单，CIDR 格式，逗号分隔 (如 10.0.0.0/8,192.168.0.0/16)
  -ip-deny string       IP 黑名单，CIDR 格式，逗号分隔
  -max-conns int        全局最大并发连接数，0=不限 (默认 0)
  -rate-limit int       全局带宽限制 (字节/秒)，0=不限 (默认 0)

数据处理:
  -compress             启用 LZ4 数据压缩，减少传输带宽
  -traffic-log string   流量日志文件路径，记录连接详情

动态插件:
  -plugin-dir string    动态插件目录 (默认不加载)
                        - 如果目录下有 plugin.json，直接加载该目录作为单个插件
                        - 如果目录下没有 plugin.json，扫描子目录加载所有插件
                        - 在 plugin.json 中设置 "enabled": false 可禁用插件
  -plugin-timeout dur   插件调用超时，防止插件卡住阻塞主程序 (默认 5s)
                        插件超时是指: 主程序调用插件后等待响应的最长时间
                        超时后主程序跳过该插件，继续处理连接

示例:
  # 基本启动
  signal-server -relay-port 9000 -public-addr myserver.com

  # 带认证和限流
  signal-server -relay-port 9000 -auth-key mysecret123 -max-conns 1000 -rate-limit 1048576

  # 带动态插件
  signal-server -relay-port 9000 -plugin-dir ./plugins -plugin-timeout 3s

  # 完整配置
  signal-server -stun-port 3478 -relay-port 9000 -public-addr myserver.com \
    -auth-key mysecret123 -ip-deny 10.0.0.0/8 -max-conns 500 \
    -compress -traffic-log /var/log/traffic.log -plugin-dir ./plugins

Systemd 服务 (Linux):
  创建 /etc/systemd/system/signal-server.service:
  
  [Unit]
  Description=p2p-tun Signal Server
  After=network.target

  [Service]
  Type=simple
  ExecStart=/usr/local/bin/signal-server -relay-port 9000 -public-addr myserver.com
  Restart=always

  [Install]
  WantedBy=multi-user.target

  然后: systemctl enable --now signal-server
`
