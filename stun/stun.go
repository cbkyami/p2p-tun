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
	family := attrValue[0]
	xorPort := binary.BigEndian.Uint16(attrValue[2:4])
	port := xorPort ^ uint16(stunMagicCookie>>16)

	var ip net.IP
	if family == familyIPv4 {
		if len(attrValue) < 8 {
			return nil, ErrInvalidResponse
		}
		xorIP := binary.BigEndian.Uint32(attrValue[4:8])
		ip = net.IPv4(
			byte(xorIP^stunMagicCookie>>24),
			byte(xorIP^stunMagicCookie>>16),
			byte(xorIP^stunMagicCookie>>8),
			byte(xorIP^stunMagicCookie),
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
	family := attrValue[0]
	port := binary.BigEndian.Uint16(attrValue[2:4])

	var ip net.IP
	if family == familyIPv4 {
		if len(attrValue) < 8 {
			return nil, ErrInvalidResponse
		}
		ip = net.IPv4(attrValue[4], attrValue[5], attrValue[6], attrValue[7])
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
	if _, err := conn.WriteToUDP(req, udpAddr); err != nil {
		return nil, fmt.Errorf("write to stun server: %w", err)
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

func DetectNATType(stunServer string) (string, *Addr, *net.UDPConn, error) {
	_, err := net.ResolveUDPAddr("udp", stunServer)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve stun server: %w", err)
	}

	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return "", nil, nil, fmt.Errorf("listen udp: %w", err)
	}

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	logutil.Debug("stun", "第一次绑定请求到 %s", stunServer)
	addr1, err := BindingRequest(conn, stunServer)
	if err != nil {
		conn.Close()
		return "", nil, nil, fmt.Errorf("first binding request: %w", err)
	}

	if addr1.IP.Equal(localAddr.IP) && addr1.Port == localAddr.Port {
		logutil.Info("stun", "NAT 类型判定: no-nat (公网地址: %s)", addr1)
		return "no-nat", addr1, conn, nil
	}

	host, _, err := net.SplitHostPort(stunServer)
	if err != nil {
		conn.Close()
		return "", nil, nil, fmt.Errorf("split host port: %w", err)
	}

	altStunServer := fmt.Sprintf("%s:3479", host)
	logutil.Debug("stun", "第二次绑定请求到 %s", altStunServer)
	addr2, err := BindingRequest(conn, altStunServer)
	if err != nil {
		logutil.Debug("stun", "第二次绑定请求失败: %v", err)
		if isLocalPortChanged(addr1, localAddr) {
			logutil.Info("stun", "NAT 类型判定: symmetric (公网地址: %s)", addr1)
			return "symmetric", addr1, conn, nil
		}
		logutil.Info("stun", "NAT 类型判定: port-restricted (公网地址: %s)", addr1)
		return "port-restricted", addr1, conn, nil
	}

	if addr1.Port != addr2.Port {
		logutil.Debug("stun", "两次映射端口不同: %d vs %d", addr1.Port, addr2.Port)
		logutil.Info("stun", "NAT 类型判定: symmetric (公网地址: %s)", addr1)
		return "symmetric", addr1, conn, nil
	}

	logutil.Debug("stun", "两次映射端口相同: %d", addr1.Port)
	logutil.Info("stun", "NAT 类型判定: full-cone (公网地址: %s)", addr1)
	return "full-cone", addr1, conn, nil
}

func isLocalPortChanged(mapped *Addr, local *net.UDPAddr) bool {
	return !mapped.IP.Equal(local.IP) || mapped.Port != local.Port
}
