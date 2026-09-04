# Step 6: Several instances behind nginx

**Code:** [`deploy/nginx.conf`](../deploy/nginx.conf),
[`scripts/cluster.sh`](../scripts/cluster.sh), [`main.go`](../main.go).
**Load test output:**
[`benchmarks/06-nginx-3-instances.txt`](../benchmarks/06-nginx-3-instances.txt),
[`benchmarks/06-direct-one-instance.txt`](../benchmarks/06-direct-one-instance.txt),
[`benchmarks/06-failover.txt`](../benchmarks/06-failover.txt).

## What we are building

Three copies of the application running at the same time, on ports 8081, 8082 and 8083,
with nginx on port 8080 handing each incoming request to one of them in turn.

## Why this works

Running three copies only makes sense if they agree about the data. This one does,
because the application keeps nothing of its own. Every link is in Postgres and every
cached copy is in Redis, and all three instances are talking to the same Postgres and the
same Redis.

That is checkable. Create a link through nginx, which sends it to one instance, then ask
each instance for it directly:

```
create: {"code":"hh5ext7", ...}
  instance 8081: 302
  instance 8082: 302
  instance 8083: 302
```

Two of those instances had never heard of that link and answered correctly anyway.

The one code change this needed was the listen address:

```go
addr := os.Getenv("ADDR")
if addr == "" {
	addr = defaultAddr
}
```

Three copies of a program cannot all listen on port 8080, so the port comes from the
environment. `ADDR=":8082" ./shortener` runs a second copy.

---

## The nginx configuration

The whole thing is in [`deploy/nginx.conf`](../deploy/nginx.conf). Four parts matter.

### The list of instances

```nginx
upstream shortener {
    server 127.0.0.1:8081;
    server 127.0.0.1:8082;
    server 127.0.0.1:8083;

    keepalive 64;
}
```

`upstream` names a group of servers. With no other instruction nginx works through them
in turn, one request each.

### Keeping connections to the instances open

`keepalive 64` tells nginx to hold sixty four connections to the instances open and reuse
them. Without it, nginx opens a connection to an instance, sends one request, and closes
it again, which is the same expensive pattern as opening a database connection per query.

It needs two more lines to do anything:

```nginx
proxy_http_version 1.1;
proxy_set_header Connection "";
```

nginx talks to upstreams with HTTP/1.0 by default, which has no persistent connections at
all, and the incoming `Connection` header has to be cleared so it is not passed along to
the instance.

### The Host header

```nginx
proxy_set_header Host $http_host;
```

The application builds `short_url` out of the `Host` header it received. Without this
line, the instance sees nginx's internal upstream name instead of what the client asked
for.

`$http_host` rather than `$host` is deliberate. `$host` drops the port number, and the
first version of this configuration used it and produced links like
`http://localhost/dfbf43p`, missing the `:8080` that makes them work.

### Logging

```nginx
log_format upstreams '$status $request_uri -> $upstream_addr';
access_log off;
```

`$upstream_addr` is which instance served a request, which is how to confirm the load
actually spread:

```
   7 127.0.0.1:8081
   4 127.0.0.1:8082
   3 127.0.0.1:8083
```

It is off by default because writing one line per request is real work. At forty thousand
requests a second, a load test with the access log on is partly a measurement of the
access log.

---

## Running it

```bash
brew install nginx
scripts/cluster.sh start
```

That builds the binary, starts three instances, waits for each to answer on `/healthz`,
and starts nginx in front of them. Everything it writes goes in `run/`, which is not
committed.

```bash
curl -X POST localhost:8080/shorten -d '{"url":"https://go.dev"}'
curl -i localhost:8080/<code-from-above>
```

```bash
scripts/cluster.sh stop
```

---

## Load test results

Five runs of ten seconds at fifty concurrent, on the machine that was already running the
load generator, Postgres and Redis.

| | One instance, direct | Three instances via nginx |
| --- | --- | --- |
| Requests per second | 43,063 | 36,987 |
| 99th percentile | 1.6 ms | 2.1 ms |

Three instances are 14% slower than one.

Nothing went wrong. The machine had no spare capacity to hand out, so adding copies of
the application did not add any, and nginx has to be paid for. Watching CPU during both
runs shows the bill:

| | Direct | Via nginx |
| --- | --- | --- |
| Application instances | ~330% | ~314% |
| nginx | 0% | ~133% |
| Redis | ~48% | ~41% |
| Load generator | ~221% | ~186% |
| Total | ~600% | ~673% |

nginx costs about 1.3 cores, and every request now travels over two TCP connections
instead of one, is parsed twice and written twice. More total CPU is being spent to serve
fewer requests.

This is what horizontal scaling costs when the thing being shared out is one machine.
Three instances on three machines would have three times the CPU. Three instances on one
machine have the same CPU, split three ways, with a proxy taking a slice.

## What it buys

Killing an instance while traffic is flowing, twenty seconds of load with instance 8082
killed seven seconds in:

```
Status code distribution:
  [302]	746920 responses
```

746,920 requests, every one of them answered. No errors, and throughput over the whole
run was 37,344 requests per second, which is the same as the run where nothing was
killed.

A request that nginx sends to an instance which has gone away fails at nginx, and nginx
sends it to the next instance in the list instead. The client is never told. One third of
the capacity disappeared without a single person noticing.

That is what the 14% pays for.
