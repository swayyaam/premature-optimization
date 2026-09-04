# Step 8: Timeouts, limits, and refusing work

**Code:** [`limits.go`](../limits.go), [`main.go`](../main.go).
**Load test output:**
[`benchmarks/08-cache-stall.txt`](../benchmarks/08-cache-stall.txt),
[`benchmarks/08-limits.txt`](../benchmarks/08-limits.txt),
[`benchmarks/08-normal.txt`](../benchmarks/08-normal.txt).

## What we are building

Three things that all decide what to do when something goes wrong:

- Deadlines, so a slow dependency is given up on instead of waited for.
- A limit on how many requests are handled at once, with anything beyond it refused
  straight away.
- A rate limit per client.

The step starts by breaking the service on purpose to show what it does without them.

## Breaking it

Redis is a cache. Every link in it is a copy of a row in Postgres, and the application
already treats a cache error as a miss and reads from Postgres instead. Losing the cache
should be survivable.

So: twenty seconds of load on the redirect endpoint, and five seconds in, Redis is told
to stop answering for eight seconds.

```bash
hey -z 20s -c 50 -disable-redirects http://localhost:8080/seed250000
redis-cli CLIENT PAUSE 8000     # at t=5s
```

```
  Requests/sec:  25786.78
  Slowest:       8.1036 secs
  [302]          515764 responses
```

515,764 responses divided by the twelve seconds Redis was healthy is 42,980 requests per
second, which is the normal rate. So essentially nothing was served during the other
eight seconds. A cache stalled and the whole service stopped, while Postgres sat there
healthy and able to answer every request.

The slowest request took 8.1 seconds. It was waiting for Redis, because nothing in the
code had said how long it was willing to wait.

### The percentiles hid it completely

```
  99%% in 0.0016 secs
```

The 99th percentile of that run is 1.6 ms. An eight second outage is invisible in it,
because only about fifty requests were caught in the stall out of half a million, which
is a hundredth of a percent.

Percentiles over a whole run describe the run, not the worst moment in it. `Slowest`, and
the arithmetic on the throughput, are what showed the problem.

---

## Deadlines

### On the call

```go
const cacheTimeout = 50 * time.Millisecond

ctx, cancel := context.WithTimeout(ctx, cacheTimeout)
defer cancel()

url, err := s.cache.Get(ctx, "link:"+code).Result()
```

`context.WithTimeout` makes a context that cancels itself after the given time, and it
inherits from the one passed in, so the request being abandoned still cancels this too.
`cancel` has to be called to release it, which is what the `defer` is for.

A cache is worth having while it is fast. Fifty milliseconds is already far longer than a
healthy answer, which is tens of microseconds, so anything slower than this is a cache
that is not working, and the read gives up and asks Postgres.

Repeating the experiment with this in place:

```
  Slowest:  5.0566 secs
```

Better, and nowhere near fixed. The deadline on the call was doing its job and the request
was still waiting five seconds.

### On the client

The call could not start until the client had a connection to give it, and the pool has
its own timeout which was still at the library's default of several seconds. The deadline
on the call cannot help with time spent before the call.

```go
opts.ReadTimeout = cacheTimeout
opts.WriteTimeout = cacheTimeout
opts.PoolTimeout = cacheTimeout
opts.MaxRetries = -1
```

`MaxRetries = -1` disables retries. In this library `0` does not mean none, it means the
default of three, so a failing cache would be asked four times before anyone was told.

```
  Slowest:  0.2064 secs
```

Eight seconds to a fifth of a second. Every request in all three runs returned a correct
redirect, so nothing about this changed what the service does, only how long it is
prepared to wait.

**What this does not fix.** During the stall the service serves roughly a thousand
requests per second instead of forty two thousand, because every single request now waits
the full fifty milliseconds before giving up and going to Postgres. The damage is bounded
rather than removed. What was an outage is now a slowdown.

### On the server

```go
srv := &http.Server{
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       10 * time.Second,
	WriteTimeout:      10 * time.Second,
	IdleTimeout:       60 * time.Second,
}
```

These are about slow clients rather than slow dependencies. `http.ListenAndServe` uses a
server with none of them set, so a client can open a connection, send one byte of a
request header, and hold that connection open for as long as it likes. A few hundred of
those and the server has no capacity left for anyone else, and no request has technically
gone wrong.

---

## Middleware

The two limits are written as middleware, which is a function that takes a handler and
returns a handler with something added around it:

```go
func limitInFlight(limit int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ...decide something...
		next.ServeHTTP(w, r)
	})
}
```

Because it takes and returns the same interface, wrappers stack:

```go
h = newIPLimiter(rps, burst).middleware(h)
return limitInFlight(limit, h)
```

The rate limiter sees a request first, then the in-flight limit, then the router.

## Refusing work

```go
slots := make(chan struct{}, limit)

select {
case slots <- struct{}{}:
	defer func() { <-slots }()
	next.ServeHTTP(w, r)
default:
	w.Header().Set("Retry-After", "1")
	http.Error(w, "too many requests in progress", http.StatusServiceUnavailable)
}
```

A buffered channel used as a set of permits. Taking a slot is a send, giving it back is a
receive, and the `default` case runs when no slot is free.

`struct{}` is a type that holds nothing and takes no memory. It is what Go uses when only
the fact of a value matters and never the value.

The behaviour this produces is the point of the step. Five slots against fifty concurrent
clients:

```
  [302]  53266 responses
  [503]  432681 responses
  99% in 0.0014 secs
```

Most requests were refused, and the ones that were accepted were served in 1.4 ms, which
is the speed of a server with no load problem at all.

A server with no such limit accepts everything. Each accepted request holds a connection,
a goroutine and memory while it waits, and the result is that everything gets slower
together until the whole thing falls over. Refusing immediately keeps the accepted work
fast and tells the rest to come back, which is something a client can act on.

## Rate limiting

```go
c.limiter = rate.NewLimiter(l.rps, l.burst)
// ...
return c.limiter.Allow()
```

`golang.org/x/time/rate` is a token bucket. Tokens are added at a fixed rate up to a
maximum, a request takes one, and a request that finds none is refused. This is the first
dependency in this project that is not there to talk to something else. It is here
because a correct token bucket is fiddly to write and this one is maintained by the Go
team.

One limiter is kept per client address:

```go
addr := r.RemoteAddr
if host, _, err := net.SplitHostPort(addr); err == nil {
	addr = host
}
```

`r.RemoteAddr` includes the port, which changes with every connection, so the port is
stripped and the address is what is counted.

That map would grow for as long as the process runs, so a goroutine sweeps it every five
minutes and drops limiters for addresses that have gone quiet.

Ten per second, with twenty five requests sent as fast as `curl` manages:

```
302 302 302 302 302 302 302 302 302 302 429 429 429 429 429 429 429 429 429 429 302 429 ...
```

The first ten are the burst. The single 302 in position twenty one is one token refilling,
about a tenth of a second in.

Both limits are off or generous by default. Every request in every measurement in this
repo comes from `127.0.0.1`, so a per client rate limit would refuse the load generator.
`RATE_LIMIT=10` and `MAX_IN_FLIGHT=5` turn them down far enough to see.

## Logging during a failure

```go
func (s *store) logCacheError(code string, err error) {
	now := time.Now().UnixNano()
	last := s.lastCacheLog.Load()
	if now-last < int64(time.Second) {
		return
	}
	if s.lastCacheLog.CompareAndSwap(last, now) {
		log.Printf("cache %q: %v", code, err)
	}
}
```

When the cache is broken, every request fails in the same way. Logging each one means
forty thousand identical lines a second, which is enough work to become a second problem
on top of the first. This prints at most one a second.

`CompareAndSwap` writes a new value only if the old one is still what you read, which is
how two goroutines arriving at the same moment do not both decide it is their turn. The
eight second stall produced eight lines.

---

## Load test results

Normal conditions, nothing failing, five runs of ten seconds at fifty concurrent:

| | Without any of this | With it |
| --- | --- | --- |
| Requests per second | 43,553 | 42,432 |
| 99th percentile | 1.5 ms | 1.6 ms |

2.6% lower, which is exactly the spread this machine shows between identical runs. The
cost is at the edge of what can be measured here.

Under a stalled cache:

| | Slowest request |
| --- | --- |
| No timeouts | 8.10 s |
| Deadline on the call only | 5.06 s |
| Deadline plus client timeouts | 0.21 s |

## Try it

```bash
go run .
```

```bash
RATE_LIMIT=10 go run .
for i in $(seq 1 25); do curl -s -o /dev/null -w '%{http_code} ' localhost:8080/seed250000; done
```

```bash
MAX_IN_FLIGHT=5 go run .
hey -z 6s -c 50 -disable-redirects http://localhost:8080/seed250000
```

Stalling the cache while load is running:

```bash
redis-cli CLIENT PAUSE 8000
```
