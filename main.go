// Command premature-optimization is a URL shortener.
//
// Step 1: everything lives in this one file and every short link lives in memory.
// See docs/01-minimal-api.md.
package main

import (
	"encoding/json"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
)

const (
	addr       = ":8080"
	codeLength = 7
	alphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// store holds every short code and the URL it points at.
//
// The map is guarded by a mutex because net/http runs every request in its own
// goroutine, so several requests can touch this map at the same instant.
type store struct {
	mu   sync.RWMutex
	urls map[string]string
}

func newStore() *store {
	return &store{urls: make(map[string]string)}
}

// save picks an unused short code, records the URL under it, and returns the code.
func (s *store) save(url string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var code string
	for {
		code = randomCode()
		if _, taken := s.urls[code]; !taken {
			break
		}
	}

	s.urls[code] = url
	return code
}

// find returns the URL stored under code, and whether there was one.
func (s *store) find(code string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	url, ok := s.urls[code]
	return url, ok
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

// handleShorten builds the POST /shorten handler, closing over the store it writes to.
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

		code := s.save(req.URL)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(shortenResponse{
			Code:     code,
			ShortURL: "http://" + r.Host + "/" + code,
		})
	}
}

// handleRedirect builds the GET /{code} handler, closing over the store it reads from.
func handleRedirect(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		url, ok := s.find(r.PathValue("code"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok\n"))
}

func main() {
	s := newStore()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /shorten", handleShorten(s))
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /{code}", handleRedirect(s))

	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
