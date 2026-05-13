package plugin

import (
	"sync"
	"time"
)

type RateLimit struct {
	bytesPerSec int64
	tokens      float64
	maxTokens   float64
	lastRefill  time.Time
	mu          sync.Mutex
}

func NewRateLimit(bytesPerSec int64) *RateLimit {
	if bytesPerSec <= 0 {
		return nil
	}
	maxT := float64(bytesPerSec)
	return &RateLimit{
		bytesPerSec: bytesPerSec,
		tokens:      maxT,
		maxTokens:   maxT,
		lastRefill:  time.Now(),
	}
}

func (r *RateLimit) refill() {
	now := time.Now()
	elapsed := now.Sub(r.lastRefill).Seconds()
	r.lastRefill = now
	r.tokens += elapsed * float64(r.bytesPerSec)
	if r.tokens > r.maxTokens {
		r.tokens = r.maxTokens
	}
}

func (r *RateLimit) Wait(n int) {
	if r == nil || r.bytesPerSec <= 0 {
		return
	}
	r.mu.Lock()
	r.refill()

	if r.tokens >= float64(n) {
		r.tokens -= float64(n)
		r.mu.Unlock()
		return
	}

	deficit := float64(n) - r.tokens
	waitSec := deficit / float64(r.bytesPerSec)
	r.tokens = 0
	r.mu.Unlock()

	if waitSec > 0 {
		time.Sleep(time.Duration(waitSec * float64(time.Second)))
	}

	r.mu.Lock()
	r.refill()
	r.tokens -= float64(n)
	if r.tokens < 0 {
		r.tokens = 0
	}
	r.mu.Unlock()
}

func (r *RateLimit) BytesPerSec() int64 {
	if r == nil {
		return 0
	}
	return r.bytesPerSec
}
