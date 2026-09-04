# Go glossary

Every Go term used in this repo, in the order it first came up, with a link to the step
that introduced it.

This assumes you already program in something like Java. Functions, variables, objects,
and classes are taken as given, so the entries below cover what Go calls things and where
it behaves differently. The file grows one step at a time.

---

## Step 1: [The minimal API](01-minimal-api.md)

**module**
What Go builds and versions, described by a `go.mod` file holding a name and a dependency
list, roughly the job of a `pom.xml`. Created with `go mod init <name>`. The name is an
import path, which is why it looks like a URL.

**package**
How code is grouped inside a module. Every file names its package on the first line.
Files in the same package see each other's names without importing.

**`package main`**
Builds a runnable binary instead of a library, and requires a `func main()`.

**import**
Uses a path, and the last segment becomes the name in code, so `net/http` is written as
`http.`. An import you never use fails the build, as does an unused local variable.

**standard library**
The packages that ship with Go: `net/http`, `encoding/json`, `sync`, `log`,
`math/rand/v2`. Nothing to install and no `go.mod` entry.

**`go run .` and `go build .`**
Compile and run, or compile to a binary, from the current directory.

**`gofmt`**
The formatter that ships with Go. One style, no options. `gofmt -w .` applies it.

**`go vet`**
Flags code that compiles but is probably a mistake, such as copying a mutex.

**`:=`**
Short declaration. Creates a variable and infers its type from the right side. Only works
inside a function. The longer form is `var code string`, with the type after the name.

**zero value**
Java requires locals to be assigned before use. Go gives everything a default: `0`, `""`,
`false`, and `nil` for maps, slices, pointers, and interfaces. A declared variable is
always usable.

**struct**
Go has no classes. A struct holds fields, one per line with no commas, name before type.
Methods are declared separately, outside the type.

**exported and unexported names**
Capitalization replaces `public` and `private`. A capitalized name is visible to other
packages; a lowercase one is not. Applies to types, fields, functions, and methods.

**map**
Go's `HashMap`, written `map[K]V`. An unassigned map is `nil` and writing to it crashes
the program, so create it with `make`.

**`make`**
Built-in that sets up maps, slices, and channels.

**comma-ok form**
`v, ok := m[k]` returns the value plus a boolean saying whether the key was there. Since
there is no `null` to check, this is how a missing key is told apart from a stored empty
value.

**blank identifier `_`**
Stands in for a value you are required to receive and have no use for.

**`if` with a statement in front**
`if v, ok := m[k]; ok { }` runs a statement, then tests a condition, with both scoped to
the `if`. Conditions take no brackets and braces are always required.

**pointer**
Go copies values when it passes them, structs included, so you take a pointer when you do
not want the copy. `&x` takes an address and `*T` is the type "pointer to T". No pointer
arithmetic, and Go is garbage collected.

**method and receiver**
A method is a function with a receiver in brackets before its name:
`func (s *store) save(...)`. The receiver is Go's `this`, except you name it and pick
whether it is a value or a pointer. A pointer receiver is needed to modify the struct and
to avoid copying a lock.

**multiple return values**
`func find(...) (string, bool)`. Go's error handling is built on this.

**`error`**
Go has no exceptions, no `throw`, and no `try`. A call that can fail returns an `error`
as its last value, and `nil` means success, so `if err != nil` is checked at the call
site.

**backtick string**
Ignores backslash escapes, which is convenient for text containing `"`.

**goroutine**
A function running concurrently, scheduled by the Go runtime rather than the operating
system, starting at a few kilobytes. Started with `go f()`. `net/http` starts one per
connection, so handlers run concurrently the same way servlets do.

**data race**
Two goroutines using the same memory with no coordination, at least one of them writing.
Go detects it on maps and stops the process with
`fatal error: concurrent map read and map write` instead of corrupting itself the way a
`HashMap` would.

**race detector**
`go run -race .` or `go test -race`. Watches memory access and reports races even when
the timing happened to work. Around ten times the CPU cost, so it is for development and
CI.

**`sync.RWMutex`**
A read/write lock. `Lock`/`Unlock` allows one writer. `RLock`/`RUnlock` allows many
readers at once. `sync.Mutex` is the plain version and matches `synchronized`. Read-heavy
work wants the read mode, otherwise every core queues behind the same lock.

**`defer`**
Schedules a call for when the surrounding function returns, however it returns, which is
what `finally` does. `defer mu.Unlock()` right after `mu.Lock()` means a `return` added
later still releases the lock.

**interface**
Works like a Java interface, except a type satisfies one just by having the right
methods. There is no `implements` clause and nothing declares the relationship.

**closure**
A function that holds on to variables from where it was written. Used here to give each
handler the store instead of keeping it in a package-level variable.

**slice**
Go's list type, `[]byte` or `[]string`. A view over an array made of a pointer, a length,
and a capacity, closer to `ArrayList` than to a Java array.

**`for`**
Go's only loop keyword. Covers the counting loop, the while loop, `for {}` for an endless
one, and `for i := range x` to walk a collection.

**struct tag**
Backtick metadata after a struct field, read at runtime through reflection.
`` `json:"short_url"` `` does the job of `@JsonProperty`.

**`http.ServeMux`**
The router in the standard library. Since Go 1.22 its patterns include the method and
named path segments, as in `"GET /{code}"`. The most specific pattern wins regardless of
registration order, and a wrong method gets a 405 automatically.

**`http.HandlerFunc`**
A function type with a method on it, which is how a plain `func(w, r)` satisfies the
`http.Handler` interface.

**`http.ResponseWriter`**
The response side of a handler. Set headers, call `WriteHeader(status)`, then
`Write(body)`, in that order. Writing the body first sends a 200 and discards later
header changes.

**`*http.Request`**
The request side: method, URL, headers, and `r.Body` as a readable stream.
`r.PathValue("code")` reads a named segment from the router pattern.

**`http.ListenAndServe`**
Binds an address and serves until it fails. It only returns on failure, so
`log.Fatal(http.ListenAndServe(...))` is the usual line.

**`encoding/json`**
`json.NewDecoder(r).Decode(&v)` reads a stream into a pointer.
`json.NewEncoder(w).Encode(v)` writes one out. Only capitalized fields are visible to it.

**`math/rand/v2`**
Random numbers, seeded automatically. `rand.IntN(n)`. Not suitable for values that have
to be unguessable, which is what `crypto/rand` is for.

**`log`**
The built-in logger. Writes to standard error with a timestamp. `log.Fatal` prints and
exits with a non-zero status.

---

## Step 2: [Adding Postgres](02-postgres.md)

**`go get`**
Adds a dependency and records it in `go.mod`.

**`go.sum`**
Holds a hash of every module version used, so a build fails if the contents of a
published version ever change. Committed alongside `go.mod`.

**`go mod tidy`**
Adds whatever the code imports and drops whatever it no longer uses. Worth running before
a commit.

**`go install <module>@latest`**
Downloads, compiles, and puts a command line program in `$(go env GOPATH)/bin`. How Go
programs like `hey` are distributed, with no package manager and no runtime to install.

**blank import**
`_ "github.com/jackc/pgx/v5/stdlib"` loads a package purely for its side effects. Needed
because an unreferenced import normally fails the build.

**`init()`**
A function that runs automatically when its package is loaded. The pgx driver uses one to
register itself with `database/sql`, which is what makes `sql.Open("pgx", ...)` resolve.
Together with the blank import it does the job of `Class.forName` in old JDBC code.

**`database/sql`**
The standard library's database interface. It defines connections, queries, and
transactions, and knows no specific database. Go ships no drivers, so talking to Postgres
needs one added.

**`sql.DB`**
What `sql.Open` returns. A pool of connections, not a single connection, safe to use from
many goroutines at once. Shared state moves into the database, so no mutex is needed
around it.

**`sql.Open` and `Ping`**
`Open` only parses settings and connects lazily, so a wrong host looks fine until the
first query. `Ping` forces a connection immediately and is how you fail at startup
instead.

**`QueryRow` and `Scan`**
`QueryRow` runs a statement expected to return a single row. `Scan(&url)` copies that
row's columns into the variables you point it at, converting types and reporting a
mismatch as an error.

**`Exec`**
Runs a statement with no result rows, such as an insert. Returns a `sql.Result` that can
report rows affected.

**placeholder `$1`**
Postgres numbers its query placeholders instead of using `?`. Values travel separately
from the statement text, so they are never parsed as SQL. Building the query with string
concatenation is how SQL injection happens.

**`sql.ErrNoRows`**
The error `Scan` returns when the query matched nothing. A missing row arrives as an
error rather than a null result.

**`errors.Is`**
Asks whether an error is, or wraps, a specific one. Go errors can carry another error
inside them the way an exception carries a cause, and `errors.Is` walks that chain.
`err == target` breaks as soon as anything wraps the error.

**`os.Getenv`**
Reads an environment variable and returns an empty string when it is not set, so there is
no separate presence check.

---

## Step 3: [Indexes and connection pooling](03-indexes-and-pooling.md)

**`res.RowsAffected()`**
Asks a `sql.Result` how many rows a statement changed. With
`INSERT ... ON CONFLICT DO NOTHING`, one row means the insert happened and zero means
something already held that key.

**`SetMaxOpenConns`**
Caps how many database connections may exist at once. Unlimited by default. Without a
cap, a busy server keeps opening connections and closing them again, and a Postgres
connection costs a new process on the server plus a handshake.

**`SetMaxIdleConns`**
How many unused connections the pool keeps ready instead of closing. The default of 2 is
low for a server handling many requests at a time.

**`SetConnMaxLifetime`**
Retires a connection after a given age, so a long-running process does not keep
connections to a database that has been restarted or moved.

**`time.Duration`**
A length of time, counted in nanoseconds, with its own type. Written by multiplying named
units, as in `time.Hour` or `5 * time.Minute`. Every Go function that takes a timeout
takes one of these.
