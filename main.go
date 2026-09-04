// Command premature-optimization is a URL shortener.
//
// Step 8: timeouts, a limit on requests in progress, and an optional rate
// limit, so that one slow dependency cannot take the service down.
// See docs/08-backpressure.md.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

const (
	defaultAddr     = ":8080"
	codeLength      = 7
	alphabet        = "abcdefghijklmnopqrstuvwxyz0123456789"
	defaultDSN      = "postgres://localhost:5432/shortener?sslmode=disable"
	defaultRedisURL = "redis://localhost:6379/0"
	maxConns        = 25
	cacheTTL        = time.Hour
)

// store reads and writes short links.
//
// cache is nil when no cache is configured, and every read then goes to
// Postgres. A cache that returns an error is treated as a miss, so the read
// falls through to Postgres rather than failing.
type store struct {
	db    *sql.DB
	cache *redis.Client

	// When the cache is broken every request fails the same way, so the
	// message is printed at most once a second. Logging at the rate requests
	// arrive is its own kind of outage.
	lastCacheLog atomic.Int64
}

// logCacheError prints at most one cache message per second.
func (s *store) logCacheError(code string, err error) {
	now := time.Now().UnixNano()
	last := s.lastCacheLog.Load()
	if now-last < int64(time.Second) {
		return
	}
	if s.lastCacheLog.CompareAndSwap(last, now) {
		log.Printf("cache %q: %v", code, err)
	}
}

// save inserts the URL under a new short code and returns the code.
func (s *store) save(ctx context.Context, url string) (string, error) {
	for {
		code := randomCode()

		res, err := s.db.ExecContext(ctx,
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
	}
}

// find returns the URL stored under code, looking in the cache first.
// A missing code comes back as sql.ErrNoRows.
func (s *store) find(ctx context.Context, code string) (string, error) {
	if url, ok := s.cacheGet(ctx, code); ok {
		return url, nil
	}

	var url string
	err := s.db.QueryRowContext(ctx, "SELECT url FROM links WHERE code = $1", code).Scan(&url)
	if err != nil {
		return "", err
	}

	s.cachePut(ctx, code, url)
	return url, nil
}

// cacheGet reports whether the cache held this code. A missing cache, and a
// cache that answers with an error, both report a miss so the caller falls
// through to Postgres.
func (s *store) cacheGet(ctx context.Context, code string) (string, bool) {
	if s.cache == nil {
		return "", false
	}

	ctx, cancel := context.WithTimeout(ctx, cacheTimeout)
	defer cancel()

	url, err := s.cache.Get(ctx, "link:"+code).Result()
	if err == nil {
		return url, true
	}
	if !errors.Is(err, redis.Nil) {
		s.logCacheError(code, err)
	}
	return "", false
}

// cachePut stores a link. Failing to cache is not a reason to fail the request.
func (s *store) cachePut(ctx context.Context, code, url string) {
	if s.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cacheTimeout)
	defer cancel()

	if err := s.cache.Set(ctx, "link:"+code, url, cacheTTL).Err(); err != nil {
		s.logCacheError(code, err)
	}
}

func randomCode() string {
	b := make([]byte, codeLength)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

type shortenRequest struct {
	URL string `json:"url"`
}

type shortenResponse struct {
	Code     string `json:"code"`
	ShortURL string `json:"short_url"`
}

func handleShorten(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req shortenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `body must be JSON: {"url": "https://example.com"}`, http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}

		code, err := s.save(r.Context(), req.URL)
		if err != nil {
			log.Printf("save %q: %v", req.URL, err)
			http.Error(w, "could not save the link", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(shortenResponse{
			Code:     code,
			ShortURL: "http://" + r.Host + "/" + code,
		})
	}
}

func handleRedirect(s *store, clicks clickRecorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")

		url, err := s.find(r.Context(), code)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			log.Printf("find %q: %v", code, err)
			http.Error(w, "could not look up the link", http.StatusInternalServerError)
			return
		}

		clicks.record(code)
		http.Redirect(w, r, url, http.StatusFound)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok\n"))
}

// openCache returns nil when REDIS_URL is set to an empty string, which runs the
// application with no cache at all.
func openCache(ctx context.Context) *redis.Client {
	url, set := os.LookupEnv("REDIS_URL")
	if !set {
		url = defaultRedisURL
	}
	if url == "" {
		log.Print("no cache configured, every read will go to Postgres")
		return nil
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		log.Fatalf("bad cache settings: %v", err)
	}
	opts.PoolSize = maxConns

	// A cache is only worth having while it is fast. These make the client give
	// up quickly instead of holding a request while Redis is not answering.
	// Without PoolTimeout a request waits for a free connection long after the
	// deadline on its own call has passed. MaxRetries of -1 disables retries,
	// which in this library is what -1 means and not what 0 means: 0 leaves the
	// default of three, so a failing cache would be asked four times.
	opts.ReadTimeout = cacheTimeout
	opts.WriteTimeout = cacheTimeout
	opts.PoolTimeout = cacheTimeout
	opts.MaxRetries = -1

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatalf("cannot reach the cache: %v", err)
	}
	return client
}

func main() {
	// ctx is cancelled the first time the process is asked to stop.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("bad database settings: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("cannot reach the database: %v", err)
	}

	s := &store{db: db, cache: openCache(ctx)}
	clicks := newClickRecorder(db)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /shorten", handleShorten(s))
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /{code}", handleRedirect(s, clicks))

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: withLimits(mux),

		// Without these a connection can be held open indefinitely by a client
		// that never finishes sending, or never reads what it asked for.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Serving happens on its own goroutine so main can wait for the signal.
	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	stop()
	log.Print("stopping")

	// Let requests already in progress finish, then write whatever clicks are
	// still queued.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	clicks.close()
}
