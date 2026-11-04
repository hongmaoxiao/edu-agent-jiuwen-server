package gateway

import (
	"net"
	"strings"
	"sync"
	"time"
)

type rateEntry struct {
	count int
	until time.Time
}

// limiter is a fixed-window counter per key, one table per key kind ("ip", "login", …),
// so filling one kind cannot lock out the others. Same as deepfate-server.
type limiter struct {
	sync.Mutex
	kinds map[string]map[string]rateEntry
}

const limiterSize = 4096

func (l *limiter) allow(key string, limit int, window time.Duration) bool {
	l.Lock()
	defer l.Unlock()
	kind, _, _ := strings.Cut(key, ":")
	if l.kinds == nil {
		l.kinds = map[string]map[string]rateEntry{}
	}
	entries := l.kinds[kind]
	if entries == nil {
		entries = map[string]rateEntry{}
		l.kinds[kind] = entries
	}
	now := time.Now()
	entry, exists := entries[key]
	if exists && now.Before(entry.until) {
		if entry.count >= limit {
			return false
		}
		entry.count++
		entries[key] = entry
		return true
	}
	// ponytail: process-local limits for one instance; use shared limits before scaling out.
	if !exists && len(entries) >= limiterSize {
		for k, v := range entries {
			if !now.Before(v.until) {
				delete(entries, k)
			}
		}
		if len(entries) >= limiterSize {
			oldest := ""
			for k, v := range entries {
				if oldest == "" || v.until.Before(entries[oldest].until) {
					oldest = k
				}
			}
			delete(entries, oldest)
		}
	}
	entries[key] = rateEntry{count: 1, until: now.Add(window)}
	return true
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func ipBlock(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
