// Command premature-optimization is a URL shortener.
//
// Step 3: the code column is indexed and the connection pool is configured.
// See docs/03-indexes-and-pooling.md.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	addr       = ":8080"
	codeLength = 7
	alphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
	defaultDSN = "postgres://localhost:5432/shortener?sslmode=disable"
	maxConns   = 25
)

// store reads and writes short links in Postgres.
//
// There is no mutex here. sql.DB is a pool of connections and is safe to use
// from many goroutines at once.
type store struct {
	db *sql.DB
}

// save inserts the URL under a new short code and returns the code.
//
// The unique index on code decides whether a code was free, so the insert is
// tried directly and retried on the rare occasion it collides.
func (s *store) save(url string) (string, error) {
	for {
		code := randomCode()

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
	}
}

// find returns the URL stored under code. A missing code comes back as sql.ErrNoRows.
func (s *store) find(code string) (string, error) {
	var url string
	err := s.db.QueryRow("SELECT url FROM links WHERE code = $1", code).Scan(&url)
	return url, err
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

		code, err := s.save(req.URL)
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

func handleRedirect(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.PathValue("code")

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

		http.Redirect(w, r, url, http.StatusFound)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok\n"))
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("bad database settings: %v", err)
	}
	defer db.Close()

	// Left at their defaults, MaxIdleConns is 2 and MaxOpenConns is unlimited,
	// so a busy server opens and closes a connection for nearly every request.
	// Holding as many idle connections as the pool may open keeps them alive
	// and reused instead.
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.Ping(); err != nil {
		log.Fatalf("cannot reach the database: %v", err)
	}

	s := &store{db: db}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /shorten", handleShorten(s))
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /{code}", handleRedirect(s))

	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
