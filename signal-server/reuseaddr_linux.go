//go:build linux

package main

import (
	"net"
	"syscall"
)

func listenTCP(addr string) (net.Listener, error) {
	cfg := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			err := c.Control(func(fd uintptr) {
				opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
			if err != nil {
				return err
			}
			return opErr
		},
	}
	return cfg.Listen(nil, "tcp", addr)
}
