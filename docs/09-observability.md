# Step 9: Structured logs and metrics

**Code:** [`metrics.go`](../metrics.go), [`logging.go`](../logging.go).
**Load test output:**
[`benchmarks/09-observability.txt`](../benchmarks/09-observability.txt),
[`benchmarks/09-normal.txt`](../benchmarks/09-normal.txt).

## What we are building

Two things:

- Every log line becomes a set of named fields instead of a sentence, using `log/slog`
  from the standard library.
- The process keeps counters and histograms of what it is doing and publishes them at
  `GET /metrics`, using the Prometheus client library.

## Why

The previous step paused Redis for eight seconds under load. The service kept answering
correctly and the load test reported this at the end:

```
  99%% in 0.0019 secs
```

Under two milliseconds at the 99th percentile, for a run in which the service spent seven
seconds serving about one percent of its normal traffic.

The percentiles are not wrong. They describe the whole run, and the run was mostly
healthy. The bad seconds contribute almost nothing to them, because during those seconds
almost no requests were being served, so there are almost none to count. A period where
the service nearly stops serving is a period that barely appears in any statistic made
from the requests it served.

Finding it took reading `Slowest`, dividing the total by the seconds that were healthy,
and noticing the answer was the normal rate. That works once, when you already suspect
something. It does not work as a way of running a service.

The application also runs as several processes now, so "watch the terminal" stopped being
a strategy a while ago.

---

## Structured logging

`log.Printf` produces a sentence. Sentences are fine to read and hard to search, and the
values inside them have to be picked back out with a regular expression.

`log/slog` has been in the standard library since Go 1.21, so this needs no dependency:

```go
slog.Info("clicks queued", "workers", workers, "batch_size", batchSize, "queue_size", queueSize)
slog.Error("cache failing", "code", code, "error", err)
```

The message stays fixed and everything that varies becomes a named field. Arguments come
in pairs, a name then a value.

```
time=2026-09-04T18:04:23.079+05:30 level=INFO msg="clicks queued" workers=4 batch_size=500 queue_size=10000
```

Where the output goes is a **handler**:

```go
if os.Getenv("LOG_FORMAT") == "json" {
	handler = slog.NewJSONHandler(os.Stderr, opts)
} else {
	handler = slog.NewTextHandler(os.Stderr, opts)
}
slog.SetDefault(slog.New(handler))
```

Both are structured. The text one is the same fields in a form that is readable in a
terminal, and the JSON one is for anything that collects logs. The code that writes the
logs does not change either way, which is the point of separating the two.

`slog` has no equivalent of `log.Fatalf`, because ending the process is not something a
logger should decide. So there is a small helper:

```go
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
```

`args ...any` is a variadic parameter, and passing `args...` hands the whole slice
straight through.

---

## Metrics

A log line is a record of one event. A metric is a number describing all of them, and it
is what answers "how is the service doing right now".

```go
requests = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "shortener_requests_total",
	Help: "Requests served, by route and response status.",
}, []string{"route", "status"})
```

Three kinds are used here.

A **counter** only goes up. `shortener_requests_total` and
`shortener_cache_operations_total` are counters. You never read the value itself, you read
how fast it is going up, which is the difference between two readings divided by the time
between them.

A **gauge** goes up and down and describes right now. `shortener_requests_in_flight` is a
gauge.

A **histogram** counts how many observations fell into each of a set of ranges.
`shortener_request_duration_seconds` is a histogram, which is what makes it possible to
ask what the 99th percentile was during one particular minute rather than across a whole
run.

The buckets have to be picked in advance:

```go
Buckets: []float64{
	0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005,
	0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
},
```

A healthy response here is about one millisecond and a broken one is over a hundred, so
the buckets run from well below the first to well above the second. Buckets that all sit
on one side of where the answers actually are record nothing useful.

### Nothing is sent anywhere

```go
mux.Handle("GET /metrics", promhttp.Handler())
```

The numbers live in the process's memory and are published on a URL. Prometheus fetches
that URL every so often and keeps the history. There is no agent, no network calls out of
the process, and if nothing ever reads the endpoint the cost is a few counters being
incremented.

```
shortener_requests_total{route="GET /{code}",status="302"} 2
shortener_requests_total{route="GET /{code}",status="404"} 1
shortener_cache_operations_total{result="hit"} 1
shortener_cache_operations_total{result="miss"} 2
shortener_clicks_total{result="queued"} 2
shortener_clicks_total{result="written"} 2
shortener_requests_in_flight 1
```

### Measuring every request

```go
func measure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		inFlightNow.Inc()
		next.ServeHTTP(sw, r)
		inFlightNow.Dec()

		requests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		requestDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}
```

More middleware, wrapped outside everything else so it sees requests the limits refuse as
well as the ones that are served.

### Reading the status code back

`http.ResponseWriter` can be written to and cannot be read from, so there is no way to ask
what status was sent. The only way to know is to be the thing that sent it:

```go
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
```

Writing a type inside a struct with no field name is **embedding**. Every method of the
embedded thing becomes a method of the outer struct, so `statusWriter` satisfies
`http.ResponseWriter` without listing its methods. Declaring `WriteHeader` on the outside
takes over that one, and the rest are still the original.

The default of `http.StatusOK` matters, because a handler that only calls `Write` never
calls `WriteHeader` at all, and 200 is what actually gets sent.

### Labels are where this goes wrong

```go
route := r.Pattern
```

`r.Pattern` is the route that matched, `GET /{code}`, and not the path that was asked for,
`/seed250000`.

That distinction is the most important line in the file. Every different label value makes
a separate counter that lives in memory for as long as the process runs. Labelling by path
would make one for every short code anyone has ever followed. A few million links and the
metrics use more memory than the links do, which is a well known way to turn a monitoring
system into the outage.

Labels are for things with a small fixed set of values. Route, status, hit or miss. Never
an identifier.

---

## What the metrics showed

The step 8 experiment again, with `/metrics` read once a second while it ran.

```
   t   requests/s  cache_err/s  mean_ms
   2        42811            0      1.08
   5        45671            0      0.96
   6        35741           70      1.24
   7          411          419    123.92
   8          382          368    141.05
   9          355          390    137.29
  12          392          406    132.87
  13          385          374    137.42
  14        22116          179      2.37
  15        43961            0      1.04
  18        42649            0      1.07
```

Throughput fell by a factor of a hundred, mean latency went up by a factor of a hundred
and thirty, and the cache error counter says why. The recovery at t=14 is visible too.

The same run, summarised once at the end:

```
  Requests/sec:  25536.53
  Average:       0.0020 secs
  99%% in 0.0019 secs
```

Both describe the same twenty seconds. One of them is usable.

## What it costs

Five runs of ten seconds at fifty concurrent, nothing failing.

| | Without metrics or slog | With them |
| --- | --- | --- |
| Requests per second | 42,432 | 42,344 |
| 99th percentile | 1.6 ms | 1.6 ms |

0.2%, against a machine that varies by 2.6% between identical runs. Incrementing a few
counters per request does not show up.

## Try it

```bash
go run .
curl -s localhost:8080/metrics | grep shortener_
```

```bash
LOG_FORMAT=json go run .
```

Watching a rate by hand, the way a scraper would:

```bash
curl -s localhost:8080/metrics | grep shortener_requests_total
sleep 1
curl -s localhost:8080/metrics | grep shortener_requests_total
```
