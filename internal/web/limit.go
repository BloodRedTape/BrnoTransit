package web

import (
	"context"
	"math"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	generalPerMinute = 300
	generalBurst     = 120
	heavyPerMinute   = 30
	heavyBurst       = 10
	apiPerMinute     = 60
	apiBurst         = 20
	heavyQueueWait   = 3 * time.Second
	maxStreamsPerIP  = 4
	maxStreamsTotal  = 256
	sweepEvery       = time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu        sync.Mutex
	rate      float64
	burst     float64
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newRateLimiter(perMinute, burst int) *rateLimiter {
	return &rateLimiter{rate: float64(perMinute) / 60, burst: float64(burst), buckets: map[string]*bucket{}}
}

func (l *rateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

func (l *rateLimiter) sweep(now time.Time) {
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}

type semaphore chan struct{}

func (s semaphore) acquire(ctx context.Context, wait time.Duration) bool {
	select {
	case s <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case s <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s semaphore) release() { <-s }

type connLimiter struct {
	mu     sync.Mutex
	perKey int
	max    int
	total  int
	counts map[string]int
}

func (c *connLimiter) acquire(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total >= c.max || c.counts[key] >= c.perKey {
		return false
	}
	c.total++
	c.counts[key]++
	return true
}

func (c *connLimiter) release(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total--
	if c.counts[key]--; c.counts[key] <= 0 {
		delete(c.counts, key)
	}
}

type limits struct {
	general *rateLimiter
	heavy   *rateLimiter
	api     *rateLimiter
	work    semaphore
	streams *connLimiter
}

func newLimits() *limits {
	return &limits{
		general: newRateLimiter(generalPerMinute, generalBurst),
		heavy:   newRateLimiter(heavyPerMinute, heavyBurst),
		api:     newRateLimiter(apiPerMinute, apiBurst),
		work:    make(semaphore, max(2, runtime.NumCPU())),
		streams: &connLimiter{perKey: maxStreamsPerIP, max: maxStreamsTotal, counts: map[string]int{}},
	}
}

func tooMany(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retry.Seconds())))))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}

func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	http.Error(w, "server busy, try again shortly", http.StatusServiceUnavailable)
}

func rateLimit(l *rateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, retry := l.allow(clientKey(r), time.Now()); !ok {
			tooMany(w, retry)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *limits) heavyWork(next http.Handler) http.Handler {
	return rateLimit(l.heavy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.work.acquire(r.Context(), heavyQueueWait) {
			busy(w)
			return
		}
		defer l.work.release()
		next.ServeHTTP(w, r)
	}))
}

func (l *limits) stream(next http.Handler) http.Handler {
	return rateLimit(l.api, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)
		if !l.streams.acquire(key) {
			busy(w)
			return
		}
		defer l.streams.release(key)
		next.ServeHTTP(w, r)
	}))
}

func remoteAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	return ip.Unmap(), err == nil
}

func fromProxy(r *http.Request) bool {
	ip, ok := remoteAddr(r)
	return ok && (ip.IsLoopback() || ip.IsPrivate())
}

func clientIP(r *http.Request) (netip.Addr, bool) {
	if fromProxy(r) {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			last := xff[len(xff)-1]
			if i := strings.LastIndexByte(last, ','); i >= 0 {
				last = last[i+1:]
			}
			if ip, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
				return ip.Unmap(), true
			}
		}
	}
	return remoteAddr(r)
}

func clientKey(r *http.Request) string {
	ip, ok := clientIP(r)
	if !ok {
		return r.RemoteAddr
	}
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}
