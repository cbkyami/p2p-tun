package forward

import (
	"io"
	"net"
	"sync"
	"time"

	"p2p_tun/logutil"
)

func ForwardTCP(listenAddr string, targetAddr string) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	logutil.Info("forward", "TCP 转发启动: %s -> %s", listenAddr, targetAddr)

	for {
		clientConn, err := ln.Accept()
		if err != nil {
			logutil.Error("forward", "TCP accept 错误: %v", err)
			continue
		}
		go handleTCPConn(clientConn, targetAddr)
	}
}

func handleTCPConn(clientConn net.Conn, targetAddr string) {
	defer clientConn.Close()

	logutil.Debug("forward", "TCP 新连接: %s", clientConn.RemoteAddr())

	targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		logutil.Error("forward", "连接目标 %s 失败: %v", targetAddr, err)
		return
	}
	defer targetConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(targetConn, clientConn)
		targetConn.(*net.TCPConn).CloseWrite()
	}()

	go func() {
		defer wg.Done()
		io.Copy(clientConn, targetConn)
		clientConn.(*net.TCPConn).CloseWrite()
	}()

	wg.Wait()
	logutil.Debug("forward", "TCP 连接关闭: %s", clientConn.RemoteAddr())
}

func ForwardUDP(listenAddr string, targetAddr string) error {
	listenUDP, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return err
	}

	targetUDP, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", listenUDP)
	if err != nil {
		return err
	}
	defer conn.Close()

	logutil.Info("forward", "UDP 转发启动: %s -> %s", listenAddr, targetAddr)

	buf := make([]byte, 65535)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			logutil.Error("forward", "UDP 读取错误: %v", err)
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		go func(data []byte, clientAddr *net.UDPAddr) {
			targetConn, err := net.DialUDP("udp", nil, targetUDP)
			if err != nil {
				logutil.Error("forward", "UDP 连接目标失败: %v", err)
				return
			}
			defer targetConn.Close()

			if _, err := targetConn.Write(data); err != nil {
				logutil.Error("forward", "UDP 写入目标失败: %v", err)
				return
			}

			respBuf := make([]byte, 65535)
			if err := targetConn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
				return
			}
			rn, _, err := targetConn.ReadFromUDP(respBuf)
			if err != nil {
				logutil.Error("forward", "UDP 读取目标响应失败: %v", err)
				return
			}

			if _, err := conn.WriteToUDP(respBuf[:rn], clientAddr); err != nil {
				logutil.Error("forward", "UDP 写回客户端失败: %v", err)
			}
		}(data, clientAddr)
	}
}
