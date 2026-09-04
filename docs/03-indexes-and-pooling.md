# Step 3: Indexes and connection pooling

**Code:** [`main.go`](../main.go), [`sql/schema.sql`](../sql/schema.sql).
**Load test output:** [`benchmarks/03-index-only.txt`](../benchmarks/03-index-only.txt),
[`benchmarks/03-index-and-pooling.txt`](../benchmarks/03-index-and-pooling.txt),
[`benchmarks/03-pool-settings.txt`](../benchmarks/03-pool-settings.txt).

## What we are building

Two changes, measured separately so it is clear what each one did.

1. A unique index on the `code` column.
2. Connection pool settings on the `sql.DB`.

The endpoints do not change. The measurement to beat is 395 requests per second with a
99th percentile of 309 ms, recorded in
[`benchmarks/02-postgres.txt`](../benchmarks/02-postgres.txt).

---

## The index

The lookup every redirect performs is this:

```sql
SELECT url FROM links WHERE code = 'seed250000';
```

Asking Postgres to explain how it runs that gives the reason the previous measurement was
slow:

```
Parallel Seq Scan on links  (actual time=14.798..15.181 rows=0.33 loops=3)
  Filter: (code = 'seed250000'::text)
  Rows Removed by Filter: 166667
  Buffers: shared hit=5154
```

`Seq Scan` is Postgres reading the table from the first row to the last. With no index
there is no other way to find one row, so it compared all 500,000 of them and threw away
everything that did not match. It read 5,154 pages of the table to return 31 bytes.

An index is a second structure, kept alongside the table, that holds the indexed column
in sorted order with a pointer back to the row. Looking something up in it is a
descent through a tree rather than a walk through everything, which is the same reason a
`HashMap` beats scanning an `ArrayList`.

```sql
CREATE UNIQUE INDEX links_code_key ON links (code);
```

The same query afterwards:

```
Index Scan using links_code_key on links  (actual time=0.025..0.026 rows=1.00 loops=1)
  Index Cond: (code = 'seed250000'::text)
  Buffers: shared hit=1 read=3
```

Four pages instead of 5,154, and 0.026 ms instead of 19.9 ms. The work stopped scaling
with the size of the table.

An index is not free. This one takes 15 MB next to the 40 MB table, and every insert now
has to update the index as well as the table, so writes get slightly slower in exchange
for reads getting enormously faster. That trade is worth it here because a shortener
reads far more often than it writes.

### Why unique

A plain index would give the same lookup speed. `UNIQUE` also tells Postgres that two
rows may never share a code, and it enforces that. The database becomes the thing that
decides whether a code is taken, which changes the insert.

---

## Letting the database enforce uniqueness

The previous version asked whether a code existed and then inserted it:

```go
var exists bool
err := s.db.QueryRow("SELECT EXISTS (SELECT 1 FROM links WHERE code = $1)", code).Scan(&exists)
// ...then insert
```

Two round trips, and a gap between them. Two requests could each ask about the same code,
each be told it was free, and each insert it.

With the unique index, the insert can simply be attempted:

```go
res, err := s.db.Exec(
	"INSERT INTO links (code, url) VALUES ($1, $2) ON CONFLICT (code) DO NOTHING",
	code, url,
)
if err != nil {
	return "", err
}

rows, err := res.RowsAffected()
if err != nil {
	return "", err
}
if rows == 1 {
	return code, nil
}
```

`ON CONFLICT (code) DO NOTHING` tells Postgres that an insert violating that unique index
is not an error. It quietly inserts nothing instead.

`RowsAffected` on the result says which happened. One row means the code was free and is
now taken. Zero rows means something else had it, and the loop generates another code and
tries again.

This is one round trip instead of two, and it is correct no matter how many requests
arrive at once, because the uniqueness check and the insert are now the same operation.
Two requests generating the same code is decided by the database, and exactly one of them
wins.

---

## The connection pool

`sql.DB` is a pool. Its behaviour comes from three settings, and until now all three were
left alone:

```go
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(25)
db.SetConnMaxLifetime(time.Hour)
```

- `SetMaxOpenConns` caps how many connections may exist at once. The default is
  unlimited.
- `SetMaxIdleConns` says how many may sit unused, ready to be picked up again. The
  default is 2.
- `SetConnMaxLifetime` closes and replaces a connection after a while, so that a
  long-lived process does not hold connections to a database that has since been
  restarted or moved.

`time.Hour` is a `time.Duration`, which is a count of nanoseconds with a named type on
it. Durations are written by multiplying the units, as in `5 * time.Minute`, and they are
what every Go function taking a timeout accepts.

### Why the defaults hurt

A Postgres connection is expensive. The server starts a separate operating system process
for each one, and the client has to do a network handshake and authenticate before any
query can run.

The default combination is unlimited connections with only two kept idle. With fifty
requests arriving at once, the pool opens connections freely, and then when a request
finishes and more than two connections are idle, the extra ones are closed. Under
sustained load the server spends its time building and tearing down connections.

Capping the open connections stops that. Once the ceiling is reached, a request waits for
a connection instead of opening its own, and a connection that comes free is handed
straight to whoever is waiting. It is never idle, so it is never closed.

### Which setting mattered

Measured rather than assumed. Four combinations, ten seconds each, fifty concurrent, all
with the index in place:

| Settings | req/s | 99th percentile |
| --- | --- | --- |
| Both set to 25 | 41,800 | 4.8 ms |
| `MaxOpenConns(25)`, idle left at 2 | 41,022 | 5.9 ms |
| `MaxIdleConns(25)`, open left unlimited | 37,405 | 15.9 ms |
| Both left at Go's defaults | 13,565 | 32.4 ms |

Either setting on its own recovers most of the gap, which says the problem was
connections being discarded and rebuilt, not the number of them. Setting both is the best
of the four and is what the code does.

The number 25 is not special. Anything from 10 upward measured within a few percent of
it. What matters is that a ceiling exists and that idle connections are kept.

### One thing seen once

The first run with the index and the default pool returned 945 responses with status 500,
and the server log said:

```
dial tcp 127.0.0.1:5432: connect: can't assign requested address
```

That is the machine running out of ephemeral ports. Every closed connection leaves a
socket in `TIME_WAIT` for around thirty seconds, and closing them faster than they expire
eventually leaves no port numbers free.

It did not happen again once the earlier runs were given thirty seconds to drain, so it
belongs here as something observed and not as a reliable property of the configuration.
It is recorded in the header of
[`benchmarks/03-index-only.txt`](../benchmarks/03-index-only.txt) along with the clean
run that replaced it.

---

## Applying the change

On a database that already exists:

```bash
psql -d shortener -c "CREATE UNIQUE INDEX links_code_key ON links (code);"
```

Building the index took under a second on 500,000 rows. From scratch,
[`sql/schema.sql`](../sql/schema.sql) now creates it along with the table.

## Try it

```bash
go run .
```

```bash
curl -X POST localhost:8080/shorten -d '{"url":"https://go.dev"}'
curl -i localhost:8080/<code-from-above>
```

```bash
psql -d shortener -c "explain analyze select url from links where code = 'seed250000';"
```

---

## Load test results

Twenty seconds, fifty concurrent, redirects for a link that exists. The same command
every time.

| | Postgres, no index | Index added | Index and pool |
| --- | --- | --- | --- |
| Requests per second | 395 | 9,584 | 41,913 |
| Median | 87 ms | 0.3 ms | 0.9 ms |
| 99th percentile | 309 ms | 40.9 ms | 5.1 ms |
| Slowest | 432 ms | 111 ms | 189 ms |

The index multiplied throughput by 24. Configuring the pool multiplied it by another 4.4.
Together they are 106 times the starting point.

The two changes are worth separating because they are different kinds of problem. The
index was the database doing far more work than it needed to. The pool was the
application creating and destroying an expensive resource in a loop, and the database was
never the thing at fault.

Both were invisible from the outside. The application returned correct answers throughout
and looked instant when clicked by hand.

For scale, the in-memory version measured 89,930 requests per second. This version keeps
its data on restart, can be shared by more than one copy of the application, and reaches
just under half that.
