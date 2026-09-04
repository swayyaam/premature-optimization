package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Counters and histograms the process keeps in memory. Prometheus reads them by
// fetching /metrics, so nothing is sent anywhere and there is no agent.
var (
	requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "shortener_requests_total",
		Help: "Requests served, by route and response status.",
	}, []string{"route", "status"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "shortener_request_duration_seconds",
		Help: "How long requests took, by route.",
		// Buckets have to be chosen. These run from well under a healthy
		// response to well over the point where something is wrong.
		Buckets: []float64{
			0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005,
			0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
		},
	}, []string{"route"})

	cacheOps = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "shortener_cache_operations_total",
		Help: "Cache lookups, by outcome: hit, miss or error.",
	}, []string{"result"})

	clickOps = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "shortener_clicks_total",
		Help: "Clicks, by what happened to them: queued, dropped or written.",
	}, []string{"result"})

	inFlightNow = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "shortener_requests_in_flight",
		Help: "Requests being handled right now.",
	})
)

// statusWriter remembers the status code on its way out.
//
// http.ResponseWriter has no way to read back what was written, so the only way
// to record it is to wrap it. The embedded interface means every method that is
// not overridden below is the original one.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// measure records a count and a duration for every request.
func measure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		inFlightNow.Inc()
		next.ServeHTTP(sw, r)
		inFlightNow.Dec()

		// r.Pattern is the route that matched, "GET /{code}", rather than the
		// path that was asked for. Using the path would make a new set of
		// counters for every short code that has ever been followed, which is
		// millions of them, and would use more memory than the links do.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}

		requests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		requestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}
