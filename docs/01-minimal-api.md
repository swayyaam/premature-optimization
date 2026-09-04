# Step 1: The minimal API

**Code:** [`main.go`](../main.go). One file, 127 lines, standard library only.

## What we are building

A working URL shortener with three endpoints:

| Method | Path | What it does |
| --- | --- | --- |
| `POST` | `/shorten` | Reads `{"url": "..."}` and returns a short code |
| `GET` | `/{code}` | Redirects to the URL that code was created for |
| `GET` | `/healthz` | Returns `ok` |

Short links are kept in a map in the program's own memory. There is no database, no
router library, and no dependencies at all. Everything used here comes with Go.

## Why start this small

Later steps are going to claim that some change made the application faster or more
reliable. A claim like that needs something to compare against, and that is what this
step is: the version everything else gets measured against.

A map in memory is also the simplest storage available. Looking up a key takes tens of
nanoseconds. There is no network call, nothing to serialize, and no disk involved. That
makes it a useful starting line, because it shows what the HTTP layer costs before any
storage is added underneath it.

---

## The Go in this step

This assumes you already write code in something like Java, so functions, variables,
objects, and classes are not explained here. What follows is what Go does differently and
what the standard library gives you. The same terms are listed in
[go-glossary.md](go-glossary.md).

### Modules, packages, and where the program starts

A **module** is the unit Go builds and versions, roughly what a `pom.xml` describes. It
is one `go.mod` file holding a name and a list of dependencies:

```bash
go mod init github.com/swayyaam/premature-optimization
```

The name looks like a URL because Go downloads imported code straight from that path.
Nothing imports this project yet, so for now it is a label.

Inside a module, code is grouped into **packages**, and each file names its package on
the first line. `package main` is the one that produces a runnable binary, and it needs a
`func main()`:

```go
package main

func main() {
	// entry point
}
```

Any other package name builds a library. This project has one package spread over one
file.

Imports are paths, and the last segment becomes the name you call things through:

```go
import (
	"encoding/json"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
)
```

So `net/http` is used as `http.ListenAndServe`, and `math/rand/v2` as `rand.IntN`. All
five ship with Go, so `go.mod` lists no dependencies at all.

Two things are stricter than you may expect. An import you never use fails the build, and
so does a local variable you declare and never read. There is no warning level for this.

### Running and checking it

```bash
go run .        # compile and run the current directory
go build .      # compile to a binary
go vet ./...    # flags code that compiles but is probably a mistake
gofmt -l .      # list files that are not formatted the standard way
```

Compilation is fast enough that `go run .` feels like running a script.

Formatting is settled by `gofmt`, which ships with the language and has no options.
`gofmt -w .` rewrites your files to match, and every Go project you read will look the
same as a result.

### Declarations and zero values

```go
const (
	addr       = ":8080"
	codeLength = 7
	alphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
)
```

The brackets group several `const` declarations so the keyword is not repeated. Types are
inferred here.

`addr` has no host before the port, which tells the server to accept connections on every
network interface. `"localhost:8080"` would only accept them from the same machine.

Variables have two forms, and the type always comes after the name:

```go
var code string        // stated type, starts empty
code := randomCode()   // type inferred from the right side
```

`:=` is the common one and only works inside a function.

Java gives fields a default value but makes you assign locals before use. Go applies
defaults everywhere: `0`, `""`, `false`, and `nil` for maps, slices, pointers, and
interfaces. These are called **zero values**, and a declared variable is always usable.

### Structs and visibility

Go has no classes. A **struct** holds fields, and methods are declared separately,
outside the type:

```go
type store struct {
	mu   sync.RWMutex
	urls map[string]string
}
```

Fields go one per line with no commas, name first and type second.

There are no `public` or `private` keywords. A name starting with a capital letter is
visible to other packages, and a lowercase one stays inside its own package. That rule
covers types, fields, functions, and methods alike. Everything here lives in one package,
so most names are lowercase, and the exception later in this file is worth watching for.

### Maps

`map[string]string` is Go's `HashMap<String, String>`. Ours holds a short code and the
URL it points to.

A map variable that has not been assigned is `nil`, and writing to a `nil` map crashes
the program, so it gets created first. `make` is the built-in that sets up maps, slices,
and channels:

```go
s := &store{urls: make(map[string]string)}
```

Reading has two forms:

```go
url := s.urls[code]        // a missing key gives you the zero value, ""
url, ok := s.urls[code]    // ok is false when the key was absent
```

There is no `null` to check for, so the second form is how you tell a missing key from a
key stored with an empty value. `find` passes that pair up to its caller for exactly that
reason.

The same form shows up when only the second half matters:

```go
if _, taken := s.urls[code]; !taken {
```

`_` is the **blank identifier**, used wherever Go hands you a value you have no use for.
This line also shows that an `if` can run a statement before its condition, separated by
a semicolon, with both scoped to the `if`. Conditions take no brackets, and braces are
always required.

### Pointers and value semantics

Java hands you references to objects and copies of primitives, and never asks you to
think about it. Go copies whatever you give it, structs included, and you ask for a
pointer when you do not want the copy. `&x` takes an address and `*T` is the type
"pointer to T":

```go
func newStore() *store {
	return &store{urls: make(map[string]string)}
}
```

There is no pointer arithmetic, and Go is garbage collected, so returning a pointer to
something created inside a function is normal.

### Methods and receivers

A method is a function with a **receiver** written in brackets before its name:

```go
func (s *store) save(url string) string {
```

That is `save` on `*store`, called as `s.save(...)`. The receiver is Go's `this`, except
you name it and choose its type. Methods live at the top level of the file rather than
inside the type declaration, which is what lets you attach them to a type you defined
anywhere in the package.

The receiver here is a pointer for one specific reason: a `sync.RWMutex` must never be
copied. With a value receiver, every call would work on its own copy of the lock, so two
goroutines could each hold what they think is the lock. `go vet` catches this exact
mistake, which is a good reason to run it.

### Multiple return values and errors

A Go function can return more than one value:

```go
func (s *store) find(code string) (string, bool) {
	url, ok := s.urls[code]
	return url, ok
}
```

Error handling is built on that, because **Go has no exceptions**. There is no `throw`,
no `try`, no `catch`, and no stack unwinding. A call that can fail returns an `error` as
its last value, and the caller checks it immediately:

```go
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
	http.Error(w, `body must be JSON: {"url": "https://example.com"}`, http.StatusBadRequest)
	return
}
```

`error` is a built-in interface type, and `nil` means the call succeeded. This is why
`if err != nil` appears so often in Go code. Nothing skips past your function to be
handled higher up, so every failure is dealt with where it happens.

The message is written in backticks. A backtick string ignores backslash escapes, so the
`"` characters inside it need no escaping.

### Goroutines

A **goroutine** is a function running concurrently, scheduled by the Go runtime instead
of the operating system. They start at a few kilobytes and grow on demand, so a program
can run hundreds of thousands of them where threads would run out long before.
`go someFunction()` starts one.

There is no `go` statement anywhere in `main.go`, and it still matters, because
`http.ListenAndServe` starts a goroutine for every connection. This is the same situation
as a servlet container handing each request to a thread from a pool: your handler code
runs many times at once, on several cores.

Which means two requests can reach that map at the same instant, and Go maps do not allow
that. Here is the same program with the lock removed, called by 50 clients at once:

```
WARNING: DATA RACE
Read at 0x00c0000b4d80 by goroutine 11:
  main.(*store).save()
Previous write at 0x00c0000b4d80 by goroutine 10:
  main.(*store).save()
...
fatal error: concurrent map read and map write
```

No exception was thrown and no request got a 500. The process stopped, and everything it
was in the middle of stopped with it. A Java `HashMap` in the same situation corrupts
itself quietly and misbehaves later; Go checks for the overlap and ends the program on
the spot.

That report came from the **race detector**, part of the Go toolchain:

```bash
go run -race .
```

It watches memory while the program runs and reports unsynchronized access even when the
timing happened to work out. It costs roughly ten times the CPU, so it belongs in
development and CI.

### The lock, and `defer`

`sync.RWMutex` is a read/write lock with two modes:

| Call | Meaning |
| --- | --- |
| `Lock()` / `Unlock()` | One writer, no readers |
| `RLock()` / `RUnlock()` | Many readers together, no writer |

Writes take the first, reads take the second:

```go
func (s *store) save(url string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	// ...
}

func (s *store) find(code string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// ...
}
```

That split has a direct effect on throughput. A shortener serves far more redirects than
it creates links. A plain `sync.Mutex`, which is what `synchronized` gives you, would make
every redirect queue behind every other redirect, so a machine with eight cores would get
through the work of one. `RLock` lets the reads run together and only makes them wait
while a write is in progress.

The lock is declared directly above the fields it protects. That placement is the
convention for showing what it covers, since Go has nothing like `synchronized` marking
the boundary for you.

**`defer`** schedules a call for when the surrounding function returns, however it
returns, which is the job `finally` does. Writing `defer s.mu.Unlock()` on the line after
`Lock()` is the standard approach, because any `return` added to the middle of the
function later still releases the lock.

### Starting the server

```go
log.Fatal(http.ListenAndServe(addr, mux))
```

`http.ListenAndServe` takes an address and a router and serves connections until it
fails. It only returns on failure, so the error it returns is always worth printing.
`log.Fatal` prints and exits with a non-zero status. `log` is the built-in logger and
writes to standard error with a timestamp.

### Routing

```go
mux := http.NewServeMux()
mux.HandleFunc("POST /shorten", handleShorten(s))
mux.HandleFunc("GET /healthz", handleHealth)
mux.HandleFunc("GET /{code}", handleRedirect(s))
```

`ServeMux` is the router in the standard library. Since Go 1.22 its patterns include the
HTTP method and named path segments, so it covers what you would otherwise reach for
annotations to express.

- `"POST /shorten"` matches that method and path only. A `GET` to the same path returns
  **405 Method Not Allowed** with no code from us.
- `"GET /{code}"` captures one path segment under the name `code`, read in the handler
  with `r.PathValue("code")`.
- The most specific pattern wins regardless of registration order, so `GET /healthz`
  returns `ok` even though the `/{code}` wildcard was registered first.

### Handlers

Every handler has the same signature:

```go
func(w http.ResponseWriter, r *http.Request)
```

`*http.Request` carries the method, URL, headers, and `r.Body` as a readable stream.

`http.ResponseWriter` is an **interface**, and Go interfaces work like Java's with one
difference: a type satisfies one by having the right methods, with no `implements`
clause anywhere. Nothing declares the relationship. Three methods matter here:

```go
w.Header().Set("Content-Type", "application/json")  // headers first
w.WriteHeader(http.StatusCreated)                   // then the status, once
w.Write([]byte("ok\n"))                             // then the body
```

That order is required and nothing enforces it. Writing the body first sends a `200 OK`
by itself, and any header set afterwards is discarded.

`http.HandlerFunc` is a function type with a method on it, which is how a plain
`func(w, r)` satisfies the `http.Handler` interface and can be passed to `HandleFunc`.

Three common replies have helpers, all used here:

```go
http.Error(w, "url is required", http.StatusBadRequest)  // status plus a text message
http.NotFound(w, r)                                      // 404
http.Redirect(w, r, url, http.StatusFound)               // 302 with a Location header
```

`http.Redirect` is the whole redirect endpoint. It sets `Location` and writes the status.
**302 Found** marks the redirect as temporary, so browsers and proxies ask the server
again every time. A 301 is permanent, and clients would cache it and stop calling the
server, which leaves nothing to measure and no way to repoint a link later.

### Passing the store to the handlers

A handler needs the store, and its signature has no room for it. Java would inject it as
a field on the controller. Go returns the handler from a function that already has it:

```go
func handleShorten(s *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ...uses s...
	}
}
```

A function that holds on to variables from where it was written is a **closure**, and Go
keeps those variables alive as long as the function exists. `main` decides what the store
is and passes it in, so each handler's dependencies show up in its signature and none of
them reach for a package-level variable.

### JSON

```go
type shortenRequest struct {
	URL string `json:"url"`
}

type shortenResponse struct {
	Code     string `json:"code"`
	ShortURL string `json:"short_url"`
}
```

The backtick text after a field is a **struct tag**, metadata read at runtime through
reflection. It does the job of Jackson's `@JsonProperty`.

Both structs use capitalized field names because `encoding/json` is another package and
can only see exported names. A lowercase `url string` is skipped silently, with no error
and no value, which is the usual cause of a Go struct that encodes to an empty object.

```go
var req shortenRequest
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
```

`&req` passes a pointer because `Decode` writes into the variable. Passing `req` would
hand it a copy to fill in and throw away. Reading from `r.Body` directly means the body
is never held in memory in full.

Encoding goes the other way:

```go
json.NewEncoder(w).Encode(shortenResponse{...})
```

`Encode` returns an error this file does not check, which is the one unchecked call in
it. The status code has already gone out by then, so there is no way left to tell the
client anything, and the only failure is a client that has hung up.

### Slices, loops, and generating a code

```go
func randomCode() string {
	b := make([]byte, codeLength)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}
```

A **slice** is Go's list type, written `[]byte` or `[]string`. It is a view over an array
holding a pointer, a length, and a capacity, which puts it closer to `ArrayList` than to
a Java array. `make([]byte, 7)` gives seven zero bytes.

`for` is the only loop keyword in Go. It covers the counting loop, the while loop,
`for {}` for an endless one, and `for i := range b` to walk a collection:

```go
for {
	code = randomCode()
	if _, taken := s.urls[code]; !taken {
		break
	}
}
```

Indexing a string gives a single `byte`, and `string(b)` converts the slice back. That
conversion copies, since Go strings are immutable.

`math/rand/v2` seeds itself, so nothing has to be set up and two processes will not
produce the same sequence. It is not suitable for values that have to be unguessable,
which is what `crypto/rand` is for.

---

## How the code fits together

**Creating a link.** `POST /shorten` decodes the body, rejects an empty URL, and calls
`store.save`. That method takes the write lock, generates a seven character code, checks
the map, and repeats until it finds one that is free. It stores the URL under that code
and returns it. The handler replies `201 Created` with the code and a full short URL
built from `r.Host`.

**Following a link.** `GET /{code}` reads the code out of the path and calls
`store.find`, which takes the read lock and does one map lookup. A hit sends a 302 to the
original URL, and a miss sends a 404.

**Health.** `GET /healthz` writes `ok`. It touches no shared state and takes no locks, so
it measures the HTTP layer on its own.

The retry loop in `save` is worth one note. With 36 characters in 7 positions there are
about 78 billion possible codes, so the loop exists for correctness rather than because
it is expected to run twice. It is also why `save` holds the write lock across its whole
body instead of only the final assignment. Checking whether a code is free and then
claiming it has to happen as one step, or two goroutines could both find the same code
free and both write to it.

---

## Try it

```bash
go run .
```

Create a link:

```bash
curl -X POST localhost:8080/shorten -d '{"url":"https://go.dev/doc/effective_go"}'
```

```json
{"code":"8ew4pgn","short_url":"http://localhost:8080/8ew4pgn"}
```

Follow it. `-i` prints the response headers, which is where the redirect is:

```bash
curl -i localhost:8080/8ew4pgn
```

```
HTTP/1.1 302 Found
Location: https://go.dev/doc/effective_go
```

The error cases:

```bash
curl -i localhost:8080/nosuch                         # 404
curl -X POST localhost:8080/shorten -d 'not json'     # 400
curl -X POST localhost:8080/shorten -d '{}'           # 400, url is required
curl -i -X POST localhost:8080/healthz                # 405, from the router
```

Then stop the server, start it again, and request the same short link.

## Load test results

None for this step. There is no load testing tool set up yet, and a number produced by
hand here would not be repeatable. `/benchmarks` is empty until that is in place.
