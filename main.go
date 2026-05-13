package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"p2p_tun/forward"
	"p2p_tun/keepalive"
	"p2p_tun/logutil"
	"p2p_tun/plugin"
	"p2p_tun/relay"
	"p2p_tun/stun"
	"p2p_tun/upnp"
)

const version = "v2.1"

type AppStatus struct {
	Running        bool               `json:"running"`
	Mode           string             `json:"mode"`
	PublicAddr     string             `json:"public_addr"`
	LocalPorts     string             `json:"local_ports"`
	NATType        string             `json:"nat_type"`
	StartedAt      time.Time          `json:"-"`
	UptimeSeconds  int64              `json:"uptime_seconds"`
	BytesIn        int64              `json:"bytes_in"`
	BytesOut       int64              `json:"bytes_out"`
	ActiveChannels int                `json:"active_channels"`
	LastError      string             `json:"last_error"`
	Services       []relay.ServiceMap `json:"services"`
	mu             sync.RWMutex
}

var (
	appStatus    AppStatus
	relayClient  *relay.Client
	stopCh       chan struct{}
	statusMu     sync.RWMutex
	guiToken     string
	authKeyVal   string
	compressVal  bool
	ipAllowVal   string
	ipDenyVal    string
	maxConnsVal  int
	rateLimitVal int64
)

func (s *AppStatus) SetRunning(mode, publicAddr string) {
	s.mu.Lock()
	s.Running = true
	s.Mode = mode
	s.PublicAddr = publicAddr
	s.StartedAt = time.Now()
	s.LastError = ""
	s.mu.Unlock()
}

func (s *AppStatus) SetStopped(err string) {
	s.mu.Lock()
	s.Running = false
	s.LastError = err
	s.mu.Unlock()
}

func (s *AppStatus) SetNATType(natType string) {
	s.mu.Lock()
	s.NATType = natType
	s.mu.Unlock()
}

func (s *AppStatus) ToJSON() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	uptime := int64(0)
	if !s.StartedAt.IsZero() {
		uptime = int64(time.Since(s.StartedAt).Seconds())
	}

	return map[string]interface{}{
		"running":         s.Running,
		"mode":            s.Mode,
		"public_addr":     s.PublicAddr,
		"local_ports":     s.LocalPorts,
		"nat_type":        s.NATType,
		"uptime_seconds":  uptime,
		"bytes_in":        s.BytesIn,
		"bytes_out":       s.BytesOut,
		"active_channels": s.ActiveChannels,
		"last_error":      s.LastError,
		"services":        s.Services,
	}
}

func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 登录接口和主页面无需认证（主页面包含登录表单）
		if r.URL.Path == "/api/login" || r.URL.Path == "/" {
			next(w, r)
			return
		}
		token := r.Header.Get("Authorization")
		if strings.HasPrefix(token, "Bearer ") {
			token = token[7:]
		} else {
			cookie, err := r.Cookie("gui_token")
			if err == nil {
				token = cookie.Value
			}
		}
		if token != guiToken {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		next(w, r)
	}
}

func parsePorts(s string) []int {
	var ports []int
	for _, part := range strings.Split(s, ",") {
		p, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || p <= 0 || p > 65535 {
			continue
		}
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		ports = []int{8080}
	}
	return ports
}

func protoEnabled(proto, want string) bool {
	if proto == "both" {
		return true
	}
	return proto == want
}

func main() {
	localPorts := flag.String("local", "8080", "本地服务端口，多个用逗号分隔，如 8080,22,3306")
	preferPorts := flag.String("port", "0", "期望的公网端口，多个用逗号分隔，0=自动匹配local")
	upnpFlag := flag.Bool("upnp", false, "启用 UPnP 端口映射（默认关闭）")
	stunServer := flag.String("stun", "", "STUN 服务器地址，留空则不使用 STUN（如 stun.l.google.com:19302）")
	relayServer := flag.String("relay", "", "中继服务器地址 ip:port")
	proto := flag.String("proto", "tcp", "协议 tcp/udp/both（both 表示同时启用 TCP 和 UDP）")
	verboseFlag := flag.Bool("verbose", false, "输出详细调试日志")
	guiFlag := flag.Bool("gui", false, "启动 GUI 界面")
	authKey := flag.String("auth-key", "", "中继认证密钥，需与服务端一致")
	guiTokenFlag := flag.String("gui-token", "", "GUI 认证 token，留空则自动生成")
	compressFlag := flag.Bool("compress", false, "启用数据压缩 (lz4)")
	ipAllow := flag.String("ip-allow", "", "IP 白名单 (CIDR, 逗号分隔)")
	ipDeny := flag.String("ip-deny", "", "IP 黑名单 (CIDR, 逗号分隔)")
	maxConns := flag.Int("max-conns", 0, "最大并发连接数，0=不限")
	rateLimit := flag.Int64("rate-limit", 0, "带宽限制 (字节/秒)，0=不限")

	flag.Usage = func() {
		fmt.Print(helpText)
	}

	flag.Parse()

	logutil.SetVerbose(*verboseFlag)

	if len(os.Args) == 1 {
		flag.Usage()
		return
	}

	authKeyVal = *authKey
	guiToken = *guiTokenFlag
	compressVal = *compressFlag
	ipAllowVal = *ipAllow
	ipDenyVal = *ipDeny
	maxConnsVal = *maxConns
	rateLimitVal = *rateLimit

	logutil.Info("main", "p2p-tun %s 启动", version)
	logutil.Info("main", "参数: local=%s, port=%s, upnp=%v, stun=%s, relay=%s, proto=%s, verbose=%v, gui=%v",
		*localPorts, *preferPorts, *upnpFlag, *stunServer, *relayServer, *proto, *verboseFlag, *guiFlag)

	appStatus.LocalPorts = *localPorts

	if *guiFlag {
		startGUI()
	} else {
		runCLI(*localPorts, *preferPorts, *upnpFlag, *stunServer, *relayServer, *proto)
	}
}

func runCLI(localPortsStr, preferPortsStr string, upnpEnabled bool, stunServer, relayServer, proto string) {
	stopCh = make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		logutil.Info("main", "正在退出...")
		close(stopCh)
		os.Exit(0)
	}()

	runTunnel(localPortsStr, preferPortsStr, upnpEnabled, stunServer, relayServer, proto, compressVal, ipAllowVal, ipDenyVal, maxConnsVal, rateLimitVal)
}

func runTunnel(localPortsStr, preferPortsStr string, upnpEnabled bool, stunServer, relayServer, proto string, compress bool, ipAllow, ipDeny string, maxConns int, rateLimit int64) {
	localPorts := parsePorts(localPortsStr)
	preferPorts := parsePorts(preferPortsStr)

	if upnpEnabled {
		logutil.Info("main", "进入第一层: 尝试 UPnP")
		gw, err := upnp.DiscoverGateway()
		if err == nil {
			publicIP, err := gw.GetExternalIP()
			if err == nil {
				allOK := true
				var mappedPorts []string
				for _, currentProto := range []string{"tcp", "udp"} {
					if !protoEnabled(proto, currentProto) {
						continue
					}
					for i, lp := range localPorts {
						extPort := lp
						if i < len(preferPorts) && preferPorts[i] != 0 {
							extPort = preferPorts[i]
						}
						err = gw.AddPortMapping(extPort, lp, strings.ToUpper(currentProto), "p2p-tun")
						if err != nil {
							allOK = false
							break
						}
						mappedPorts = append(mappedPorts, fmt.Sprintf("%s:%d/%s->%d", publicIP, extPort, strings.ToUpper(currentProto), lp))
						defer gw.DeletePortMapping(extPort, strings.ToUpper(currentProto))
					}
					if !allOK {
						break
					}
				}
				if allOK && len(mappedPorts) > 0 {
					publicAddr := strings.Join(mappedPorts, ", ")
					logutil.Info("main", "UPnP 映射成功: %s", publicAddr)
					appStatus.SetRunning("upnp", publicAddr)

					for _, currentProto := range []string{"tcp", "udp"} {
						if !protoEnabled(proto, currentProto) {
							continue
						}
						for i, lp := range localPorts {
							extPort := lp
							if i < len(preferPorts) && preferPorts[i] != 0 {
								extPort = preferPorts[i]
							}
							listenAddr := ":" + strconv.Itoa(extPort)
							targetAddr := "127.0.0.1:" + strconv.Itoa(lp)
							if currentProto == "tcp" {
								go forward.ForwardTCP(listenAddr, targetAddr)
							} else {
								go forward.ForwardUDP(listenAddr, targetAddr)
							}
						}
					}

					select {}
				}
			}
		}
		logutil.Info("main", "UPnP 失败: %v", err)
	}

	if stunServer != "" {
		logutil.Info("main", "进入第二层: STUN 探测 NAT 类型")
		natType, publicAddr, stunConn, err := stun.DetectNATType(stunServer)
		if err != nil {
			logutil.Info("main", "STUN 探测失败: %v", err)
			appStatus.SetNATType("unknown")
			stunConn = nil
		} else {
			appStatus.SetNATType(natType)
			logutil.Info("main", "NAT 类型: %s, 公网地址: %s", natType, publicAddr)

			if natType == "full-cone" {
				publicAddrStr := publicAddr.String()
				logutil.Info("main", "NAT1 Full Cone: %s", publicAddr)
				appStatus.SetRunning("full-cone", publicAddrStr)
				go keepalive.Keepalive(stunConn, stunServer, 25*time.Second, stopCh)

				for _, currentProto := range []string{"tcp", "udp"} {
					if !protoEnabled(proto, currentProto) {
						continue
					}
					for i, lp := range localPorts {
						listenPort := publicAddr.Port + i
						listenAddr := ":" + strconv.Itoa(listenPort)
						targetAddr := "127.0.0.1:" + strconv.Itoa(lp)
						if currentProto == "tcp" {
							go forward.ForwardTCP(listenAddr, targetAddr)
						} else {
							go forward.ForwardUDP(listenAddr, targetAddr)
						}
						logutil.Info("main", "Full Cone %s 转发: :%d -> 127.0.0.1:%d", strings.ToUpper(currentProto), listenPort, lp)
					}
				}

				select {}
			}
			logutil.Info("main", "NAT 类型 %s，不适合直连", natType)
		}

		if stunConn != nil {
			stunConn.Close()
		}
	}

	logutil.Info("main", "进入第三层: 公网 VPS 中继")
	if relayServer == "" {
		errMsg := "需要提供公网中继服务器地址，使用 -relay ip:port 参数"
		logutil.Error("main", "%s", errMsg)
		appStatus.SetStopped(errMsg)
		return
	}

	var services []relay.ServiceMap
	for i, lp := range localPorts {
		remotePort := lp
		if i < len(preferPorts) && preferPorts[i] != 0 {
			remotePort = preferPorts[i]
		}
		for _, currentProto := range []string{"tcp", "udp"} {
			if !protoEnabled(proto, currentProto) {
				continue
			}
			services = append(services, relay.ServiceMap{
				LocalPort:  lp,
				RemotePort: remotePort,
				Proto:      currentProto,
			})
		}
	}

	appStatus.mu.Lock()
	appStatus.Services = services
	appStatus.mu.Unlock()

	var compressor *plugin.Compression
	if compress {
		compressor = plugin.NewCompression(1, 128)
		logutil.Info("main", "数据压缩已启用 (lz4, min_size=128)")
	}

	relayClient = &relay.Client{
		ServerAddr: relayServer,
		AuthKey:    authKeyVal,
		Services:   services,
		Compressor: compressor,
		IPAllow:    ipAllow,
		IPDeny:     ipDeny,
		MaxConns:   maxConns,
		RateLimit:  rateLimit,
	}

	logutil.Info("main", "使用中继模式")
	if err := relayClient.Connect(); err != nil {
		logutil.Error("main", "中继连接失败: %v", err)
		appStatus.SetStopped(err.Error())
		return
	}

	publicAddrStr := relayServer
	appStatus.SetRunning("relay", publicAddrStr)

	if stopCh != nil {
		<-stopCh
	}
}

func startGUI() {
	stopCh = make(chan struct{})

	if guiToken == "" {
		guiToken = generateToken()
	}

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			logutil.RecordTraffic()
		}
	}()

	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/", authMiddleware(handleIndex))
	http.HandleFunc("/api/status", authMiddleware(handleStatus))
	http.HandleFunc("/api/start", authMiddleware(handleStart))
	http.HandleFunc("/api/stop", authMiddleware(handleStop))
	http.HandleFunc("/api/logs", authMiddleware(handleLogs))
	http.HandleFunc("/api/traffic", authMiddleware(handleTraffic))
	http.HandleFunc("/api/connections", authMiddleware(handleConnections))

	addr := "127.0.0.1:19999"
	logutil.Info("main", "GUI 服务启动: http://%s", addr)
	logutil.Info("main", "GUI 认证 token: %s", guiToken)
	logutil.Info("main", "访问 http://%s 后输入此 token 登录", addr)

	go openBrowser("http://" + addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		logutil.Error("main", "GUI 服务错误: %v", err)
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Token != guiToken {
		http.Error(w, `{"error":"invalid token"}`, 403)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": guiToken, "status": "ok"})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(guiHTML))
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data := appStatus.ToJSON()
	data["bytes_in"] = logutil.GetBytesIn()
	data["bytes_out"] = logutil.GetBytesOut()
	data["active_channels"] = logutil.GetActiveChans()
	if relayClient != nil {
		data["total_conns"] = relayClient.GetTotalConns()
	} else {
		data["total_conns"] = int64(0)
	}
	json.NewEncoder(w).Encode(data)
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	var params struct {
		LocalPorts  string `json:"local_ports"`
		RemotePorts string `json:"remote_ports"`
		Upnp        bool   `json:"upnp"`
		StunServer  string `json:"stun_server"`
		RelayAddr   string `json:"relay_addr"`
		Proto       string `json:"proto"`
		Compress    bool   `json:"compress"`
		IPAllow     string `json:"ip_allow"`
		IPDeny      string `json:"ip_deny"`
		MaxConns    int    `json:"max_conns"`
		RateLimit   int64  `json:"rate_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	if params.LocalPorts == "" {
		params.LocalPorts = "8080"
	}
	if params.RemotePorts == "" {
		params.RemotePorts = "0"
	}
	if params.Proto == "" {
		params.Proto = "tcp"
	}

	appStatus.mu.RLock()
	if appStatus.Running {
		appStatus.mu.RUnlock()
		http.Error(w, "Already running", 400)
		return
	}
	appStatus.mu.RUnlock()

	appStatus.mu.Lock()
	appStatus.LocalPorts = params.LocalPorts
	appStatus.mu.Unlock()

	go runTunnel(params.LocalPorts, params.RemotePorts, params.Upnp, params.StunServer, params.RelayAddr, params.Proto, params.Compress, params.IPAllow, params.IPDeny, params.MaxConns, params.RateLimit)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	if stopCh != nil {
		close(stopCh)
		stopCh = make(chan struct{})
	}
	if relayClient != nil {
		relayClient.Close()
		relayClient = nil
	}

	appStatus.SetStopped("stopped by user")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logutil.GetRecentLogs())
}

func handleTraffic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"points": logutil.GetTrafficHistory(),
	})
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if relayClient == nil {
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}
	conns := relayClient.GetConnections()
	type connOutput struct {
		ChannelID   uint32 `json:"channel_id"`
		RemoteAddr  string `json:"remote_addr"`
		LocalPort   int    `json:"local_port"`
		Proto       string `json:"proto"`
		ConnectedAt string `json:"connected_at"`
		Duration    string `json:"duration"`
		BytesIn     int64  `json:"bytes_in"`
		BytesOut    int64  `json:"bytes_out"`
	}
	result := make([]connOutput, len(conns))
	for i, c := range conns {
		dur := time.Since(c.ConnectedAt)
		durStr := fmt.Sprintf("%ds", int(dur.Seconds()))
		if dur >= 60*time.Second {
			durStr = fmt.Sprintf("%dm%ds", int(dur.Minutes()), int(dur.Seconds())%60)
		}
		if dur >= 3600*time.Second {
			durStr = fmt.Sprintf("%dh%dm", int(dur.Hours()), int(dur.Minutes())%60)
		}
		result[i] = connOutput{
			ChannelID:   c.ChannelID,
			RemoteAddr:  c.RemoteAddr,
			LocalPort:   c.LocalPort,
			Proto:       c.Proto,
			ConnectedAt: c.ConnectedAt.Format("15:04:05"),
			Duration:    durStr,
			BytesIn:     c.BytesIn,
			BytesOut:    c.BytesOut,
		}
	}
	json.NewEncoder(w).Encode(result)
}

func openBrowser(url string) {
	time.Sleep(500 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Run()
}

var helpText = `p2p-tun v2.1 - NAT 穿透工具
将内网服务暴露到公网

用法:
  p2p-tun.exe [选项]

选项:
  -local string      本地服务端口，多个用逗号分隔 (默认 "8080")
  -port string       期望的公网端口，多个用逗号分隔，0=自动匹配local (默认 "0")
  -upnp              启用 UPnP 端口映射 (默认关闭)
  -stun string       STUN 服务器地址，留空则不使用 STUN (如 stun.l.google.com:19302)
  -relay string      中继服务器地址 ip:port
  -proto string      协议 tcp/udp/both (默认 "tcp")
  -auth-key string   中继认证密钥，需与服务端一致
  -compress          启用数据压缩 (lz4)
  -ip-allow string   IP 白名单 (CIDR, 逗号分隔)
  -ip-deny string    IP 黑名单 (CIDR, 逗号分隔)
  -max-conns int     最大并发连接数，0=不限
  -rate-limit int    带宽限制 (字节/秒)，0=不限
  -verbose           输出详细调试日志
  -gui               启动 GUI 界面
  -gui-token string  GUI 认证 token，留空则自动生成
  -help              显示此帮助

穿透流程:
  默认: 直接使用中继服务器
  -upnp 开启: UPnP → 中继
  -stun 设置: STUN → 中继
  两者都开启: UPnP → STUN → 中继

示例:
  # 基本用法（直接中继）
  p2p-tun.exe -local 8080 -relay myvps.com:9000

  # 启用 UPnP
  p2p-tun.exe -local 8080 -relay myvps.com:9000 -upnp

  # 启用 STUN
  p2p-tun.exe -local 8080 -relay myvps.com:9000 -stun stun.l.google.com:19302

  # 多端口
  p2p-tun.exe -local 8080,22,3306 -port 8080,22,3306 -relay myvps.com:9000

  # RDP 双协议
  p2p-tun.exe -local 3389 -port 3389 -relay myvps.com:9000 -proto both

  # 带认证
  p2p-tun.exe -local 8080 -relay myvps.com:9000 -auth-key mysecret123

  # GUI 模式
  p2p-tun.exe -gui
`

var guiHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>p2p-tun v2.1</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Fira+Code:wght@400;500;600;700&family=Fira+Sans:wght@300;400;500;600;700&display=swap" rel="stylesheet">
<script src="https://cdn.tailwindcss.com"></script>
<script>
tailwind.config = {
  theme: {
    extend: {
      colors: {
        bg: '#020617',
        panel: '#0F172A',
        card: '#1E293B',
        border: '#334155',
        accent: '#22C55E',
        'accent-dim': '#16A34A',
        danger: '#EF4444',
        warn: '#F59E0B',
        txt: '#F8FAFC',
        muted: '#94A3B8',
        dim: '#64748B',
      },
      fontFamily: {
        sans: ['Fira Sans', 'sans-serif'],
        mono: ['Fira Code', 'monospace'],
      }
    }
  }
}
</script>
<style>
* { box-sizing: border-box; }
body { font-family: 'Fira Sans', sans-serif; }
@keyframes pulse-glow {
  0%, 100% { box-shadow: 0 0 4px rgba(34,197,94,0.4); }
  50% { box-shadow: 0 0 12px rgba(34,197,94,0.7); }
}
@keyframes fade-in { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: translateY(0); } }
.dot-on { width:8px; height:8px; border-radius:50%; background:#22C55E; animation: pulse-glow 2s ease-in-out infinite; }
.dot-off { width:8px; height:8px; border-radius:50%; background:#EF4444; }
.fade-in { animation: fade-in 0.3s ease-out; }
.log-box::-webkit-scrollbar { width:5px; }
.log-box::-webkit-scrollbar-track { background:#020617; }
.log-box::-webkit-scrollbar-thumb { background:#334155; border-radius:3px; }
.log-box::-webkit-scrollbar-thumb:hover { background:#475569; }
.conn-table::-webkit-scrollbar { width:5px; }
.conn-table::-webkit-scrollbar-track { background:#020617; }
.conn-table::-webkit-scrollbar-thumb { background:#334155; border-radius:3px; }
@media (prefers-reduced-motion: reduce) { .dot-on { animation: none; } .fade-in { animation: none; } }
</style>
</head>
<body class="bg-bg text-txt min-h-screen">

<div id="loginScreen" class="min-h-screen flex items-center justify-center">
  <div class="bg-panel border border-border rounded-2xl p-8 w-96 fade-in">
    <div class="flex items-center gap-3 mb-6">
      <svg class="w-8 h-8 text-accent" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M2 12h4m12 0h4M12 2v4m0 12v4"/></svg>
      <span class="text-2xl font-bold text-txt">p2p-tun</span>
    </div>
    <p class="text-muted text-sm mb-5">输入控制台显示的 Token 登录</p>
    <input id="tokenInput" type="password" placeholder="Token"
      class="w-full p-3 bg-bg border border-border rounded-lg text-txt font-mono text-sm
             focus:outline-none focus:border-accent transition-colors duration-200 mb-3">
    <p id="loginError" class="text-danger text-xs mb-3 hidden">Token 无效</p>
    <button onclick="doLogin()"
      class="w-full p-3 bg-accent text-bg font-bold rounded-lg hover:bg-accent-dim
             transition-colors duration-200 cursor-pointer">登录</button>
  </div>
</div>

<div id="dashboard" class="hidden">

<header class="bg-panel border-b border-border sticky top-0 z-50">
  <div class="max-w-7xl mx-auto px-4 sm:px-6 h-14 flex items-center justify-between">
    <div class="flex items-center gap-3">
      <svg class="w-5 h-5 text-accent" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="10"/><path d="M2 12h4m12 0h4M12 2v4m0 12v4"/></svg>
      <span class="text-base font-bold text-txt">p2p-tun</span>
      <span class="text-dim text-xs font-mono">v2.1</span>
    </div>
    <div class="flex items-center gap-4">
      <div class="flex items-center gap-2">
        <span id="headerDot" class="dot-off"></span>
        <span id="headerStatus" class="text-sm text-muted">未启动</span>
      </div>
      <div id="headerAddr" class="text-xs font-mono text-dim hidden sm:block">-</div>
    </div>
  </div>
</header>

<main class="max-w-7xl mx-auto px-4 sm:px-6 py-5 space-y-4">

  <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-3">
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">状态</div>
      <div id="cardStatus" class="mt-1.5 text-lg font-bold font-mono text-dim">未启动</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">模式</div>
      <div id="cardMode" class="mt-1.5 text-lg font-bold font-mono text-txt">-</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">NAT</div>
      <div id="cardNAT" class="mt-1.5 text-lg font-bold font-mono text-txt">-</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">运行时长</div>
      <div id="cardUptime" class="mt-1.5 text-lg font-bold font-mono text-txt">0s</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">本地端口</div>
      <div id="cardLocalPorts" class="mt-1.5 text-lg font-bold font-mono text-txt">-</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">流入</div>
      <div id="cardBytesIn" class="mt-1.5 text-lg font-bold font-mono text-accent">0 B</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">流出</div>
      <div id="cardBytesOut" class="mt-1.5 text-lg font-bold font-mono text-danger">0 B</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">活跃通道</div>
      <div id="cardChannels" class="mt-1.5 text-lg font-bold font-mono text-txt">0</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">累计连接</div>
      <div id="cardTotalConns" class="mt-1.5 text-lg font-bold font-mono text-txt">0</div>
    </div>
    <div class="bg-panel border border-border rounded-xl p-4">
      <div class="text-[10px] text-dim uppercase tracking-widest">公网地址</div>
      <div id="cardPublicAddr" class="mt-1.5 text-sm font-bold font-mono text-txt truncate">-</div>
    </div>
  </div>

  <div class="grid grid-cols-1 lg:grid-cols-4 gap-4">
    <div class="bg-panel border border-border rounded-xl p-5">
      <h2 class="text-[10px] text-dim uppercase tracking-widest font-semibold mb-4">配置</h2>
      <div class="space-y-3">
        <div>
          <label class="block text-[10px] text-dim mb-1">本地端口</label>
          <input type="text" id="localPorts" value="8080" placeholder="8080,22,3306"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">公网端口 (0=自动)</label>
          <input type="text" id="remotePorts" value="0" placeholder="8080,22,3306"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">中继服务器</label>
          <input type="text" id="relayAddr" placeholder="myvps.com:9000"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div class="flex items-center justify-between bg-bg border border-border rounded-lg px-3 py-2">
            <div>
              <div class="text-[10px] text-dim">UPnP</div>
              <div class="text-[9px] text-dim">端口映射</div>
            </div>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" id="upnpToggle" class="sr-only peer">
              <div class="w-9 h-5 bg-border rounded-full peer peer-checked:bg-accent
                          after:content-[''] after:absolute after:top-[2px] after:left-[2px]
                          after:bg-white after:rounded-full after:h-4 after:w-4
                          after:transition-all peer-checked:after:translate-x-full"></div>
            </label>
          </div>
          <div class="flex items-center justify-between bg-bg border border-border rounded-lg px-3 py-2">
            <div>
              <div class="text-[10px] text-dim">压缩</div>
              <div class="text-[9px] text-dim">lz4</div>
            </div>
            <label class="relative inline-flex items-center cursor-pointer">
              <input type="checkbox" id="compressToggle" class="sr-only peer">
              <div class="w-9 h-5 bg-border rounded-full peer peer-checked:bg-accent
                          after:content-[''] after:absolute after:top-[2px] after:left-[2px]
                          after:bg-white after:rounded-full after:h-4 after:w-4
                          after:transition-all peer-checked:after:translate-x-full"></div>
            </label>
          </div>
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">STUN 服务器 (留空=不使用)</label>
          <input type="text" id="stunServer" placeholder="stun.l.google.com:19302"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">协议</label>
          <select id="proto"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
            <option value="both">TCP + UDP</option>
          </select>
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">IP 白名单</label>
          <input type="text" id="ipAllow" placeholder="1.2.3.0/24"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">IP 黑名单</label>
          <input type="text" id="ipDeny" placeholder="10.0.0.0/8"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">最大连接数</label>
          <input type="number" id="maxConns" placeholder="0" min="0"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
        <div>
          <label class="block text-[10px] text-dim mb-1">带宽限制 B/s</label>
          <input type="number" id="rateLimit" placeholder="0" min="0"
            class="w-full bg-bg border border-border rounded-lg px-3 py-2 text-xs font-mono text-txt
                   focus:outline-none focus:border-accent transition-colors duration-200">
        </div>
      </div>
      <div class="flex gap-3 mt-5">
        <button id="btnStart" onclick="startTunnel()"
          class="flex-1 bg-accent text-bg font-bold py-2.5 rounded-lg text-sm
                 hover:bg-accent-dim transition-colors duration-200 cursor-pointer
                 disabled:opacity-30 disabled:cursor-not-allowed">启动</button>
        <button id="btnStop" onclick="stopTunnel()" disabled
          class="flex-1 bg-danger text-txt font-bold py-2.5 rounded-lg text-sm
                 hover:brightness-110 transition-colors duration-200 cursor-pointer
                 disabled:opacity-30 disabled:cursor-not-allowed">停止</button>
      </div>
    </div>

    <div class="lg:col-span-3 bg-panel border border-border rounded-xl p-5 flex flex-col">
      <h2 class="text-[10px] text-dim uppercase tracking-widest font-semibold mb-3">实时流量</h2>
      <div class="flex-1 min-h-0 relative" style="min-height:200px;">
        <canvas id="trafficCanvas" class="w-full h-full absolute inset-0 rounded-lg" style="background:#020617;"></canvas>
      </div>
    </div>
  </div>

  <div class="grid grid-cols-1 lg:grid-cols-4 gap-4">
    <div class="bg-panel border border-border rounded-xl p-5">
      <h2 class="text-[10px] text-dim uppercase tracking-widest font-semibold mb-4">端口映射</h2>
      <div id="portMapList" class="space-y-2 font-mono text-xs">
        <div class="text-dim">暂无映射</div>
      </div>
    </div>

    <div class="lg:col-span-3 bg-panel border border-border rounded-xl p-5">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-[10px] text-dim uppercase tracking-widest font-semibold">实时连接</h2>
        <div class="flex items-center gap-2">
          <span id="connCount" class="text-xs font-mono text-accent">0</span>
          <span class="text-[10px] text-dim">个活跃</span>
        </div>
      </div>
      <div class="conn-table overflow-y-auto font-mono text-xs" style="max-height:260px;">
        <table class="w-full text-left">
          <thead class="sticky top-0 bg-panel z-10">
            <tr class="text-dim border-b border-border">
              <th class="pb-2 pr-3 font-medium">ID</th>
              <th class="pb-2 pr-3 font-medium">来源</th>
              <th class="pb-2 pr-3 font-medium">端口</th>
              <th class="pb-2 pr-3 font-medium">协议</th>
              <th class="pb-2 pr-3 font-medium">时间</th>
              <th class="pb-2 pr-3 font-medium">时长</th>
              <th class="pb-2 pr-3 font-medium">↓流入</th>
              <th class="pb-2 font-medium">↑流出</th>
            </tr>
          </thead>
          <tbody id="connTableBody">
            <tr><td colspan="8" class="py-6 text-center text-dim">暂无连接</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <div class="bg-panel border border-border rounded-xl p-5">
    <h2 class="text-[10px] text-dim uppercase tracking-widest font-semibold mb-3">日志</h2>
    <div id="logsWindow" class="log-box bg-bg rounded-lg border border-border p-3 overflow-y-auto font-mono text-xs leading-relaxed" style="height:240px;">
    </div>
  </div>

</main>

</div>

<script>
var AUTH_TOKEN = localStorage.getItem('gui_token') || '';

function checkAuth() {
  if (!AUTH_TOKEN) {
    showLoginForm();
  } else {
    fetch('/api/status', {headers:{'Authorization':'Bearer '+AUTH_TOKEN}})
      .then(function(r) {
        if (r.status === 401) { showLoginForm(); }
        else { showDashboard(); startPolling(); }
      })
      .catch(function() { showLoginForm(); });
  }
}

function showLoginForm() {
  document.getElementById('loginScreen').classList.remove('hidden');
  document.getElementById('dashboard').classList.add('hidden');
  localStorage.removeItem('gui_token');
  AUTH_TOKEN = '';
}

function showDashboard() {
  document.getElementById('loginScreen').classList.add('hidden');
  document.getElementById('dashboard').classList.remove('hidden');
}

function doLogin() {
  var token = document.getElementById('tokenInput').value;
  fetch('/api/login', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({token: token})
  }).then(function(r) { return r.json(); }).then(function(d) {
    if (d.status === 'ok') {
      AUTH_TOKEN = d.token;
      localStorage.setItem('gui_token', AUTH_TOKEN);
      showDashboard();
      startPolling();
    } else {
      document.getElementById('loginError').classList.remove('hidden');
    }
  }).catch(function() {
    document.getElementById('loginError').classList.remove('hidden');
  });
}

function authFetch(url, opts) {
  opts = opts || {};
  opts.headers = opts.headers || {};
  opts.headers['Authorization'] = 'Bearer ' + AUTH_TOKEN;
  return fetch(url, opts);
}

function formatBytes(b) {
  if (b < 1024) return b + ' B';
  if (b < 1048576) return (b/1024).toFixed(1) + ' KB';
  return (b/1048576).toFixed(1) + ' MB';
}
function formatUptime(s) {
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s/60) + 'm ' + (s%60) + 's';
  return Math.floor(s/3600) + 'h ' + Math.floor((s%3600)/60) + 'm';
}
function escapeHtml(t) {
  var d = document.createElement('div');
  d.textContent = t;
  return d.innerHTML;
}

var pollStarted = false;
function startPolling() {
  if (pollStarted) return;
  pollStarted = true;
  updateStatus();
  updateLogs();
  updateTraffic();
  updateConnections();
  setInterval(updateStatus, 2000);
  setInterval(updateLogs, 2000);
  setInterval(updateTraffic, 1000);
  setInterval(updateConnections, 1000);
}

function updateStatus() {
  authFetch('/api/status').then(function(r) {
    if (r.status === 401) { showLoginForm(); return null; }
    return r.json();
  }).then(function(d) {
    if (!d) return;
    var headerDot = document.getElementById('headerDot');
    var headerStatus = document.getElementById('headerStatus');
    var cardStatus = document.getElementById('cardStatus');
    if (d.running) {
      headerDot.className = 'dot-on';
      headerStatus.textContent = '运行中';
      headerStatus.className = 'text-sm text-accent';
      cardStatus.textContent = '运行中';
      cardStatus.className = 'mt-1.5 text-lg font-bold font-mono text-accent';
    } else {
      headerDot.className = 'dot-off';
      headerStatus.textContent = '未启动';
      headerStatus.className = 'text-sm text-muted';
      cardStatus.textContent = '未启动';
      cardStatus.className = 'mt-1.5 text-lg font-bold font-mono text-dim';
    }
    document.getElementById('cardMode').textContent = d.mode || '-';
    document.getElementById('cardNAT').textContent = d.nat_type || '-';
    document.getElementById('cardUptime').textContent = d.uptime_seconds ? formatUptime(d.uptime_seconds) : '0s';
    document.getElementById('cardBytesIn').textContent = formatBytes(d.bytes_in);
    document.getElementById('cardBytesOut').textContent = formatBytes(d.bytes_out);
    document.getElementById('cardChannels').textContent = d.active_channels;
    document.getElementById('cardTotalConns').textContent = d.total_conns || 0;
    document.getElementById('cardLocalPorts').textContent = d.local_ports || '-';
    document.getElementById('cardPublicAddr').textContent = d.public_addr || '-';
    document.getElementById('headerAddr').textContent = d.public_addr || '-';
    document.getElementById('btnStart').disabled = d.running;
    document.getElementById('btnStop').disabled = !d.running;

    if (d.services && d.services.length > 0) {
      var html = '';
      for (var i = 0; i < d.services.length; i++) {
        var s = d.services[i];
        html += '<div class="bg-bg rounded-lg px-3 py-2 border border-border">';
        html += '<span class="text-accent">VPS:' + s.remote_port + '</span>';
        html += ' <span class="text-dim">&rarr;</span> ';
        html += '<span class="text-txt">127.0.0.1:' + s.local_port + '</span>';
        html += ' <span class="text-dim uppercase">' + s.proto + '</span>';
        html += '</div>';
      }
      document.getElementById('portMapList').innerHTML = html;
    } else {
      document.getElementById('portMapList').innerHTML = '<div class="text-dim">暂无映射</div>';
    }
  }).catch(function(){});
}

function updateLogs() {
  authFetch('/api/logs').then(function(r) {
    if (r.status === 401) return null;
    return r.json();
  }).then(function(logs) {
    if (!logs) return;
    var w = document.getElementById('logsWindow');
    var html = '';
    for (var i = 0; i < logs.length; i++) {
      var l = logs[i];
      var colorClass = 'text-dim';
      if (l.level === 'INFO') colorClass = 'text-txt';
      else if (l.level === 'WARN') colorClass = 'text-warn';
      else if (l.level === 'ERROR') colorClass = 'text-danger';
      else if (l.level === 'DEBUG') colorClass = 'text-dim';
      html += '<div class="' + colorClass + '"><span class="text-dim">[' + l.module + ']</span> ' + l.level + ' ' + escapeHtml(l.message) + '</div>';
    }
    w.innerHTML = html;
    w.scrollTop = w.scrollHeight;
  }).catch(function(){});
}

var trafficData = [];
function updateTraffic() {
  authFetch('/api/traffic').then(function(r) {
    if (r.status === 401) return null;
    return r.json();
  }).then(function(d) {
    if (!d) return;
    trafficData = d.points || [];
    drawTrafficChart();
  }).catch(function(){});
}

function drawTrafficChart() {
  var canvas = document.getElementById('trafficCanvas');
  if (!canvas) return;
  var dpr = window.devicePixelRatio || 1;
  var rect = canvas.getBoundingClientRect();
  canvas.width = rect.width * dpr;
  canvas.height = rect.height * dpr;
  var ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);
  var w = rect.width, h = rect.height;
  var pad = {top: 20, right: 20, bottom: 25, left: 60};
  var cw = w - pad.left - pad.right, ch = h - pad.top - pad.bottom;
  ctx.fillStyle='#020617';ctx.fillRect(0,0,w,h);
  if(trafficData.length<2){ctx.fillStyle='#475569';ctx.font='13px Fira Sans';ctx.textAlign='center';ctx.fillText('等待数据...',w/2,h/2);return;}
  var maxVal = 1024;
  trafficData.forEach(function(p) {
    maxVal = Math.max(maxVal, p.bytes_in_rate, p.bytes_out_rate);
  });
  maxVal = Math.ceil(maxVal / 1024) * 1024;
  if (maxVal < 1024) maxVal = 1024;
  ctx.strokeStyle='#1E293B';ctx.lineWidth=1;
  var gl=5;for(var i=0;i<=gl;i++){var y=pad.top+(ch/gl)*i;ctx.beginPath();ctx.moveTo(pad.left,y);ctx.lineTo(w-pad.right,y);ctx.stroke();
    ctx.fillStyle='#64748B';ctx.font='10px Fira Code';ctx.textAlign='right';ctx.fillText(formatBytes(maxVal-(maxVal/gl)*i)+'/s',pad.left-6,y+3);}
  var first=trafficData[0],last=trafficData[trafficData.length-1],dur=last.time-first.time||60;
  ctx.fillStyle='#64748B';ctx.font='10px Fira Code';ctx.textAlign='center';ctx.fillText('-'+dur+'s',w/2,h-4);ctx.fillText('0s',w-pad.right,h-4);
  ctx.strokeStyle='#1E293B';ctx.beginPath();ctx.moveTo(pad.left,pad.top+ch);ctx.lineTo(w-pad.right,pad.top+ch);ctx.stroke();
  function drawLine(data, color, fillAlpha) {
    if (data.length < 2) return;
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.beginPath();
    var stepX = cw / (data.length - 1);
    data.forEach(function(v, i) {
      var x = pad.left + stepX * i;
      var y = pad.top + ch - (v / maxVal) * ch;
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.stroke();
    var gradient = ctx.createLinearGradient(0, pad.top, 0, pad.top + ch);
    gradient.addColorStop(0, fillAlpha);
    gradient.addColorStop(1, 'rgba(0,0,0,0)');
    ctx.fillStyle = gradient;
    ctx.lineTo(pad.left + stepX * (data.length - 1), pad.top + ch);
    ctx.lineTo(pad.left, pad.top + ch);
    ctx.closePath();
    ctx.fill();
  }
  drawLine(trafficData.map(function(p){return p.bytes_in_rate}),'#22C55E','rgba(34,197,94,0.25)');
  drawLine(trafficData.map(function(p){return p.bytes_out_rate}),'#EF4444','rgba(239,68,68,0.25)');
  ctx.font='11px Fira Code';var ly=pad.top+14;
  ctx.fillStyle='#22C55E';ctx.textAlign='left';ctx.fillText('\u2014 流入 ('+formatBytes(trafficData[trafficData.length-1].bytes_in_rate)+'/s)',pad.left,ly);
  ctx.fillStyle='#EF4444';ctx.fillText('\u2014 流出 ('+formatBytes(trafficData[trafficData.length-1].bytes_out_rate)+'/s)',pad.left+180,ly);
}

function updateConnections() {
  authFetch('/api/connections').then(function(r) {
    if (r.status === 401) return null;
    return r.json();
  }).then(function(conns) {
    if (!conns) return;
    document.getElementById('connCount').textContent = conns.length;
    var tbody = document.getElementById('connTableBody');
    if (conns.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8" class="py-4 text-center text-gray-600">暂无连接</td></tr>';
      return;
    }
    var html = '';
    for (var i = 0; i < conns.length; i++) {
      var c = conns[i];
      html+='<tr class="border-b border-border/50 hover:bg-card/30 transition-colors duration-150">';
       html+='<td class="py-2 pr-3 text-accent">'+c.channel_id+'</td>';
       html+='<td class="py-2 pr-3 text-txt">'+escapeHtml(c.remote_addr||'-')+'</td>';
       html+='<td class="py-2 pr-3 text-txt">'+c.local_port+'</td>';
       html+='<td class="py-2 pr-3 text-dim uppercase">'+c.proto+'</td>';
       html+='<td class="py-2 pr-3 text-dim">'+c.connected_at+'</td>';
       html+='<td class="py-2 pr-3 text-dim">'+c.duration+'</td>';
       html+='<td class="py-2 pr-3 text-accent">'+formatBytes(c.bytes_in)+'</td>';
       html+='<td class="py-2 text-danger">'+formatBytes(c.bytes_out)+'</td>';
      html += '</tr>';
    }
    tbody.innerHTML = html;
  }).catch(function(){});
}

function startTunnel() {
  var params = {
    local_ports: document.getElementById('localPorts').value || '8080',
    remote_ports: document.getElementById('remotePorts').value || '0',
    upnp: document.getElementById('upnpToggle').checked,
    stun_server: document.getElementById('stunServer').value,
    relay_addr: document.getElementById('relayAddr').value,
    proto: document.getElementById('proto').value,
    compress: document.getElementById('compressToggle').checked,
    ip_allow: document.getElementById('ipAllow').value,
    ip_deny: document.getElementById('ipDeny').value,
    max_conns: parseInt(document.getElementById('maxConns').value) || 0,
    rate_limit: parseInt(document.getElementById('rateLimit').value) || 0
  };
  authFetch('/api/start', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(params)}).then(function(r) {
    if (r.status === 401) { showLoginForm(); return; }
    return r.json();
  }).then(function() { updateStatus(); }).catch(function(){});
}

function stopTunnel() {
  authFetch('/api/stop', {method:'POST'}).then(function(r) {
    if (r.status === 401) { showLoginForm(); return; }
    return r.json();
  }).then(function() { updateStatus(); }).catch(function(){});
}

checkAuth();
</script>
</body>
</html>
`
