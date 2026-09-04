# Step 7: Counting clicks on a queue

**Code:** [`clicks.go`](../clicks.go), [`main.go`](../main.go),
[`sql/schema.sql`](../sql/schema.sql).
**Load test output:** [`benchmarks/07-clicks-off.txt`](../benchmarks/07-clicks-off.txt),
[`benchmarks/07-clicks-sync.txt`](../benchmarks/07-clicks-sync.txt),
[`benchmarks/07-clicks-async.txt`](../benchmarks/07-clicks-async.txt).

## What we are building

Every redirect now records that it happened, so the application can say how many times a
link was followed.

That puts a database write on the busiest endpoint in the application, which until now
did no writing at all. This step measures what that costs when the write happens during
the request, and then moves the write onto a queue so the person following the link does
not wait for it.

`CLICKS=sync` and `CLICKS=off` run the other two versions from the same binary, so all
three can be measured against each other.

## The table

```sql
CREATE TABLE IF NOT EXISTS clicks (
    code       TEXT        NOT NULL,
    clicked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

One row per redirect, and nothing reads it yet, so it has no index.

## Doing it during the request

The obvious version, and the one to measure first:

```go
func (s *syncRecorder) record(code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, "INSERT INTO clicks (code) VALUES ($1)", code); err != nil {
		log.Printf("clicks: %v", err)
	}
}
```

Throughput on the redirect endpoint goes from 43,524 requests per second to 28,342. A
third of the capacity of the service is now spent recording that the service was used.

The reason is not the size of the insert. It is that there is one insert per request, each
one its own transaction that Postgres has to commit and make durable separately, and the
redirect waits for all of it before the browser is told where to go.

Nothing about a click needs to happen before the redirect is sent. It only needs to happen.

---

## The queue

The queued version is in [`clicks.go`](../clicks.go). A handler hands the code over and
returns immediately. Separate goroutines take codes off the queue, gather them into
batches, and write each batch as one statement.

### Channels

```go
ch chan string
```

A **channel** is a pipe that carries values between goroutines, and it is the main way
concurrent Go code communicates. One goroutine sends into it, another receives from it,
and the channel takes care of the synchronisation. It is close to a `BlockingQueue` in
Java, built into the language.

```go
ch <- code       // send
code := <-ch     // receive
```

By default a channel has no room in it, and a send waits until someone is ready to
receive. A **buffered** channel has room, and a send only waits when that room is full:

```go
ch: make(chan string, queueSize)   // queueSize is 10000
```

That buffer is the queue. Ten thousand clicks can be waiting to be written before anyone
has to wait for anything.

### Starting the workers

```go
r.wg.Add(workers)
for i := 0; i < workers; i++ {
	go r.run()
}
```

`go r.run()` starts a goroutine. This is the first place in this project that starts one
directly, rather than `net/http` doing it. Four of them run at once, all receiving from
the same channel, and Go hands each queued value to exactly one of them.

`sync.WaitGroup` counts goroutines that have not finished. `Add(4)` says four are
expected, each one calls `Done` when it returns, and `Wait` blocks until the count reaches
zero. It does the job of a `CountDownLatch`.

### Waiting for whichever happens first

A worker needs to write a batch when the batch is full, and also when it has been sitting
half full for too long. `select` waits on several channel operations and runs whichever
is ready first:

```go
for {
	select {
	case code, open := <-r.ch:
		if !open {
			r.flush(batch)
			return
		}
		batch = append(batch, code)
		if len(batch) >= batchSize {
			r.flush(batch)
			batch = batch[:0]
		}
	case <-ticker.C:
		r.flush(batch)
		batch = batch[:0]
	}
}
```

`time.NewTicker` is a channel that receives a value at a fixed interval, every hundred
milliseconds here. So a batch is written when it reaches five hundred codes, or when a
tenth of a second has passed, whichever comes first. Without the ticker a quiet period
would leave a partly filled batch sitting in memory indefinitely.

`code, open := <-r.ch` is the two value receive. `open` is false once the channel has been
closed and drained, which is how a worker knows to write what it has and stop.

`batch = batch[:0]` reuses the same underlying array instead of allocating a new slice
every time.

### Never making the redirect wait

```go
func (r *recorder) record(code string) {
	select {
	case r.ch <- code:
	default:
		r.dropped.Add(1)
	}
}
```

A `select` with a `default` case does not wait. If the send can happen right now it
happens, and otherwise `default` runs immediately.

So when the queue is full, the click is counted and thrown away. That is a decision, and
it is the important one in this step. The alternative is to let the send block, which
would mean a slow database making every redirect slow, which is the problem this step
exists to remove. A redirect is worth more than a statistic.

`atomic.Int64` is a counter that several goroutines can increment safely. A plain `int64`
would be a data race.

### One statement instead of five hundred

```go
var stmt strings.Builder
stmt.WriteString("INSERT INTO clicks (code) VALUES ")
args := make([]any, len(batch))
for i, code := range batch {
	if i > 0 {
		stmt.WriteString(",")
	}
	fmt.Fprintf(&stmt, "($%d)", i+1)
	args[i] = code
}

r.db.ExecContext(ctx, stmt.String(), args...)
```

This builds `INSERT INTO clicks (code) VALUES ($1),($2),...` up to five hundred at a time.
One statement, one transaction, one commit, instead of five hundred of each.

`strings.Builder` grows a string without making a new copy on every append, the way
`StringBuilder` does. `any` is Go's name for a value of any type. `args...` passes the
slice as separate arguments to a function that takes a variable number of them.

The codes still travel as parameters rather than being pasted into the statement, so this
is doing nothing unsafe.

### Why `record` takes no context

```go
type clickRecorder interface {
	record(code string)
	close()
}
```

Every other database call in this application takes the request's context, so the work
stops if the client goes away. This one does not, on purpose.

A queued click outlives the request that created it. The redirect has already been sent by
the time a worker gets to it, and that request's context has already been cancelled.
Passing it in would mean every click was cancelled a moment after being queued.

### Stopping without losing the queue

Ten thousand clicks can be waiting in memory, so the process cannot simply exit.

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
```

`signal.NotifyContext` gives back a context that is cancelled when the process is asked to
stop. Waiting on it is `<-ctx.Done()`.

```go
srv := &http.Server{Addr: addr, Handler: mux}

go func() {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}()

<-ctx.Done()

shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
srv.Shutdown(shutdownCtx)
clicks.close()
```

Serving moves onto its own goroutine so that `main` is free to wait for the signal.
`srv.Shutdown` stops accepting new connections and waits for requests already in progress,
returning `http.ErrServerClosed` to the goroutine still sitting in `ListenAndServe`, which
is why that specific error is not treated as a failure.

Then `clicks.close()` closes the channel and waits:

```go
func (r *recorder) close() {
	close(r.ch)
	r.wg.Wait()
	log.Printf("clicks: %d written, %d dropped", r.written.Load(), r.dropped.Load())
}
```

Closing a channel makes every remaining value still readable and then reports the channel
as closed, so each worker drains what is left, writes its final batch, and returns.

Twenty redirects sent immediately before a shutdown:

```
clicks: 25 written, 0 dropped
```

---

## Load test results

Five runs of ten seconds at fifty concurrent, on the redirect endpoint, with the clicks
table emptied before each mode.

| | Clicks off | Written during the request | Queued |
| --- | --- | --- | --- |
| Requests per second | 43,524 | 28,342 | 43,553 |
| 99th percentile | 1.5 ms | 2.9 ms | 1.5 ms |
| Clicks recorded | 0 | 1,504,822 | 2,305,635 |

Writing the click during the request costs 35% of throughput and doubles the 99th
percentile.

Queuing it costs nothing that can be measured. 43,553 against 43,524 is a difference of
0.07%, and this machine varies by 2.6% between identical runs.

The last row matters most. The queued version recorded 2.3 million
clicks, which is more than the synchronous version managed, because it was serving more
requests to record. Nothing was dropped. Doing the work later did not mean doing less of
it.

### The queue really does drop

Nothing was dropped above, so the drop path was checked separately by rebuilding with a
queue of 50 and a single worker, which is far too small for this load:

```
throughput:       42825
redirects served: 342642
clicks: 339941 written, 2701 dropped
```

339,941 plus 2,701 is 342,642, exactly the number of redirects served. Under a queue small
enough to overflow constantly, 0.8% of clicks were lost and throughput did not move. The
redirect endpoint stayed at full speed while the statistics took the damage, which is what
the `default` case was written to do.

---

## Try it

```bash
go run .
```

```bash
curl -s -o /dev/null localhost:8080/seed250000
psql -d shortener -c "select count(*) from clicks;"
```

Run it again within a tenth of a second and the count will not have moved yet.

```bash
CLICKS=sync go run .    # written during the request
CLICKS=off go run .     # not recorded
```
