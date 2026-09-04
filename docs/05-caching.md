# Step 5: Caching reads in Redis

**Code:** [`main.go`](../main.go).
**Load test output:** [`benchmarks/05-cache-hit.txt`](../benchmarks/05-cache-hit.txt),
[`benchmarks/05-no-cache.txt`](../benchmarks/05-no-cache.txt).

## What we are building

A read of a short link now looks in Redis first. If the link is there, Postgres is not
asked at all. If it is not, Postgres answers and the result is written to Redis so the
next read finds it.

The cache can also be turned off, with the same binary, by setting `REDIS_URL` to an
empty string. That exists so the two configurations can be measured against each other
without changing the code between runs.

## Cache-aside

The pattern has three steps and the application does all of them:

1. Look in the cache. If the value is there, return it.
2. Otherwise read from the database.
3. Write what the database returned into the cache, then return it.

```go
func (s *store) find(ctx context.Context, code string) (string, error) {
	if url, ok := s.cacheGet(ctx, code); ok {
		return url, nil
	}

	var url string
	err := s.db.QueryRowContext(ctx, "SELECT url FROM links WHERE code = $1", code).Scan(&url)
	if err != nil {
		return "", err
	}

	s.cachePut(ctx, code, url)
	return url, nil
}
```

The cache is never the only copy of anything. Everything in it came from Postgres and can
be rebuilt by asking Postgres again, which is what makes it safe to lose.

Entries are written with an hour's expiry:

```go
s.cache.Set(ctx, "link:"+code, url, cacheTTL)
```

A short link in this application never changes once created, so the entry cannot go
stale. The expiry is there to put a limit on how much Redis holds, by letting links that
stopped being asked for fall out on their own.

Keys are prefixed with `link:` so that anything else stored later is easy to tell apart.

---

## The Go in this step

### `context.Context`

Every call into Redis takes a `context.Context` as its first argument, and the Postgres
calls have been changed to the `Context` versions to match:

```go
s.db.QueryRowContext(ctx, "SELECT url FROM links WHERE code = $1", code)
s.db.ExecContext(ctx, "INSERT INTO links ...", code, url)
s.cache.Get(ctx, "link:"+code)
```

A `Context` is a value that travels down through a call chain carrying one message:
whether the work is still wanted. Something at the top can cancel it, and every function
holding that context finds out and can stop early instead of finishing work nobody is
waiting for.

The convention is rigid, and following it is most of what you need to know. A context is
the first parameter, it is named `ctx`, and it is passed down rather than stored in a
struct.

There are two ways a context enters this program.

```go
url, err := s.find(r.Context(), code)
```

`r.Context()` on an HTTP request is cancelled when the client goes away. If a browser
gives up on a redirect while Postgres is still working on the query, the query is
cancelled rather than run to completion for a reply nobody will read. Under load, work
that nobody is waiting for is the most wasteful kind.

```go
ctx := context.Background()
```

`context.Background()` is the empty context, used at the start of the program where there
is nothing to inherit from and nothing to cancel it. Startup uses it for the database and
cache connection checks.

### `redis.Nil`

A key that is not in Redis comes back as an error rather than an empty value, in the same
way `database/sql` reports a missing row:

```go
url, err := s.cache.Get(ctx, "link:"+code).Result()
if err == nil {
	return url, true
}
if !errors.Is(err, redis.Nil) {
	log.Printf("cache get %q: %v", code, err)
}
return "", false
```

`redis.Nil` is the specific error meaning "no such key", and it is matched with
`errors.Is` for the same reason `sql.ErrNoRows` is.

The shape of this function matters more than the detail. Any error that is not
`redis.Nil` gets logged and then treated as a miss, so the read carries on to Postgres.
A cache that is answering with errors makes the application slower, and it does not make
it fail.

### `os.LookupEnv`

```go
url, set := os.LookupEnv("REDIS_URL")
if !set {
	url = defaultRedisURL
}
if url == "" {
	log.Print("no cache configured, every read will go to Postgres")
	return nil
}
```

`os.Getenv` returns an empty string both when a variable is missing and when it is set to
nothing. `os.LookupEnv` returns a second value telling the two apart, which is what makes
`REDIS_URL=` mean "run without a cache" while leaving it unset means "use the default".

### Failing at startup, tolerating failure later

The two are handled differently on purpose.

At startup the cache is pinged, and the program exits if it cannot be reached:

```
cannot reach the cache: dial tcp [::1]:6399: connect: connection refused
```

A cache that was configured wrongly should be loud, because the alternative is a service
that quietly runs with no cache and nobody notices for a month.

Once running, an error from Redis is counted as a miss and the request goes to Postgres.
Losing the cache at that point costs speed, not correctness.

---

## Setting it up

Redis with its default settings is all this needs.

```bash
brew install redis
brew services start redis
redis-cli ping
```

## Try it

```bash
go run .
```

```bash
curl -X POST localhost:8080/shorten -d '{"url":"https://go.dev"}'
curl -i localhost:8080/<code-from-above>
redis-cli GET link:<code-from-above>
redis-cli TTL link:<code-from-above>
```

Running with no cache at all:

```bash
REDIS_URL= go run .
```

---

## Load test results

Five runs of ten seconds at fifty concurrent, same binary, same machine, the only
difference being whether the cache was on. One link is requested over and over, so once
it is warm every read is a hit.

| | Cache off | Cache on |
| --- | --- | --- |
| Requests per second | 42,142 | 43,021 |
| Mean | 1.2 ms | 1.2 ms |
| 99th percentile | 5.6 ms | 1.5 ms |

**Throughput did not improve.** The difference is 2.1%, and the measured spread of this
machine doing nothing different is 2.6%. By the rule set out when that spread was
measured, a 2.1% difference has not been shown to be a difference at all.

**The 99th percentile improved by 3.7 times**, and it stopped moving. All five cached
runs reported 1.5 ms, with no variation between them at all. The uncached runs ranged
from 4.9 ms to 6.4 ms.

### Why throughput did not move

Throughput under a fixed number of concurrent requests is decided by how long the average
request takes. Fifty requests in flight, each taking 1.2 ms, is about 41,700 requests per
second, and that is what both configurations measure.

The cache did not change the mean. It changed the tail. Postgres was already answering
most requests in well under a millisecond, so there was very little mean latency for a
cache to remove. What Postgres also did was occasionally take 6 ms, and those occasional
slow answers are what disappeared.

### What the cache did do

Watching CPU during the runs shows where the work went:

| | Cache off | Cache on |
| --- | --- | --- |
| Application | ~381% | ~365% |
| Postgres | ~213% | 0% |
| Redis | 0% | ~62% |
| Load generator | ~234% | ~230% |

Postgres went from doing two full cores of work to doing none. Redis does the same job
for under a third of the cost.

So the cache bought two things here, and neither of them is the throughput number. It
made response times predictable, and it took the database almost entirely out of the read
path.

### What this measurement does not cover

One link was requested throughout, so the hit rate was 100%. That is the best case, and
real traffic asks for a mix of popular and unpopular links, so a real hit rate sits
somewhere below it. A miss is slower than having no cache at all, because it checks Redis
first and then still asks Postgres.

Both the database and the cache were on the same machine as the application, reachable
over the loopback interface with no network in between. Most of what a cache is normally
worth is the network trip to a database it replaces, and none of that is present here.
