package main

import (
	"log/slog"
	"os"
)

// setupLogging makes every log line a set of named fields rather than a
// sentence. LOG_FORMAT=json writes them as JSON for something else to read.
// The default is text, which is the same fields in a form a person can read.
func setupLogging() {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}

	var handler slog.Handler
	if os.Getenv("LOG_FORMAT") == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}

// fatal logs and stops the process. slog has no equivalent of log.Fatalf, since
// deciding to end the program is not the logger's business.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
