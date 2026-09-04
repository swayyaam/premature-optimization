# Step 10: Whether the database needs scaling

**Code:** none. Nothing was changed.
**Load test output:**
[`benchmarks/10-where-the-limit-is.txt`](../benchmarks/10-where-the-limit-is.txt).

## What this step is

The plan for this step was read replicas, and sharding if the numbers justified it. This
is the step where the numbers get looked at.

They said no, so nothing was built. What follows is the measurement and the reasoning,
because a decision not to do something is only worth anything if it is written down with
the evidence next to it.

## The question

A read replica is a second copy of the database that receives changes from the first and
answers read queries. It is worth having when the database cannot keep up with the reads
being sent to it.

So: can it?

## Everything so far was measured at one number

Every load test in this repo used fifty concurrent requests. That number was picked at the
start and never questioned, which means every conclusion drawn from it applies to fifty
concurrent requests and nothing else has been checked.

The first thing to do is vary it.

```
  conc   req/s       mean_ms    p99_ms
  25     42098       0.60       0.90
  50     42824       1.20       1.60
  100    42507       2.40       2.90
  200    42118       4.70       5.60
  400    42327       9.40       11.30
  800    41321       19.30      22.90
```

Throughput does not move. From 25 concurrent to 800, a range of 32 times, it stays at
about 42,000 requests per second. Latency rises in exact proportion instead: eight times
the concurrency, eight times the wait.

That shape is what a hard limit looks like. The service is doing all the work it can do,
and extra clients join a queue rather than getting served. Sending more traffic at it
produces slower responses and not more responses.

## Where the limit is

Two endpoints, the same sweep:

```
  conc   healthz      redirect
  50     90424        42151
  200    90131        42681
  800    98386        42120
```

`/healthz` writes a fixed string and talks to nothing at all. It is flat at about 90,000.
The redirect is flat at about 42,000.

Both are flat, so both are limits rather than queueing. The only difference between the
two code paths is that a redirect makes one round trip to a backend over a socket and
waits for the answer.

That round trip costs about 48,000 requests per second, which is more than half of
everything this machine can do.

## Which backend does not matter

If the round trip is the cost, then what is at the other end of it should not change the
answer. With the cache turned off, so that every single read is a query to Postgres:

```
  conc   req/s       p99_ms     postgres_cpu_during
  50     42039       5.10       227%
  200    42060       20.60      224%
  800    42012       85.90      222%
```

The same 42,000.

Serving every read from Redis and serving every read from Postgres produce the same
throughput. And Postgres was using about 225% of CPU on a machine with ten cores, sampled
while the tests were running, unchanged whether it was being asked for 42,000 reads a
second at 50 concurrent or at 800. It had plenty of capacity left and was still not asked
for more, because the application had nothing more to give it.

## The answer

The database is not the limit and has not been since the index was added in step 3.

A read replica gives you somewhere else to send queries. That helps when the database is
the thing that cannot keep up. Here the database is idle when the cache is warm, and
unsaturated with room to spare when the cache is off, and the ceiling is identical in both
cases. Adding a replica would put a second unsaturated database behind an application that
cannot fill the first one.

Sharding is further away still. Sharding splits the data so that no single database holds
all of it, which is worth the very large amount of complexity it costs when one machine
cannot hold the data or keep up with the writes. This table is 40 MB, and writes measured
34,366 a second at the point they were last measured, against a database doing nothing.

So neither was built.

## What would change the answer

Written down so the decision can be revisited against evidence rather than reconsidered
from scratch.

A replica starts to make sense when the database is the thing that runs out first. On
these measurements that would need the application to stop being the limit, which on one
machine means the round trip cost has to come down. Running the application and the
database on separate machines would change the shape of this entirely, because then the
round trip is a real network hop and the application is no longer competing with the
database and the load generator for the same ten cores.

That is the honest limit of everything measured in this repo. One machine was always going
to find a one machine answer.

Sharding needs a reason that is about the data rather than the requests. A table too large
for one machine's disk, or a write rate one machine cannot commit. Neither is close.

## The thing this project was named after

The full quotation is that premature optimization is the root of all evil, and the part
usually left off is that it is premature until you have measured.

Nine steps of this repo measured something and then changed it. The index in step 3 was
worth 24 times the throughput. The connection pool was worth another four. Caching in step
5 was worth nothing measurable on throughput and cut the 99th percentile by nearly four
times, which only became clear because it was measured rather than assumed. Three
instances behind nginx in step 6 came out 14% slower, and were kept anyway for a reason
that had nothing to do with speed.

This step measured something and did not change it. That is the same activity, and it is
the one the name was pointing at.

## Running these yourself

```bash
MAX_IN_FLIGHT=8192 go run .
```

```bash
for c in 25 50 100 200 400 800; do
    hey -z 10s -c $c -disable-redirects http://localhost:8080/seed250000 \
        | grep -E 'Requests/sec|99%'
    sleep 20
done
```

The same loop against `http://localhost:8080/healthz`, and again with the server started
as `REDIS_URL= MAX_IN_FLIGHT=8192 go run .` to take the cache out.
