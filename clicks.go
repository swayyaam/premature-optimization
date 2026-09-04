package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// How many clicks may wait to be written before new ones are dropped.
	queueSize = 10000
	// How many goroutines take clicks off the queue and write them.
	workers = 4
	// How many clicks go into one insert.
	batchSize = 500
	// How long a partly filled batch waits before being written anyway.
	flushInterval = 100 * time.Millisecond
)

// recorder writes one row per redirect, without making the redirect wait.
//
// Handlers put codes on a channel and return. Worker goroutines take them off,
// gather them into batches, and insert each batch in one statement.
type recorder struct {
	db      *sql.DB
	ch      chan string
	wg      sync.WaitGroup
	dropped atomic.Int64
	written atomic.Int64
}

func newRecorder(db *sql.DB) *recorder {
	r := &recorder{
		db: db,
		ch: make(chan string, queueSize),
	}
	r.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go r.run()
	}
	return r
}

// record queues a click. It never blocks. If the queue is full the click is
// counted and thrown away, because a redirect is worth more than a statistic.
func (r *recorder) record(code string) {
	select {
	case r.ch <- code:
		clickOps.WithLabelValues("queued").Inc()
	default:
		r.dropped.Add(1)
		clickOps.WithLabelValues("dropped").Inc()
	}
}

// run is one worker. It fills a batch until the batch is full or the ticker
// fires, whichever comes first, and writes whatever it has.
func (r *recorder) run() {
	defer r.wg.Done()

	batch := make([]string, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

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
}

// flush writes a whole batch as one statement.
func (r *recorder) flush(batch []string) {
	if len(batch) == 0 {
		return
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := r.db.ExecContext(ctx, stmt.String(), args...); err != nil {
		slog.Error("click batch failed", "count", len(batch), "error", err)
		return
	}
	r.written.Add(int64(len(batch)))
	clickOps.WithLabelValues("written").Add(float64(len(batch)))
}

// close stops the workers and waits for whatever is still queued to be written.
func (r *recorder) close() {
	close(r.ch)
	r.wg.Wait()
	slog.Info("click recorder stopped", "written", r.written.Load(), "dropped", r.dropped.Load())
}

// clickRecorder is how a handler reports that a short code was followed.
//
// record takes no context on purpose. Queued work outlives the request that
// created it, so tying it to the request's context would cancel it the moment
// the redirect was sent.
type clickRecorder interface {
	record(code string)
	close()
}

// nopRecorder records nothing.
type nopRecorder struct{}

func (nopRecorder) record(string) {}
func (nopRecorder) close()        {}

// syncRecorder writes the click during the redirect, so the person following
// the link waits for the insert.
type syncRecorder struct{ db *sql.DB }

func (s *syncRecorder) record(code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := s.db.ExecContext(ctx, "INSERT INTO clicks (code) VALUES ($1)", code); err != nil {
		slog.Error("click insert failed", "error", err)
	}
}

func (s *syncRecorder) close() {}

// newClickRecorder picks how clicks are recorded. CLICKS=sync writes them
// during the request and CLICKS=off does not write them at all. Both exist so
// the three can be measured against each other.
func newClickRecorder(db *sql.DB) clickRecorder {
	switch os.Getenv("CLICKS") {
	case "off":
		slog.Info("clicks not recorded")
		return nopRecorder{}
	case "sync":
		slog.Info("clicks written during the request")
		return &syncRecorder{db: db}
	default:
		slog.Info("clicks queued", "workers", workers, "batch_size", batchSize, "queue_size", queueSize)
		return newRecorder(db)
	}
}
