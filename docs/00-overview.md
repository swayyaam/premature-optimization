# Overview

## What this is

A URL shortener written in Go, rebuilt in steps so that each step handles more traffic
than the one before it. The application stays small on purpose. The work is in finding
where it slows down, measuring it, and fixing that one thing.

The name is a reference to the usual advice that you should not optimize before you have
a problem. That advice is fine, but it means most people never practice optimizing at
all. Here the problems are created on purpose, one at a time.

## What the application does

| Method | Path | What it does |
| --- | --- | --- |
| `POST` | `/shorten` | Takes a long URL and returns a short code for it |
| `GET` | `/{code}` | Redirects to the original URL |
| `GET` | `/healthz` | Returns `ok`, so load balancers and load tools have something cheap to call |

There are no accounts, no analytics, no custom aliases, and no expiry dates. The
application stays this simple all the way through, so that when the numbers change it is
clear that the infrastructure caused it.

## Who this is for

Someone who already programs, in Java or anything similar, and has not written Go.
Functions, variables, objects, and classes are taken as given. The documents cover what
Go calls things and where it behaves differently, at the point each one is first needed.

## How this repo works

Each step is one commit and one document. You can check out any commit, run the app, and
see that step working on its own.

```bash
git log --oneline --reverse    # the list of steps
git checkout <commit>
go run .
```

Each document in `/docs` covers one step and answers the same questions:

- What we are building in this step
- The Go needed to build it
- The scaling idea behind it
- The load test results

`/docs/go-glossary.md` collects every Go term used in the repo, in the order it first
came up. It grows as the project grows.

## The plan

| Step | Subject |
| --- | --- |
| 1 | One file, storage in memory, no framework |
| 2 | Postgres, with no tuning |
| 3 | Indexes and connection pooling |
| 4 | Load testing with `hey`, and a repeatable method for it |
| 5 | Caching popular links in Redis |
| 6 | Several copies of the app behind nginx |
| 7 | A queue for slow work |
| 8 | Rate limits, timeouts, and backpressure |
| 9 | Structured logging and metrics |
| 10 | Read replicas, and sharding if the numbers call for it |

The measurement that matters is requests per second the server sustains under load, with
latency percentiles next to it. A total request count on its own does not say much.

## Layout

```
README.md              index of the steps
docs/
  00-overview.md       this file
  NN-<step>.md         one file per step
  go-glossary.md       Go terms, in the order they came up
benchmarks/
  NN-<step>.txt        raw output from each load test
scripts/
  bench.sh             runs one load test the same way every time
  cluster.sh           starts several instances behind nginx
deploy/
  nginx.conf           the load balancer configuration
go.mod                 the Go module
main.go                the application
sql/                   schema and seed data
```

## Running it

You need Go installed. `go version` should print 1.22 or newer; this was written on 1.26.

```bash
go run .
```

The server listens on `http://localhost:8080`.

## About the numbers

Load tests here run on one laptop, with the load generator on the same machine as the
server. The two compete for the same CPU and there is no network between them, so these
figures do not describe what the software would do on real hardware. They are useful for
comparing one step against the next on the same machine, which is all they are used for.

Each test runs five times and the median is quoted, with the spread across runs recorded
next to it. `scripts/bench.sh` does this, and `docs/04-load-testing.md` explains what
each part of the procedure is for.
