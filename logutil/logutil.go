package logutil

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

var (
	verbose bool
	mu      sync.RWMutex
)

type LogEntry struct {
	Time    string `json:"time"`
	Module  string `json:"module"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

var (
	logBuffer []LogEntry
	bufMu     sync.RWMutex
	maxLogs   = 200
)

var (
	totalBytesIn  int64
	totalBytesOut int64
	activeChans   int64
)

func AddBytesIn(n int64)      { atomic.AddInt64(&totalBytesIn, n) }
func AddBytesOut(n int64)     { atomic.AddInt64(&totalBytesOut, n) }
func AddActiveChan(delta int64) { atomic.AddInt64(&activeChans, delta) }

func GetBytesIn() int64  { return atomic.LoadInt64(&totalBytesIn) }
func GetBytesOut() int64 { return atomic.LoadInt64(&totalBytesOut) }
func GetActiveChans() int { return int(atomic.LoadInt64(&activeChans)) }

type TrafficPoint struct {
	Time         int64 `json:"time"`
	BytesInRate  int64 `json:"bytes_in_rate"`
	BytesOutRate int64 `json:"bytes_out_rate"`
}

var (
	trafficHistory   []TrafficPoint
	trafficMu        sync.RWMutex
	maxTrafficPoints = 60

	lastBytesIn  int64
	lastBytesOut int64
)

func RecordTraffic() {
	in := atomic.LoadInt64(&totalBytesIn)
	out := atomic.LoadInt64(&totalBytesOut)

	inRate := in - lastBytesIn
	outRate := out - lastBytesOut
	lastBytesIn = in
	lastBytesOut = out

	point := TrafficPoint{
		Time:         time.Now().Unix(),
		BytesInRate:  inRate,
		BytesOutRate: outRate,
	}

	trafficMu.Lock()
	trafficHistory = append(trafficHistory, point)
	if len(trafficHistory) > maxTrafficPoints {
		trafficHistory = trafficHistory[len(trafficHistory)-maxTrafficPoints:]
	}
	trafficMu.Unlock()
}

func GetTrafficHistory() []TrafficPoint {
	trafficMu.RLock()
	defer trafficMu.RUnlock()
	result := make([]TrafficPoint, len(trafficHistory))
	copy(result, trafficHistory)
	return result
}

func SetVerbose(v bool) {
	mu.Lock()
	verbose = v
	mu.Unlock()
}

func IsVerbose() bool {
	mu.RLock()
	defer mu.RUnlock()
	return verbose
}

func addLog(module, level, msg string) {
	entry := LogEntry{
		Module:  module,
		Level:   level,
		Message: msg,
	}
	bufMu.Lock()
	logBuffer = append(logBuffer, entry)
	if len(logBuffer) > maxLogs {
		logBuffer = logBuffer[len(logBuffer)-maxLogs:]
	}
	bufMu.Unlock()
}

func Debug(module, format string, args ...interface{}) {
	if !IsVerbose() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	addLog(module, "DEBUG", msg)
	log.Printf("[%s] DEBUG %s", module, msg)
}

func Info(module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	addLog(module, "INFO", msg)
	log.Printf("[%s] INFO %s", module, msg)
}

func Warn(module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	addLog(module, "WARN", msg)
	log.Printf("[%s] WARN %s", module, msg)
}

func Error(module, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	addLog(module, "ERROR", msg)
	log.Printf("[%s] ERROR %s", module, msg)
}

func GetRecentLogs() []LogEntry {
	bufMu.RLock()
	defer bufMu.RUnlock()
	result := make([]LogEntry, len(logBuffer))
	copy(result, logBuffer)
	return result
}
