package handler

import (
	"errors"
	"log/slog"
	"net/http"
)

// logBodyLimit logs a request rejected for exceeding the body limit (other
// read errors, such as a client disconnect, are not logged as one).
func logBodyLimit(r *http.Request, err error, limit int64) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		slog.Warn("request body too large", "path", r.URL.Path, "limit_mb", limit>>20)
	}
}
