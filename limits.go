package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// How long a request may wait for the cache before the cache is given up
	// on and Postgres is asked instead.
	cacheTimeout = 50 * time.Millisecond
	// How many requests may be in progress at once.
	defaultInFlight = 512
	// How often unused per-client rate limiters are thrown away.
	limiterSweep = 5 * time.Minute
)

// limitInFlight caps how many requests are being handled at the same time.
// Anything arriving beyond the cap is refused immediately.
//
// Refusing quickly is the point. Without a cap, requests that cannot be served
// yet still take a connection, a goroutine and memory, and a server under more
// load than it can handle gets slower at everything rather than saying no to
// anything.
func limitInFlight(limit int, next http.Handler) http.Handler {
	if limit <= 0 {
		return next
	}

	// A buffered channel used as a set of permits. Taking a slot is a send,
	// giving it back is a receive.
	slots := make(chan struct{}, limit)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests in progress", http.StatusServiceUnavailable)
		}
	})
}

// ipLimiter keeps one rate limiter per client address.
type ipLimiter struct {
	mu      sync.Mutex
	clients map[string]*client
	rps     rate.Limit
	burst   int
}

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPLimiter(rps float64, burst int) *ipLimiter {
	l := &ipLimiter{
		clients: make(map[string]*client),
		rps:     rate.Limit(rps),
		burst:   burst,
	}
	go l.sweep()
	return l
}

// allow reports whether this address may make a request now.
func (l *ipLimiter) allow(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	c, known := l.clients[addr]
	if !known {
		c = &client{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.clients[addr] = c
	}
	c.lastSeen = time.Now()

	return c.limiter.Allow()
}

// sweep drops limiters for addresses that have gone quiet, so the map does not
// grow for as long as the process runs.
func (l *ipLimiter) sweep() {
	for range time.Tick(limiterSweep) {
		l.mu.Lock()
		for addr, c := range l.clients {
			if time.Since(c.lastSeen) > limiterSweep {
				delete(l.clients, addr)
			}
		}
		l.mu.Unlock()
	}
}

func (l *ipLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := r.RemoteAddr
		if host, _, err := net.SplitHostPort(addr); err == nil {
			addr = host
		}

		if !l.allow(addr) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withLimits wraps the router in whichever limits are configured.
func withLimits(h http.Handler) http.Handler {
	if rps := envFloat("RATE_LIMIT", 0); rps > 0 {
		burst := int(envFloat("RATE_BURST", rps))
		slog.Info("rate limit enabled", "per_second", rps, "burst", burst)
		h = newIPLimiter(rps, burst).middleware(h)
	}

	limit := int(envFloat("MAX_IN_FLIGHT", defaultInFlight))
	if limit > 0 {
		slog.Info("in flight limit enabled", "limit", limit)
	}
	return limitInFlight(limit, h)
}

func envFloat(name string, fallback float64) float64 {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		fatal("setting is not a number", "name", name, "value", raw)
	}
	return v
}
