package upnp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"p2p_tun/logutil"
)

type Gateway struct {
	controlURL string
	serviceURL string
	client     *http.Client
}

type soapEnvelope struct {
	XMLName       xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	EncodingStyle string   `xml:"http://schemas.xmlsoap.org/soap/envelope/ encodingStyle,attr"`
	Body          soapBody `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

type soapBody struct {
	Content string `xml:",innerxml"`
}

func DiscoverGateway() (*Gateway, error) {
	gw, err := discoverUPnPIGD()
	if err == nil {
		return gw, nil
	}

	gw, err = discoverNATPMP()
	if err == nil {
		return gw, nil
	}

	return nil, fmt.Errorf("UPnP IGD and NAT-PMP discovery failed")
}

func discoverUPnPIGD() (*Gateway, error) {
	logutil.Debug("upnp", "SSDP 搜索开始")

	ssdpAddr, err := net.ResolveUDPAddr("udp", "239.255.255.250:1900")
	if err != nil {
		return nil, err
	}

	conn, err := net.DialUDP("udp", nil, ssdpAddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	searchMsg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 3\r\n\r\n"

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}

	_, err = conn.Write([]byte(searchMsg))
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("SSDP discovery: %w", err)
	}

	response := string(buf[:n])
	location := ""
	for _, line := range strings.Split(response, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "location:") {
			location = strings.TrimSpace(line[len("location:"):])
			break
		}
	}
	if location == "" {
		return nil, fmt.Errorf("no LOCATION in SSDP response")
	}

	logutil.Debug("upnp", "收到 SSDP 响应, Location: %s", location)

	controlURL, err := getWANIPConnectionURL(location)
	if err != nil {
		logutil.Debug("upnp", "获取设备描述 XML 失败: %v", err)
		return nil, err
	}

	logutil.Debug("upnp", "找到 WANIPConnection controlURL: %s", controlURL)

	return &Gateway{
		controlURL: controlURL,
		serviceURL: location,
		client:     &http.Client{Timeout: 5 * time.Second},
	}, nil
}

type deviceDesc struct {
	XMLName xml.Name `xml:"root"`
	Device  struct {
		DeviceType string `xml:"deviceType"`
		URLBase    string `xml:"URLBase"`
		DeviceList struct {
			Device []struct {
				DeviceType string `xml:"deviceType"`
				DeviceList struct {
					Device []struct {
						DeviceType  string `xml:"deviceType"`
						ServiceList struct {
							Service []struct {
								ServiceType string `xml:"serviceType"`
								ControlURL  string `xml:"controlURL"`
							} `xml:"service"`
						} `xml:"serviceList"`
					} `xml:"device"`
				} `xml:"deviceList"`
			} `xml:"device"`
		} `xml:"deviceList"`
	} `xml:"device"`
}

func getWANIPConnectionURL(location string) (string, error) {
	resp, err := http.Get(location)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	logutil.Debug("upnp", "获取设备描述 XML 成功, 长度: %d", len(body))

	var desc deviceDesc
	if err := xml.Unmarshal(body, &desc); err != nil {
		return "", fmt.Errorf("parse device description: %w", err)
	}

	baseURL := desc.Device.URLBase
	if baseURL == "" {
		baseURL = location
	}

	for _, dev1 := range desc.Device.DeviceList.Device {
		for _, dev2 := range dev1.DeviceList.Device {
			for _, svc := range dev2.ServiceList.Service {
				if strings.Contains(svc.ServiceType, "WANIPConnection") ||
					strings.Contains(svc.ServiceType, "WANPPPConnection") {
					controlPath := svc.ControlURL
					if strings.HasPrefix(controlPath, "http") {
						return controlPath, nil
					}
					return baseURL + controlPath, nil
				}
			}
		}
	}

	return "", fmt.Errorf("WANIPConnection service not found")
}

func (g *Gateway) soapCall(action string, bodyXML string) ([]byte, error) {
	soapBody := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>%s</s:Body>
</s:Envelope>`, bodyXML)

	req, err := http.NewRequest("POST", g.controlURL, bytes.NewReader([]byte(soapBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"urn:schemas-upnp-org:service:WANIPConnection:1#`+action+`"`)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func (g *Gateway) AddPortMapping(externalPort int, internalPort int, proto string, description string) error {
	localIP, err := getLocalIP()
	if err != nil {
		return err
	}

	logutil.Info("upnp", "AddPortMapping 请求: 外部端口=%d, 内部端口=%d, 协议=%s", externalPort, internalPort, proto)

	bodyXML := fmt.Sprintf(`<u:AddPortMapping xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>%s</NewProtocol>
<NewInternalPort>%d</NewInternalPort>
<NewInternalClient>%s</NewInternalClient>
<NewEnabled>1</NewEnabled>
<NewPortMappingDescription>%s</NewPortMappingDescription>
<NewLeaseDuration>0</NewLeaseDuration>
</u:AddPortMapping>`, externalPort, proto, internalPort, localIP, description)

	respBody, err := g.soapCall("AddPortMapping", bodyXML)
	if err != nil {
		logutil.Info("upnp", "AddPortMapping 失败: %v", err)
		return err
	}

	if bytes.Contains(respBody, []byte("errorCode")) {
		logutil.Info("upnp", "AddPortMapping 失败: %s", string(respBody))
		return fmt.Errorf("UPnP AddPortMapping failed: %s", string(respBody))
	}

	logutil.Info("upnp", "AddPortMapping 成功: %d -> %d (%s)", externalPort, internalPort, proto)
	return nil
}

func (g *Gateway) GetExternalIP() (net.IP, error) {
	bodyXML := `<u:GetExternalIPAddress xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
</u:GetExternalIPAddress>`

	respBody, err := g.soapCall("GetExternalIPAddress", bodyXML)
	if err != nil {
		logutil.Info("upnp", "GetExternalIP 失败: %v", err)
		return nil, err
	}

	respStr := string(respBody)
	start := strings.Index(respStr, "<NewExternalIPAddress>")
	end := strings.Index(respStr, "</NewExternalIPAddress>")
	if start == -1 || end == -1 {
		logutil.Info("upnp", "GetExternalIP 失败: 响应中无公网 IP")
		return nil, fmt.Errorf("no external IP in UPnP response")
	}

	ipStr := strings.TrimSpace(respStr[start+len("<NewExternalIPAddress>") : end])
	ip := net.ParseIP(ipStr)
	if ip == nil {
		logutil.Info("upnp", "GetExternalIP 失败: 无效 IP %s", ipStr)
		return nil, fmt.Errorf("invalid external IP: %s", ipStr)
	}

	logutil.Info("upnp", "GetExternalIP 成功: %s", ip)
	return ip, nil
}

func (g *Gateway) DeletePortMapping(externalPort int, proto string) error {
	logutil.Info("upnp", "DeletePortMapping 清理: 端口=%d, 协议=%s", externalPort, proto)

	bodyXML := fmt.Sprintf(`<u:DeletePortMapping xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>%s</NewProtocol>
</u:DeletePortMapping>`, externalPort, proto)

	_, err := g.soapCall("DeletePortMapping", bodyXML)
	return err
}

type natPMPGateway struct {
	gatewayIP net.IP
}

func discoverNATPMP() (*Gateway, error) {
	gwIP, err := getDefaultGateway()
	if err != nil {
		return nil, err
	}

	logutil.Debug("upnp", "NAT-PMP 尝试, 网关 IP: %s", gwIP)

	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: gwIP, Port: 5351})
	if err != nil {
		logutil.Debug("upnp", "NAT-PMP 连接失败: %v", err)
		return nil, err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, err
	}

	msg := []byte{0, 0}
	_, err = conn.Write(msg)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		logutil.Debug("upnp", "NAT-PMP 响应失败: %v", err)
		return nil, fmt.Errorf("NAT-PMP: %w", err)
	}

	if n < 8 || buf[0] != 0 || buf[1] != 128 {
		logutil.Debug("upnp", "NAT-PMP 响应格式不正确")
		return nil, fmt.Errorf("NAT-PMP: unexpected response")
	}

	logutil.Debug("upnp", "NAT-PMP 成功, 网关: %s", gwIP)
	return &Gateway{
		controlURL: "",
		serviceURL: gwIP.String(),
		client:     &http.Client{Timeout: 5 * time.Second},
	}, nil
}

func getDefaultGateway() (net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				gw := net.IPv4(ip4[0], ip4[1], ip4[2], 1)
				return gw, nil
			}
		}
	}
	return nil, fmt.Errorf("no suitable network interface found")
}

func getLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}
