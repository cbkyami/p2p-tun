package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"p2p_tun/logutil"
)

const (
	stunMagicCookie  = 0x2112A442
	bindingRequest   = 0x0001
	bindingResponse  = 0x0101
	attrXORMapped    = 0x0020
	attrMappedAddr   = 0x0001
	stunHeaderSize   = 20
	stunAttrHeader   = 4
	familyIPv4       = 0x01
	familyIPv6       = 0x02
)

var (
	ErrNoXORMappedAddr = errors.New("stun: no XOR-MAPPED-ADDRESS in response")
	ErrInvalidResponse = errors.New("stun: invalid STUN response")
)

type Addr struct {
	IP   net.IP
	Port int
}

func (a *Addr) String() string {
	return fmt.Sprintf("%s:%d", a.IP, a.Port)
}

type STUNHeader struct {
	MsgType     uint16
	MsgLength   uint16
	MagicCookie uint32
	Transaction [12]byte
}

func buildBindingRequest() ([]byte, [12]byte, error) {
	var txnID [12]byte
	if _, err := rand.Read(txnID[:]); err != nil {
		return nil, txnID, err
	}
	msg := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(msg[0:2], bindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0)
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txnID[:])
	return msg, txnID, nil
}

func parseXORMappedAddr(attrValue []byte, txnID [12]byte) (*Addr, error) {
	if len(attrValue) < 4 {
		return nil, ErrInvalidResponse
	}
	family := attrValue[1]
	xorPort := binary.BigEndian.Uint16(attrValue[2:4])
	port := xorPort ^ uint16(stunMagicCookie>>16)

	var ip net.IP
	if family == familyIPv4 {
		if len(attrValue) < 8 {
			return nil, ErrInvalidResponse
		}
		xorIP := binary.BigEndian.Uint32(attrValue[4:8])
		decodedIP := xorIP ^ stunMagicCookie
		ip = net.IPv4(
			byte(decodedIP>>24),
			byte(decodedIP>>16),
			byte(decodedIP>>8),
			byte(decodedIP),
		)
	} else if family == familyIPv6 {
		if len(attrValue) < 20 {
			return nil, ErrInvalidResponse
		}
		ip = make(net.IP, 16)
		xorKey := make([]byte, 16)
		binary.BigEndian.PutUint32(xorKey[0:4], stunMagicCookie)
		copy(xorKey[4:16], txnID[:])
		for i := 0; i < 16; i++ {
			ip[i] = attrValue[4+i] ^ xorKey[i]
		}
	} else {
		return nil, ErrInvalidResponse
	}
	return &Addr{IP: ip, Port: int(port)}, nil
}

func parseMappedAddr(attrValue []byte) (*Addr, error) {
	if len(attrValue) < 4 {
		return nil, ErrInvalidResponse
	}
	family := attrValue[1]
	port := binary.BigEndian.Uint16(attrValue[2:4])

	var ip net.IP
	if family == familyIPv4 {
		if len(attrValue) < 8 {
			return nil, ErrInvalidResponse
		}
		ip = net.IP(attrValue[4:8])
	} else if family == familyIPv6 {
		if len(attrValue) < 20 {
			return nil, ErrInvalidResponse
		}
		ip = make(net.IP, 16)
		copy(ip, attrValue[4:20])
	} else {
		return nil, ErrInvalidResponse
	}
	return &Addr{IP: ip, Port: int(port)}, nil
}

func parseBindingResponse(data []byte, txnID [12]byte) (*Addr, error) {
	if len(data) < stunHeaderSize {
		return nil, ErrInvalidResponse
	}
	msgType := binary.BigEndian.Uint16(data[0:2])
	if msgType != bindingResponse {
		return nil, fmt.Errorf("%w: unexpected type 0x%04x", ErrInvalidResponse, msgType)
	}
	msgLen := binary.BigEndian.Uint16(data[2:4])
	if uint16(len(data))-stunHeaderSize < msgLen {
		return nil, ErrInvalidResponse
	}
	cookie := binary.BigEndian.Uint32(data[4:8])
	if cookie != stunMagicCookie {
		return nil, ErrInvalidResponse
	}

	var mappedAddr *Addr
	offset := stunHeaderSize
	for offset+stunAttrHeader <= len(data) {
		attrType := binary.BigEndian.Uint16(data[offset : offset+2])
		attrLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if offset+stunAttrHeader+attrLen > len(data) {
			break
		}
		attrValue := data[offset+stunAttrHeader : offset+stunAttrHeader+attrLen]

		if attrType == attrXORMapped {
			addr, err := parseXORMappedAddr(attrValue, txnID)
			if err != nil {
				return nil, err
			}
			mappedAddr = addr
		} else if attrType == attrMappedAddr && mappedAddr == nil {
			addr, err := parseMappedAddr(attrValue)
			if err == nil {
				mappedAddr = addr
			}
		}

		paddedLen := attrLen
		if paddedLen%4 != 0 {
			paddedLen += 4 - (attrLen % 4)
		}
		offset += stunAttrHeader + paddedLen
	}

	if mappedAddr == nil {
		return nil, ErrNoXORMappedAddr
	}
	return mappedAddr, nil
}

func BindingRequest(conn *net.UDPConn, stunServer string) (*Addr, error) {
	req, txnID, err := buildBindingRequest()
	if err != nil {
		return nil, err
	}

	udpAddr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return nil, fmt.Errorf("resolve stun server: %w", err)
	}

	logutil.Debug("stun", "发送 Binding Request 到 %s", stunServer)

	deadline := time.Now().Add(5 * time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	if conn.RemoteAddr() != nil {
		if _, err := conn.Write(req); err != nil {
			return nil, fmt.Errorf("write to stun server: %w", err)
		}
	} else {
		if _, err := conn.WriteToUDP(req, udpAddr); err != nil {
			return nil, fmt.Errorf("write to stun server: %w", err)
		}
	}

	buf := make([]byte, 1500)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil, fmt.Errorf("read from stun server: %w", err)
	}

	addr, err := parseBindingResponse(buf[:n], txnID)
	if err != nil {
		return nil, err
	}

	logutil.Debug("stun", "收到响应, 公网地址: %s", addr)
	return addr, nil
}

func BindingRequestWithNewConn(stunServer string) (*Addr, *net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve stun server: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("dial stun server: %w", err)
	}

	addr, err := BindingRequest(conn, stunServer)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return addr, conn, nil
}

func DetectNATType(stunServer, stunServer2 string) (string, *Addr, *net.UDPConn, error) {
	_, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve stun server: %w", err)
	}

	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return "", nil, nil, fmt.Errorf("listen udp: %w", err)
	}

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	logutil.Debug("stun", "测试1: 向 %s 发送绑定请求", stunServer)
	addr1, err := BindingRequest(conn, stunServer)
	if err != nil {
		conn.Close()
		return "", nil, nil, fmt.Errorf("first binding request: %w", err)
	}

	if addr1.IP.Equal(localAddr.IP) && addr1.Port == localAddr.Port {
		logutil.Info("stun", "NAT 类型判定: no-nat (公网地址: %s)", addr1)
		return "no-nat", addr1, conn, nil
	}

	if stunServer2 == "" {
		logutil.Info("stun", "未提供第二个 STUN 服务器，跳过 NAT 类型精确检测")
		logutil.Info("stun", "NAT 类型判定: unknown (公网地址: %s)", addr1)
		return "unknown", addr1, conn, nil
	}

	logutil.Debug("stun", "测试2: 向 %s 发送绑定请求", stunServer2)
	addr2, err := BindingRequest(conn, stunServer2)
	if err != nil {
		logutil.Debug("stun", "第二次绑定请求失败: %v", err)
		if isLocalPortChanged(addr1, localAddr) {
			logutil.Info("stun", "NAT 类型判定: symmetric (公网地址: %s)", addr1)
			return "symmetric", addr1, conn, nil
		}
		logutil.Info("stun", "NAT 类型判定: port-restricted (公网地址: %s)", addr1)
		return "port-restricted", addr1, conn, nil
	}

	if !addr1.IP.Equal(addr2.IP) {
		logutil.Debug("stun", "两次映射IP不同: %s vs %s", addr1.IP, addr2.IP)
		logutil.Info("stun", "NAT 类型判定: symmetric (公网地址: %s)", addr1)
		return "symmetric", addr1, conn, nil
	}

	if addr1.Port != addr2.Port {
		logutil.Debug("stun", "两次映射端口不同: %d vs %d", addr1.Port, addr2.Port)
		logutil.Info("stun", "NAT 类型判定: symmetric (公网地址: %s)", addr1)
		return "symmetric", addr1, conn, nil
	}

	logutil.Debug("stun", "两次映射相同: %s (非Symmetric NAT)", addr1)

	logutil.Debug("stun", "测试3: 从新端口向 %s 发送绑定请求", stunServer)
	conn2, err := net.ListenUDP("udp", nil)
	if err != nil {
		logutil.Info("stun", "NAT 类型判定: port-restricted (公网地址: %s)", addr1)
		return "port-restricted", addr1, conn, nil
	}
	defer conn2.Close()

	addr3, err := BindingRequest(conn2, stunServer)
	if err != nil {
		logutil.Debug("stun", "第三次绑定请求失败: %v", err)
		logutil.Info("stun", "NAT 类型判定: port-restricted (公网地址: %s)", addr1)
		return "port-restricted", addr1, conn, nil
	}

	logutil.Debug("stun", "新端口映射: %s (原端口: %s)", addr3, addr1)

	if !addr1.IP.Equal(addr3.IP) {
		logutil.Debug("stun", "不同本地端口映射到不同IP，判定为 symmetric")
		logutil.Info("stun", "NAT 类型判定: symmetric (公网地址: %s)", addr1)
		return "symmetric", addr1, conn, nil
	}

	portDiff := addr3.Port - addr1.Port
	if portDiff < 0 {
		portDiff = -portDiff
	}

	if portDiff <= 2 {
		logutil.Debug("stun", "端口增量很小 (%d)，可能为 Port Restricted Cone", portDiff)
		logutil.Info("stun", "NAT 类型判定: port-restricted (公网地址: %s)", addr1)
		logutil.Info("stun", "提示: 如确认为 Full Cone，请使用 -nat-type full-cone 覆盖")
		return "port-restricted", addr1, conn, nil
	}

	logutil.Debug("stun", "端口增量较大 (%d)，保守判定为 port-restricted", portDiff)
	logutil.Info("stun", "NAT 类型判定: port-restricted (公网地址: %s)", addr1)
	logutil.Info("stun", "提示: 如确认为 Full Cone，请使用 -nat-type full-cone 覆盖")
	return "port-restricted", addr1, conn, nil
}

func isLocalPortChanged(mapped *Addr, local *net.UDPAddr) bool {
	return !mapped.IP.Equal(local.IP) || mapped.Port != local.Port
}
