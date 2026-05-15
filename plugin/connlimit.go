package plugin

import (
	"net"
	"sync/atomic"
)

type ConnLimit struct {
	maxConns int32
	current  int32
}

func NewConnLimit(maxConns int) *ConnLimit {
	return &ConnLimit{maxConns: int32(maxConns)}
}

func (l *ConnLimit) OnAccept(proto string, addr net.Addr) bool {
	if l.maxConns <= 0 {
		return true
	}
	cur := atomic.LoadInt32(&l.current)
	return cur < l.maxConns
}

func (l *ConnLimit) OnOpen() {
	if l.maxConns <= 0 {
		return
	}
	for {
		cur := atomic.LoadInt32(&l.current)
		if cur >= l.maxConns {
			return
		}
		if atomic.CompareAndSwapInt32(&l.current, cur, cur+1) {
			return
		}
	}
}

func (l *ConnLimit) OnClose() {
	atomic.AddInt32(&l.current, -1)
}

func (l *ConnLimit) Current() int32 {
	return atomic.LoadInt32(&l.current)
}

func (l *ConnLimit) Max() int32 {
	return l.maxConns
}
