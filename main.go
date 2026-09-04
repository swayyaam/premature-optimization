// Command premature-optimization is a URL shortener.
//
// Step 7: every redirect is counted, and the counting happens on a queue
// rather than during the request. See docs/07-async-queue.md.
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

	url, err := s.cache.Get(ctx, "link:"+code).Result()
	if err == nil {
		return url, true
	}
	if !errors.Is(err, redis.Nil) {
		log.Printf("cache get %q: %v", code, err)
	}
	return "", false
}

// cachePut stores a link. Failing to cache is not a reason to fail the request.
func (s *store) cachePut(ctx context.Context, code, url string) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Set(ctx, "link:"+code, url, cacheTTL).Err(); err != nil {
		log.Printf("cache set %q: %v", code, err)
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

	srv := &http.Server{Addr: addr, Handler: mux}

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
