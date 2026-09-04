# premature-optimization

A URL shortener written in Go, rebuilt in steps so that each version handles more traffic
than the one before it.

The application stays small on purpose: create a short link, follow a short link. The
work is in finding where it slows down, measuring it, and fixing that one thing. Each
step is one commit and one document, so you can check out any commit, run the app, and
see that step on its own.

Written for someone who already programs and has not written Go. Each Go feature is
covered where it first appears, in terms of how it differs from a language like Java.

## Start here

[docs/00-overview.md](docs/00-overview.md) covers the goal, the plan, and how the repo is
organized.

## Running it

Needs Go 1.22 or newer (written on 1.26), a running Postgres, and a running Redis.

```bash
createdb shortener
psql -d shortener -f sql/schema.sql
psql -d shortener -f sql/seed.sql
go run .
```

`REDIS_URL=` runs it with no cache.

```bash
curl -X POST localhost:8080/shorten -d '{"url":"https://go.dev"}'
```

```bash
curl -i localhost:8080/<code-from-above>
```

## The endpoints

| Method | Path | What it does |
| --- | --- | --- |
| `POST` | `/shorten` | `{"url":"..."}` becomes `{"code":"...","short_url":"..."}` |
| `GET` | `/{code}` | 302 redirect to the original URL |
| `GET` | `/healthz` | Returns `ok` |
| `GET` | `/metrics` | Counters and histograms for Prometheus |

## The steps

| # | Subject | Document | Status |
| --- | --- | --- | --- |
| 00 | Overview: the goal, the plan, how to read this | [00-overview.md](docs/00-overview.md) | done |
| 01 | One file, storage in memory, no framework | [01-minimal-api.md](docs/01-minimal-api.md) | done |
| 02 | Postgres, with no tuning | [02-postgres.md](docs/02-postgres.md) | done |
| 03 | Indexes and connection pooling | [03-indexes-and-pooling.md](docs/03-indexes-and-pooling.md) | done |
| 04 | Load testing with `hey`, and a repeatable method | [04-load-testing.md](docs/04-load-testing.md) | done |
| 05 | Caching popular links in Redis | [05-caching.md](docs/05-caching.md) | done |
| 06 | Several copies of the app behind nginx | [06-horizontal-scaling.md](docs/06-horizontal-scaling.md) | done |
| 07 | A queue for slow work | [07-async-queue.md](docs/07-async-queue.md) | done |
| 08 | Rate limits, timeouts, and backpressure | [08-backpressure.md](docs/08-backpressure.md) | done |
| 09 | Structured logging and metrics | [09-observability.md](docs/09-observability.md) | done |
| 10 | Whether the database needs scaling | [10-database-scaling.md](docs/10-database-scaling.md) | done |
| 11 | The limit of one machine, and what is past it | [11-the-limit-of-one-machine.md](docs/11-the-limit-of-one-machine.md) | done |

[docs/go-glossary.md](docs/go-glossary.md) lists every Go term in the repo, in the order
it first came up.

## Where it ended up

The service the last commit builds keeps links in Postgres with an index on the short
code, reads them through Redis, counts every redirect on an in memory queue that is
written to Postgres in batches, gives up on the cache after fifty milliseconds, refuses
work past a set number of requests in progress, and publishes counters and histograms for
Prometheus. It serves about 42,000 redirects a second at a 99th percentile of 1.6 ms, and
about 34,000 link creations a second.

### The read path, step by step

Redirects for a link that exists, the same command each time, on one laptop that was also
running the load generator, Postgres and Redis.

| After | Requests per second | 99th percentile |
| --- | --- | --- |
| A map in memory | 89,930 | 1.1 ms |
| Postgres, no index | 395 | 309 ms |
| An index on the code column | 9,584 | 40.9 ms |
| A configured connection pool | 41,913 | 5.1 ms |
| Redis in front of it | 43,021 | 1.5 ms |
| Timeouts, limits, metrics and logs | 42,344 | 1.6 ms |

### What each step was worth

| Step | The change | What it did |
| --- | --- | --- |
| 2 | Links move to Postgres | 89,930 down to 395, and they survive a restart |
| 3 | Index on the code column | 395 up to 9,584 |
| 3 | Connection pool configured | 9,584 up to 41,913 |
| 5 | Redis cache in front of reads | Throughput unchanged. 99th percentile 5.6 ms down to 1.5 ms |
| 6 | Three instances behind nginx | 43,063 down to 36,987, and an instance can be killed under load with no failed requests |
| 7 | Click counting moved to a queue | 28,342 if the write happens in the request, 43,553 if it is queued |
| 8 | Timeouts and limits | Worst case during a cache stall, 8.10 s down to 0.21 s |
| 9 | Metrics and structured logs | 0.2%, which is inside this machine's noise |
| 10 | Nothing | The database turned out not to be the limit |

### Four results that went against expectation

- **Caching barely moved throughput.** It changed the 99th percentile by nearly four
  times and the total by less than this machine varies on its own. Postgres was already
  fast enough that there was almost no mean latency left to remove.
- **Three instances behind nginx were slower than one.** Splitting one machine three ways
  and adding a proxy costs about 1.3 cores. It was kept for what it does when an instance
  dies, not for speed.
- **The connection pool fix was not the setting it looked like.** Capping open connections
  and raising idle connections each recover most of the difference on their own, so the
  problem was connections being discarded and rebuilt rather than the size of the pool.
- **A cache stall took the whole service down.** Redis holds nothing that is not in
  Postgres, and pausing it for eight seconds stopped the service for eight seconds,
  because nothing had said how long a request was willing to wait.

### The limit

Throughput on the read path is flat at about 42,000 from 25 concurrent requests to 800.
An endpoint that talks to nothing is flat at about 90,000. The difference between the two
is one round trip to a backend, and it costs the same whether Redis or Postgres answers
it.

That is why step 10 built nothing. The database was idle when the cache was warm and had
capacity to spare when the cache was off, so a read replica would have been a second
unsaturated database behind an application that could not fill the first one.

The honest limit of all of this is that everything ran on one machine, which was always
going to produce a one machine answer. What that boundary is made of, and what would have
to change to move past it, is written up in
[docs/11-the-limit-of-one-machine.md](docs/11-the-limit-of-one-machine.md).

## Following the steps in order

```bash
git log --oneline --reverse
git checkout <commit>
go run .
```

Each commit message is the title of a step. The detail is in the matching file in
`/docs`.

## How this is put together

Go's standard library is used wherever it can do the job. A dependency gets added when
there is something specific it does that the standard library cannot, and the document
for that step says what that was.

Any claim about performance has a load test behind it, with the raw output saved in
[`/benchmarks`](benchmarks) so the numbers can be traced back to the run that produced
them.
