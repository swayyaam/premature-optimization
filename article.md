# Where a Go API Stops Getting Faster

I spent ten rounds optimizing a Go service. Then I hit a wall, and the interesting part
turned out to be working out exactly what the wall was made of.

I set out to learn how systems scale, so I built the smallest useful API I could think of,
a URL shortener with two endpoints, and rebuilt it ten times. One rule: nothing counts
without a benchmark.

I had a target number in mind at the start, the way you do when you have not yet measured
anything. I did not hit it. What I got instead was better, and this article is mostly
about the parts where I was confidently wrong.

## Benchmark setup

Everything ran on one machine, which turns out to be the most important line in this
section.

- **Machine:** Apple M5, 10 cores (4 performance, 6 efficiency), macOS 26.6
- **Versions:** Go 1.26.4, PostgreSQL 18.3, Redis 8.6.2, `hey` v0.1.5
- **Data:** 500,000 rows, a 40 MB table, and a 15 MB index once there was one
- **Load:** 50 concurrent connections unless a number says otherwise
- **Networking:** loopback only. No network card, no cable, no real hop.
- **Runs:** the first three figures are single 20 second runs. From the point where I
  stopped trusting single runs, every figure is the median of five 10 second runs, with a
  3 second warm up and 30 seconds between runs so sockets could drain.
- **Reported:** throughput in requests per second, latency as the mean and the 99th
  percentile.
- The load generator, the application, Postgres and Redis all shared those same 10 cores.

That last point is not a footnote. It is the answer to the question the article ends on.

## The two changes that did all the work

- Postgres, no index: **395 req/s**
- Add an index: **9,584**
- Add connection pooling: **41,913**

**The index.** One line:

```sql
CREATE UNIQUE INDEX links_code_key ON links (code);
```

Postgres had been reading all 500,000 rows to find one. It had been doing
this on every single request, patiently, without complaining, for as long as I had been
testing it. `EXPLAIN ANALYZE` says so in its first line of output, which I know because I
eventually ran it.

The query was correct. The code was correct. Clicking through the app by hand felt
instant. This is the trap: at one request per second, a table scan and an index lookup are
indistinguishable to a human being.

**The connection pool.** Go's `database/sql` defaults to unlimited open connections and
two idle ones. Under load that means opening and closing a Postgres connection for nearly
every request, and each one starts a process on the database server.

I knew the fix for this. The fix was obviously to raise the idle count. I was confident
enough to have written it down. Then I tested it:

- Both left at Go's defaults: **13,565 req/s**
- `MaxIdleConns(25)`, open unlimited: **37,405**
- `MaxOpenConns(25)`, idle left at 2: **41,022**
- Both set to 25: **41,800**

Either setting recovers most of the gap on its own. The problem was connections being
discarded and rebuilt, not the size of the pool. I would have published a confident and
wrong explanation, and the moral of this entire article has now arrived embarrassingly
early.

Two changes, about ten lines of code, 106 times faster. And then throughput never moved
again, no matter what I did to it.

## Everything after that did nothing

**I added a Redis cache.** 42,142 to 43,021 requests per second.

Before you get excited on my behalf: I had already run the same benchmark five times
without changing anything and watched it vary by 2.6%. A 2.1% improvement is not an
improvement. It is Tuesday.

The 99th percentile was a different story. It went from 5.6 ms to 1.5 ms and then stopped
moving entirely, reporting exactly 1.5 ms on all five runs. The arithmetic explains it: at
50 concurrent requests, throughput is concurrency divided by mean latency, and mean latency
was 1.2 ms with or without the cache. After the index, Postgres was already fast enough
that there was no mean latency left to remove. What the cache removed was the occasional
6 ms answer.

So the cache was worth keeping. It just was not worth the thing I had assumed I was buying.

**I put three instances behind nginx.** This is the part of the article where the graph
goes up.

The graph went down. 43,063 to 36,987, which is 14% worse. Three processes on one machine
share the same processors three ways, and nginx wants about 1.3 cores for itself.

I kept it anyway, because I then killed an instance in the middle of a load test and the
run finished with 746,920 requests and zero errors. Nobody noticed. That is worth 14% of
anything.

**I added a queue, timeouts, rate limiting and metrics.** Throughput after each one:
unchanged, unchanged, unchanged, unchanged.

At this point the useful question stopped being how to make it faster and became what
exactly was stopping it.

## Finding the wall

Two endpoints, swept across a 32x range of concurrency:

- 50 concurrent: `/healthz` **90,424**, redirect **42,151**
- 200 concurrent: `/healthz` **90,131**, redirect **42,681**
- 800 concurrent: `/healthz` **98,386**, redirect **42,120**

Flat. Both of them, completely flat, while latency rose in exact proportion to
concurrency. That is a ceiling and not a queue.

`/healthz` returns a fixed string and talks to nothing: about 90,000. The redirect makes
exactly one round trip to a backend: about 42,000. That single round trip costs 48,000
requests per second.

Then I turned the cache off so every read hit Postgres. Still 42,000, with Postgres using
2.25 of 10 cores and never breaking a sweat. The same ceiling whether Redis or Postgres
answers.

Which is why I never built the read replica I had planned. A replica gives you somewhere
else to send a round trip. It cannot make a round trip cheaper, and the round trip was the
whole problem.

## What the wall is made of

Four things run on this laptop during a test and all four share the same processors. Here
is the CPU each was using, sampled while the tests ran. Percentages are of a single core,
so 100% means one core fully busy.

- Application: **365%** with the cache warm, **381%** with it off
- Load generator: **230%** and **234%**
- Redis: **62%** and **0%**
- Postgres: **0%** and **213%**
- Total: **657%** and **828%**

Three things fall out of those numbers.

**The load generator is a quarter of the load.** `hey` uses about 230% of CPU to produce
this traffic, which is more than Redis and Postgres combined. I had been benchmarking the
benchmark.

**The round trip is not a network cost.** Everything talks over loopback. There is no
network card, no driver, no cable, no distance, no packet loss. What the round trip
actually costs is system calls, copying bytes across the kernel boundary, and encoding a
protocol, and all of that happens inside the application process. That is why the
application sits above 350% while the database it is querying sits at zero.

**Ten cores is a misleading number.** Four of them are performance cores and six are
efficiency cores, which are meaningfully slower. 657% spread across that mix is a lot
closer to full than it looks written down.

## Why I stopped

Getting past this needs a different shape, not different code.

1. **Put the load generator on another machine.** This recovers 230% of CPU immediately
   and makes the client side real, with connections arriving over a network card and the
   latency and loss that implies.
2. **Put Postgres and Redis on their own machines.** The application gets its whole
   processor back. Every backend call also gets slower, because loopback is tens of
   microseconds and a real hop is hundreds. Per connection throughput falls, total capacity
   rises, and only measurement settles where the balance lands.
3. **Then run several application machines behind a real load balancer.** This is where
   horizontal scaling finally does the thing everyone expects it to do. Three machines have
   three times the processors. Three processes on one machine never did.

And that is a different project, because it is a different subject.

Every question in this one could be answered by running a program and reading a number. Is
the query using the index. Is the pool churning connections. Did the cache change the mean
or the tail. Each has a definite answer that one machine produces in twenty seconds.

The questions on the other side of the wall do not work like that. How far behind is the
replica, and what does a stale read do to a user. What happens to requests in flight when
a machine is replaced. Which components can be unavailable without the others noticing.
What does the system do when the network between two parts of it is slow rather than
broken.

Those are not performance questions. They are distributed systems questions, and they come
with an apparatus: provisioning, deployment, service discovery, monitoring that collects
from several places at once. The apparatus is most of the work and none of it is about
making a Go handler faster.

## The one that nearly got me

I should mention the failure I did not see coming, because it is the one most likely to
happen to you.

I paused Redis for eight seconds under load. The service stopped for eight seconds.

Every link was in Postgres. Postgres was healthy the entire time. The code already treated
a cache error as a miss and fell through to the database. All of that was correct, and none
of it helped, because nothing in the code had said how long a request was willing to
**wait**.

Adding a 50 ms deadline to the cache call brought the worst request from 8.1 seconds down
to 5 seconds, which was confusing until I understood it: the call gave up after 50 ms
exactly as instructed, and then the request sat waiting for a connection from the Redis
pool, which had its own multi second default. A deadline on the call cannot help with time
spent before the call. Setting the client's read, write and pool timeouts got it to 0.21
seconds.

Also, in go-redis, `MaxRetries: 0` does not mean zero retries. It means the default, which
is three. You want `-1`. I would love to tell you I learned this from the documentation.

And here is the detail that should worry you most. This is what the load test summary said
about those eight seconds:

```
99% in 0.0019 secs
```

Under two milliseconds at the 99th percentile, for a run where the service spent seven of
its twenty seconds serving one percent of normal traffic. The percentiles are not lying.
During the bad seconds almost nothing was served, so there are almost no slow requests to
appear in a statistic built out of requests that finished. Your dashboard can be green
through an outage.

## What one laptop can actually tell you

It can tell you that an unindexed lookup on 500,000 rows costs a factor of 24. That
`database/sql` will churn connections until you stop it. That a cache can fix your tail and
leave your mean exactly where it was. That a dependency without a deadline is an outage
with a delayed start. That percentiles over a whole run will hide that outage completely.

None of that depends on how many machines are involved. Those are properties of the code
and they would be true on any hardware.

What it cannot tell me is what this software does on real hardware, and no measurement
taken this way ever could.

I would rather finish knowing exactly where the wall is, and what it is made of, than hit a
number I picked before I understood the problem.

---

The code, eleven write ups and every raw benchmark:
[github.com/swayyaam/premature-optimization](https://github.com/swayyaam/premature-optimization)
