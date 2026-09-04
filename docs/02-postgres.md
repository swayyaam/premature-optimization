# Step 2: Adding Postgres

**Code:** [`main.go`](../main.go), [`sql/schema.sql`](../sql/schema.sql),
[`sql/seed.sql`](../sql/seed.sql).
**Load test output:** [`benchmarks/01-in-memory.txt`](../benchmarks/01-in-memory.txt),
[`benchmarks/02-postgres.txt`](../benchmarks/02-postgres.txt).

## What we are building

The same three endpoints, with short links kept in Postgres instead of a map in memory.
The table is created with no index, no primary key, and no constraints, and the
connection pool is left exactly as Go configures it out of the box.

This step also installs `hey`, the load testing tool used for every measurement in this
repo, and takes the first two real measurements.

## Why a database

A map lives inside the running process, so the links disappear when the process stops.
They are also invisible to any other copy of the program, which means the application can
only ever be one process on one machine.

Postgres solves both. The data outlives the program, and several copies of the
application can point at the same database and see the same links.

The point of this step is to move the storage and change nothing else. No tuning, no
index, no pool settings. Then measure it.

---

## The one dependency

Go ships a package called `database/sql`. It defines how you talk to a SQL database:
connections, queries, rows, transactions. It does not know how to speak to any particular
database, because the wire protocol for Postgres has nothing to do with the one for MySQL.

That part comes from a driver, and Go ships no drivers. This is the first thing in the
project the standard library genuinely cannot do, so it is the first dependency:

```bash
go get github.com/jackc/pgx/v5/stdlib
```

`pgx` is the actively maintained Postgres driver for Go. The `/stdlib` package is the
version of it that plugs into `database/sql`, so the code stays written against the
standard library interface.

### `go.mod` and `go.sum`

`go get` changed two files.

`go.mod` now lists what the project needs:

```
require github.com/jackc/pgx/v5 v5.10.0

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	...
)
```

The `// indirect` entries are dependencies of the driver rather than things this code
imports. That is the same distinction Maven draws between a direct and a transitive
dependency, except Go writes all of them down in one file.

`go.sum` is new. It holds a cryptographic hash of every module version used, so a later
build fails loudly if the contents of a published version ever change. Both files are
committed.

`go mod tidy` adds anything the code imports and removes anything it no longer uses. It
is worth running before every commit.

### The blank import

```go
import (
	"database/sql"
	...

	_ "github.com/jackc/pgx/v5/stdlib"
)
```

The driver is imported with `_` in front, which is the blank identifier again. It means
"load this package, and I will not call anything in it".

An import that is never referenced would normally fail the build. The underscore says the
import is there for a side effect. Every Go package can have an `init()` function that
runs automatically when the package is loaded, and the pgx driver uses its `init()` to
register itself with `database/sql` under the name `pgx`. That registration is why this
line works later:

```go
db, err := sql.Open("pgx", dsn)
```

The string `"pgx"` is looked up in a registry the driver filled in at startup. It is the
same arrangement as JDBC's old `Class.forName("org.postgresql.Driver")`, with the
underscore doing the job the reflection call used to do.

---

## The Go in this step

### `sql.DB` is a pool, not a connection

```go
db, err := sql.Open("pgx", dsn)
if err != nil {
	log.Fatalf("bad database settings: %v", err)
}
defer db.Close()

if err := db.Ping(); err != nil {
	log.Fatalf("cannot reach the database: %v", err)
}
```

`sql.Open` does not connect to anything. It parses the settings and returns a pool, and
the first real connection is made when the first query runs. This is why `Ping` is called
straight after: without it, a wrong host or a stopped database would look fine at startup
and only fail on the first request.

A `sql.DB` is safe to use from many goroutines at once, and it hands each one a
connection from the pool. That has a visible effect on the code from step 1:

```go
type store struct {
	db *sql.DB
}
```

The mutex is gone. Nothing in the program is shared mutable memory any more, so there is
nothing to guard. The coordination moved into the database, which is doing the same job
with row locks and transactions.

### Running a query that returns one row

```go
func (s *store) find(code string) (string, error) {
	var url string
	err := s.db.QueryRow("SELECT url FROM links WHERE code = $1", code).Scan(&url)
	return url, err
}
```

`QueryRow` runs a statement expected to produce a single row. `Scan` copies the columns
of that row into the variables you point it at, so `&url` is a pointer for the same
reason a JSON decode target is: `Scan` writes into your variable.

`$1` is a placeholder, and Postgres numbers them rather than using `?`. The value is sent
separately from the statement text, so the database never parses `code` as SQL. Building
the string with `+` instead is how SQL injection happens.

`Scan` is where the type conversion occurs, and it checks at runtime. Scanning a `text`
column into an `int` returns an error rather than silently doing something strange.

### `sql.ErrNoRows` and `errors.Is`

A lookup that matches nothing is not a database failure, and `database/sql` reports it as
an error anyway, using one specific value:

```go
url, err := s.find(code)
if errors.Is(err, sql.ErrNoRows) {
	http.NotFound(w, r)
	return
}
if err != nil {
	log.Printf("find %q: %v", code, err)
	http.Error(w, "could not look up the link", http.StatusInternalServerError)
	return
}
```

`errors.Is` asks whether an error is, or wraps, a particular one. Go lets an error carry
another error inside it, the way a Java exception carries a cause, and `errors.Is` walks
that chain. Comparing with `err == sql.ErrNoRows` works today and breaks the moment
anything in between wraps the error, so `errors.Is` is the form to use.

The two branches matter. A missing short link is a 404 and is not worth logging. A
database that is down is a 500, and the real error goes to the log while the client gets
a short message. Handing a database error text back to the client tells strangers about
your schema.

### Running a statement that returns no rows

```go
if _, err := s.db.Exec("INSERT INTO links (code, url) VALUES ($1, $2)", code, url); err != nil {
	return "", err
}
```

`Exec` is for statements with no result rows. It returns a `sql.Result`, which can report
the number of rows affected, discarded here with `_`.

### Settings from the environment

```go
dsn := os.Getenv("DATABASE_URL")
if dsn == "" {
	dsn = defaultDSN
}
```

`os.Getenv` returns an empty string when the variable is not set, so there is no separate
"is it present" check. The connection string is a URL:

```
postgres://localhost:5432/shortener?sslmode=disable
```

Reading it from the environment means the same binary can point at a different database
without recompiling.

---

## The load testing tool

`hey` sends many HTTP requests at once and reports how the server coped. It is written in
Go, and installing it shows how Go distributes command line programs:

```bash
go install github.com/rakyll/hey@latest
```

`go install` downloads that module, compiles it, and drops the binary in
`$(go env GOPATH)/bin`. There is no package manager involved and no runtime to install,
because a compiled Go program is one self-contained file. `@latest` picks the newest
version, and an exact version like `@v0.1.5` can be pinned instead. Add that directory to
your `PATH` if it is not already there:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

A run looks like this:

```bash
hey -z 20s -c 50 -disable-redirects http://localhost:8080/seed250000
```

- `-z 20s` keeps sending for twenty seconds. `-n 5000` would send a fixed number instead.
  A duration is easier to compare across versions with very different speeds.
- `-c 50` keeps fifty requests in flight at all times. This is the load, and it is the
  number that matters most.
- `-disable-redirects` stops `hey` from following the 302 the server returns.

That last flag is not optional here. `hey` follows redirects by default, so the first
attempt at this measurement chased every 302 out to `https://example.com` and reported
this:

```
Error distribution:
  [271]	Get "https://example.com/page/250000": remote error: tls: handshake failure
```

Not one of those requests measured anything about the shortener.

`hey` prints throughput, a histogram, and the latency spread:

```
  Requests/sec:	394.7831

  Latency distribution:
    50%% in 0.0871 secs
    99%% in 0.3085 secs
```

The percentiles are the useful part. An average hides the slow requests, and the 99th
percentile is what the unluckiest one request in a hundred experienced.

Two quirks worth knowing about. The doubled percent signs above are what `hey` actually
prints, not a typo in this document. And it stops recording individual results after
1,000,000 of them, a limit called `maxRes` in its source. Past that point the histogram, the
percentiles and the status code count stop growing, while `Requests/sec` keeps counting
everything. In the step 1 run below, the reported 1,000,000 responses is that cap, and
the true total is closer to 1,800,000.

---

## The schema

```sql
CREATE TABLE IF NOT EXISTS links (
    code       TEXT        NOT NULL,
    url        TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

A table and nothing else. No primary key, no unique constraint, no index.

The load test needs a table with enough rows to be worth searching, so
[`sql/seed.sql`](../sql/seed.sql) inserts 500,000 of them with predictable codes:

```sql
INSERT INTO links (code, url)
SELECT 'seed' || lpad(i::text, 6, '0'), 'https://example.com/page/' || i
FROM generate_series(1, 500000) AS i;
```

That gives codes from `seed000001` to `seed500000`, so the load test can ask for a link it
knows exists. The table comes to 40 MB, small enough that Postgres keeps all of it in
memory. Nothing here is waiting on a disk.

## Setting it up

```bash
createdb shortener
psql -d shortener -f sql/schema.sql
psql -d shortener -f sql/seed.sql
```

## Try it

```bash
go run .
```

```bash
curl -X POST localhost:8080/shorten -d '{"url":"https://go.dev/doc/effective_go"}'
curl -i localhost:8080/<code-from-above>
```

Stop the server, start it again, and request the same link.

```bash
psql -d shortener -c "select * from links order by created_at desc limit 5;"
```

---

## Load test results

Both versions measured on the same machine, minutes apart, with the same command. Twenty
seconds, fifty concurrent requests, all of them redirects for a link that exists.

|  | Step 1, map in memory | Step 2, Postgres |
| --- | --- | --- |
| Requests per second | 89,930 | 395 |
| Median | 0.5 ms | 87 ms |
| 95th percentile | 0.8 ms | 273 ms |
| 99th percentile | 1.1 ms | 309 ms |
| Slowest | 10 ms | 432 ms |

Throughput fell by a factor of 228, and the 99th percentile went from about one
millisecond to about a third of a second.

Some of that was expected. Step 1 answered from a hash table inside the process, and this
version sends a query over a socket to another program, waits for it, and reads the
result back. That work is real and it is the price of keeping the data.

It does not explain a factor of 228. Asking Postgres what it did with the query does:

```
Gather  (actual time=18.120..19.936 rows=1.00 loops=1)
  Workers Planned: 2
  ->  Parallel Seq Scan on links  (actual time=14.798..15.181 rows=0.33 loops=3)
        Filter: (code = 'seed250000'::text)
        Rows Removed by Filter: 166667
```

`Seq Scan` means Postgres read the table from beginning to end. There is no index on
`code`, so it had no way to find that row other than comparing all 500,000 of them.
`Rows Removed by Filter: 166667` is each of the three workers throwing away everything it
read, and `rows=1.00` at the top is the single row that survived.

So every redirect reads 500,000 rows to return one, and it puts three CPU cores to work
doing it. With fifty requests arriving at once on a ten core machine, the machine runs out
of cores long before it runs out of requests, and the queue that forms is what the 309 ms
99th percentile is measuring.

Nothing warned about this. The query is correct, the code is correct, the table is
correct, and the application behaved perfectly at the one request per second a browser
generates while you are testing by hand.
