// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tsweb"
	"tailscale.com/version"
)

// LocalClient is the subset of the tailscale.LocalClient interface required by
// the Server. It is defined here so that the caller can substitute a fake for
// testing and debugging.
type LocalClient interface {
	WhoIs(context.Context, string) (*apitype.WhoIsResponse, error)
}

// uiData is the concrete type of the data value passed to the UI template.
type uiData struct {
	Query   string      // the original query
	Sources []*dbHandle // the available databases
	Source  string      // the selected source
	Output  *dbResult   // query results (may be nil)
	Error   *string     // error results (may be nil)
	Links   []UILink    // static UI links
}

// Version reports the version string of the currently running binary.
func (u *uiData) Version() string { return version.Long() }

// jsonRow represents an output row as a JSON value.
type jsonRow map[string]any

// dbResult represents the result of an SQL query in generic form.
type dbResult struct {
	Elapsed time.Duration // how long the query took
	Columns []string      // column names, in order of retrieval
	Rows    [][]any       // rows, in the types assigned by the scanner
	NumRows int           // total number of rows reported
	Trunc   bool          // whether the display was truncated
	More    bool          // whether there are more results in the database
}

// uiOutput modifies the column values of r in-place to render the values as
// strings suitable for inclusion in an HTML template. It returns r to permit
// chaining. The null string is substituted for NULL-valued columns.
func (r *dbResult) uiOutput(null string, uiRules []UIRewriteRule) *dbResult {
	if r == nil || len(r.Columns) == 0 {
		return r
	}

	// Round elapsed time for human legibilitiy.
	r.Elapsed = r.Elapsed.Round(100 * time.Microsecond)

	for _, row := range r.Rows {
	nextCol:
		for i, col := range row {
			s := valueToString(col, null)

			// Check for rewrite rules.
			colName := r.Columns[i]
			for _, rule := range uiRules {
				ok, newValue := rule.CheckApply(colName, s)
				if ok {
					row[i] = newValue
					continue nextCol
				}
			}
			row[i] = s
		}
	}
	return r
}

// csvOutput returns a CSV representation of the result, suitable for use with
// a csv.Writer.
func (r *dbResult) csvOutput() [][]string {
	if len(r.Columns) == 0 {
		return nil
	}
	rows := make([][]string, 1+len(r.Rows)) // +1 for the header
	rows[0] = r.Columns
	for i, row := range r.Rows {
		srow := make([]string, len(row))
		for j, col := range row {
			srow[j] = valueToString(col, "")
		}
		rows[i+1] = srow
	}
	return rows
}

// valueToString assigns a sensible string representation to a database value.
func valueToString(v any, null string) string {
	switch t := v.(type) {
	case nil:
		return null
	case []byte:
		// If the value looks text-like, return it as a string; otherwise encode
		// it as base64 to avert mojibake.
		if isBinaryData(t) && !utf8.Valid(t) {
			return base64.RawStdEncoding.EncodeToString(t)
		}
		return string(t)
	case string:
		return t
	case time.Time:
		// For single-day timestamps, trim off the time portion so the output
		// looks nicer on the page.
		ts := t.UTC().Format(time.RFC3339)
		pre, _, ok := strings.Cut(ts, " 00:00:00")
		if ok {
			return pre
		}
		return ts
	default:
		return fmt.Sprint(t)
	}
}

// isBinaryData reports whether data contains byte values outside the ASCII
// range, or non-printable controls.
func isBinaryData(data []byte) bool {
	for _, b := range data {
		switch {
		case b > '~': // including DEL
			return true
		case b >= ' ', b == '\t', b == '\n', b == '\r':
			// controls HT, LF, and CR are OK
		default:
			return true
		}
	}
	return false
}

// runQueryInTx executes query using h.Tx, and returns its results.
func runQueryInTx[T any](ctx context.Context, h *dbHandle, query func(context.Context, *sql.Tx) (T, error)) (T, error) {
	var out T
	err := h.Tx(ctx, func(fctx context.Context, tx *sql.Tx) error {
		var err error
		out, err = query(fctx, tx)
		return err
	})
	return out, err
}

// writeResponse calls f to write an HTTP response body of contentType.  It
// handles compressing the response with zstd if the request specifies it as an
// accepted encoding.
func writeResponse(w http.ResponseWriter, req *http.Request, contentType string, f func(w io.Writer) error) error {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Vary", "Accept-Encoding")
	if !tsweb.AcceptsEncoding(req, "zstd") {
		return f(w)
	}
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return fmt.Errorf("create compressor: %w", err)
	}
	w.Header().Set("Content-Encoding", "zstd")
	if err := f(zw); err != nil {
		zw.Close() // discard
		return err
	}
	return zw.Close()
}

// statusError is an error that carries an HTTP status code.
type statusError struct {
	code int
	err  error
}

func (s statusError) Error() string {
	return fmt.Sprintf("[%d] %s", s.code, s.err)
}

func (s statusError) Unwrap() error { return s.err }

func statusErrorf(code int, msg string, args ...any) error {
	return statusError{code: code, err: fmt.Errorf(msg, args...)}
}

// errorCode extracts an HTTP status code from an error. If err is a
// statusError, it returns the enclosed code; otherwise it defaults to
// http.StatusInternalServerError.
func errorCode(err error) int {
	var s statusError
	if errors.As(err, &s) {
		return s.code
	}
	return http.StatusInternalServerError
}

// checkQuery reports whether query is safe to send to the database.
//
// A read-only SQLite database will correctly report errors for operations that
// modify the database or its schema if it is opened read-only. However, the
// ATTACH and DETACH verbs modify only the connection, permitting the caller to
// mention any database accessible from the filesystem.
func checkQuery(query string) error {
	for _, tok := range sqlTokens(query) {
		switch tok {
		case "ATTACH", "DETACH", "TEMP", "TEMPORARY":
			return fmt.Errorf("statement %q is not allowed", tok)
		}
	}
	return nil
}

// sqlRE is a very approximate lexical matcher for SQL tokens.
var sqlRE = regexp.MustCompile(`(?m)\w+|('([^\']|'')*')|("([^\"]|"")*")|(--.*(\n|$))|\S+`)

// sqlTokens performs a lightweight and approximate lexical analysis of query
// as an SQL input. It discards string literals and comments, and returns the
// remaining "tokens" normalized to uppercase.
func sqlTokens(query string) []string {
	var tok []string
	ms := sqlRE.FindAllStringSubmatch(query, -1)
	for _, m := range ms {
		if strings.HasPrefix(m[0], "--") ||
			strings.HasPrefix(m[0], "'") ||
			strings.HasPrefix(m[0], `"`) {
			continue
		}
		tok = append(tok, strings.ToUpper(m[0]))
	}
	return tok
}
