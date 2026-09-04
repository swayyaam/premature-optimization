# Step 4: A repeatable load test

**Code:** [`scripts/bench.sh`](../scripts/bench.sh).
**Load test output:**
[`benchmarks/04-baseline-redirect.txt`](../benchmarks/04-baseline-redirect.txt),
[`benchmarks/04-baseline-create.txt`](../benchmarks/04-baseline-create.txt),
[`benchmarks/04-baseline-healthz.txt`](../benchmarks/04-baseline-healthz.txt).

## What we are building

No change to the application. This step turns load testing into a procedure that produces
the same answer twice, writes it into a script, and uses it to record a full baseline for
the current code.

Every number recorded so far came from a single run of `hey`. A single run cannot tell
you whether a difference you are looking at is the change you made or the machine having
a slightly different afternoon.

## What a measurement needs

### Repeat it, and quote the median

One run gives a number. Several runs give a number and an idea of how much that number
moves on its own. The script runs the same test five times and reports every result:

```
  run   req/s        p99
  1     41837        0.0059
  2     42364        0.0047
  3     42424        0.0045
  4     42511        0.0048
  5     42948        0.0039
```

The median of those is 42,424, and the spread from slowest to fastest is about 2.6% of
it. That spread is the useful part. It says a change that moves throughput by 2% has not
been shown to do anything at all, because this machine produces that much difference
while doing nothing different.

The median is quoted rather than the average because one slow run drags an average around
and the median ignores it.

### Warm up first

The first measurement of anything is the odd one out. Postgres has not filled its cache,
the connection pool has not opened its connections, and the operating system has not
settled. The script sends three seconds of traffic and throws the results away before it
starts recording.

Three seconds turned out to be enough for reads and not quite enough for writes. In the
write test below, the first recorded run came out 6% slower than the other four, which
were within 2% of each other. The median is unaffected, which is part of why the median
is the number being quoted.

### Wait between runs

Every closed TCP connection leaves a socket in a state called `TIME_WAIT` for around
thirty seconds, holding onto its port number. Load tests close a lot of connections, so
running one immediately after another means the second one starts with a shrinking supply
of ports.

This is not theoretical. An earlier measurement, taken with no pause after the previous
one, returned 945 failed requests with `can't assign requested address`, and the same
configuration produced no failures at all once it was given time to drain. The script
waits thirty seconds between runs.

### Know the tool's limits

`hey` follows redirects by default. Every test here is against an endpoint that returns a
302, so without `-disable-redirects` the tool measures whatever the redirect points at
rather than the server. The first attempt at measuring this application chased every
response out to `example.com` and reported TLS handshake failures.

`hey` also stops recording individual results after 1,000,000 of them, a limit named
`maxRes` in its source. Past that point the histogram and the percentiles stop growing
while `Requests/sec` keeps counting, so a fast enough server for long enough produces
percentiles drawn from part of the run. Ten second runs at these speeds stay under the
limit.

### Record where the number came from

A number with no context cannot be compared to anything later. Each file the script
writes starts with the commit, the date, the machine, the Go and Postgres versions, the
number of rows in the table, and the exact command. It also notes when the working tree
had uncommitted changes, because a measurement of code that was never committed cannot be
reproduced.

### Measure something that does no work

The most useful single number is the one for `GET /healthz`, which returns `ok` and
touches nothing. It is the fastest this server can possibly answer anything on this
machine, so it is the ceiling that every other endpoint is measured against.

### Write tests change what they measure

Running the create endpoint under load inserts rows. The write test below took the table
from 500,003 rows to 2,306,273, which is a different table from the one the read test
ran against.

So the row count goes in the header, and the rows the test created are deleted afterwards
so the next measurement starts from the same place:

```sql
DELETE FROM links WHERE url = 'https://example.com/loadtest';
```

The test writes one known URL for exactly this reason, which makes the cleanup precise
rather than a guess about which rows were test data.

---

## The script

```bash
scripts/bench.sh <output-name> <label> [hey arguments...]
```

```bash
scripts/bench.sh 04-baseline-redirect "Baseline: GET /{code}" http://localhost:8080/seed250000
```

It checks the server is answering, warms up, runs the test five times with pauses in
between, and writes `benchmarks/<output-name>.txt` containing the summary and the raw
output of every run.

The defaults are overridable, so a longer or heavier test does not need the script
edited:

```bash
REPS=10 DURATION=30s CONCURRENCY=200 scripts/bench.sh ...
```

There is no Go in this step. `hey` does the work and the script only arranges for it to
happen the same way each time.

---

## The baseline

Current code, five runs of ten seconds each at fifty concurrent requests.

| Endpoint | Median req/s | p99 | Spread across runs |
| --- | --- | --- | --- |
| `GET /healthz`, no database | 89,866 | 1.0 ms | 0.2% |
| `GET /{code}`, a link that exists | 42,424 | 4.7 ms | 2.6% |
| `POST /shorten`, creating a link | 34,366 | 6.5 ms | 6.1% |

Three things come out of this.

**The ceiling is about 89,900 requests per second.** That is this Go server, on this
machine, answering with a fixed string. No amount of work on the database moves that
number, because it does not involve the database.

**A redirect costs roughly half the machine.** 42,424 against a ceiling of 89,866 means
the Postgres round trip and everything around it takes about as long as the entire rest
of the request. That is the budget for anything that would make reads faster.

**Writes are close behind reads.** 34,366 against 42,424 is a smaller gap than expected
for an operation that inserts a row and updates an index. The write test also grew the
table from 500,003 rows to 2,306,273 while it ran, and throughput across runs two to five
stayed flat at 34,253 to 34,888. Quadrupling the table did not slow inserts down in any
way this test can detect.

The `healthz` measurement is worth noting for how still it is. Five runs inside 0.2% of
each other, because nothing in that path waits for anything. The read and write paths
move around more, and that difference is itself information: variance goes up when
something is contended.

---

## Running it yourself

```bash
go run .
```

In another terminal:

```bash
scripts/bench.sh my-test "what I am testing" http://localhost:8080/seed250000
```

For the write path, and remembering to clean up afterwards:

```bash
scripts/bench.sh my-write-test "creating links" \
    -m POST -d '{"url":"https://example.com/loadtest"}' http://localhost:8080/shorten
psql -d shortener -c "DELETE FROM links WHERE url = 'https://example.com/loadtest';"
```
