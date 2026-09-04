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

Needs Go 1.22 or newer (written on 1.26) and a running Postgres.

```bash
createdb shortener
psql -d shortener -f sql/schema.sql
psql -d shortener -f sql/seed.sql
go run .
```

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

## The steps

| # | Subject | Document | Status |
| --- | --- | --- | --- |
| 00 | Overview: the goal, the plan, how to read this | [00-overview.md](docs/00-overview.md) | done |
| 01 | One file, storage in memory, no framework | [01-minimal-api.md](docs/01-minimal-api.md) | done |
| 02 | Postgres, with no tuning | [02-postgres.md](docs/02-postgres.md) | done |
| 03 | Indexes and connection pooling | | planned |
| 04 | Load testing with `hey`, and a repeatable method | | planned |
| 05 | Caching popular links in Redis | | planned |
| 06 | Several copies of the app behind nginx | | planned |
| 07 | A queue for slow work | | planned |
| 08 | Rate limits, timeouts, and backpressure | | planned |
| 09 | Structured logging and metrics | | planned |
| 10 | Read replicas, and sharding if the numbers call for it | | planned |

[docs/go-glossary.md](docs/go-glossary.md) lists every Go term in the repo, in the order
it first came up.

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
