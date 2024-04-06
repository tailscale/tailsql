// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package tailsql implements an HTTP API and "playground" UI for sending SQL
// queries to a collection of local and/or remote databases, and rendering the
// results for human consumption.
//
// # API
//
// The main UI is served from "/", static assets from "/static/".
// The following path and query parameters are understood:
//
//   - The q parameter carries an SQL query. The syntax of the query depends on
//     the src (see below). See also "Named Queries" below.
//
//   - The src parameter names which database to query against. Its values are
//     defined when the server is set up. If src is omitted, the first database
//     is used as a default.
//
//   - "/" serves output as HTML for the UI. In this format the query (q) may
//     be empty (no output will be displayed).
//
//   - "/json" serves output as JSON objects, one per row.  In this format the
//     query (q) must be non-empty.
//
//   - "/csv" serves output as comma-separated values, the first line giving
//     the column names, the remaining lines the rows. In this format the query
//     (q) must be non-empty.
//
//   - "/meta" serves a JSON blob of metadata about available data sources.
//
// Calls to the /json endpoint must set the Sec-Tailsql header to "1". This
// prevents browser scripts from directing queries to this endpoint.
//
// Calls to /csv must either set Sec-Tailsql to "1" or include a tailsqlQuery=1
// same-site cookie.
//
// Calls to the UI with a non-empty query must include the tailsqlQuery=1
// same-site cookie, which is set when the UI first loads. This averts simple
// cross-site redirection tricks.
//
// # Named Queries
//
// The query processor treats a query of the form "named:<string>" as a named
// query.  Named queries are SQL queries pre-defined by the database, to allow
// users to make semantically stable queries without relying on a specific
// schema format.
//
// # Meta Queries
//
// The query processor treats a query "meta:named" as a meta-query to report
// the names and content of all named queries, regardless of source.
package tailsql

import (
	"context"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/types/logger"
	"tailscale.com/util/httpm"
)

//go:embed ui.tmpl
var uiTemplate string

//go:embed static
var staticFS embed.FS

var ui = template.Must(template.New("sql").Parse(uiTemplate))

// noBrowsersHeader is a header that must be set in requests to the API
// endpoints that are intended to be accessed not from browsers.  If this
// header is not set to a non-empty value, those requests will fail.
const noBrowsersHeader = "Sec-Tailsql"

// siteAccessCookie is a cookie that must be presented with any request from a
// browser that includes a query, and does not have the noBrowsersHeader.
var siteAccessCookie = &http.Cookie{
	Name: "tailsqlQuery", Value: "1", SameSite: http.SameSiteLaxMode, HttpOnly: true,
}

// contentSecurityPolicy is the CSP value sent for all requests to the UI.
// Adapted from https://owasp.org/www-community/controls/Content_Security_Policy.
const contentSecurityPolicy = `default-src 'none'; script-src 'self'; connect-src 'self'; img-src 'self'; style-src 'self'; frame-ancestors 'self'; form-action 'self';`

func requestHasSiteAccess(r *http.Request) bool {
	c, err := r.Cookie(siteAccessCookie.Name)
	return err == nil && c.Value == siteAccessCookie.Value
}

func requestHasSecureHeader(r *http.Request) bool {
	return r.Header.Get(noBrowsersHeader) != ""
}

// Server is a server for the tailsql API.
type Server struct {
	lc        LocalClient
	state     *localState // local state database (for query logs)
	self      string      // if non-empty, the local state source label
	links     []UILink
	rules     []UIRewriteRule
	authorize func(string, *apitype.WhoIsResponse) error
	qtimeout  time.Duration
	logf      logger.Logf

	mu  sync.Mutex
	dbs []*dbHandle
}

// NewServer constructs a new server with the given Options.
func NewServer(opts Options) (*Server, error) {
	// Check the validity of the sources, and get any secret names they require
	// from the secrets service. If there are any, we also require that a
	// secrets service URL is configured.
	sec, err := opts.CheckSources()
	if err != nil {
		return nil, fmt.Errorf("checking sources: %w", err)
	} else if len(sec) != 0 && opts.SecretStore == nil {
		return nil, fmt.Errorf("have %d named secrets but no secret store", len(sec))
	}

	dbs, err := opts.openSources(opts.SecretStore)
	if err != nil {
		return nil, fmt.Errorf("opening sources: %w", err)
	}
	state, err := opts.localState()
	if err != nil {
		return nil, fmt.Errorf("local state: %w", err)
	}
	if state != nil && opts.LocalSource != "" {
		db, err := opts.readOnlyLocalState()
		if err != nil {
			return nil, fmt.Errorf("read-only local state: %w", err)
		}
		dbs = append(dbs, &dbHandle{
			src:   opts.LocalSource,
			label: "tailsql local state",
			db:    db,
			named: map[string]string{
				"schema": `select * from sqlite_schema`,
			},
		})
	}

	if opts.Metrics != nil {
		addMetrics(opts.Metrics)
	}
	return &Server{
		lc:        opts.LocalClient,
		state:     state,
		self:      opts.LocalSource,
		links:     opts.UILinks,
		rules:     opts.UIRewriteRules,
		authorize: opts.authorize(),
		qtimeout:  opts.QueryTimeout.Duration(),
		logf:      opts.logf(),
		dbs:       dbs,
	}, nil
}

// SetDB adds or replaces the database associated with the specified source in
// s with the given open db and options.
//
// If a database was already open for the given source, its value is replaced,
// the old database handle is closed, and SetDB reports true.
//
// If no database was already open for the given source, a new source is added
// and SetDB reports false.
func (s *Server) SetDB(source string, db *sql.DB, opts *DBOptions) bool {
	if db == nil {
		panic("new database is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, src := range s.dbs {
		if src.Source() == source {
			src.swap(db, opts)
			return true
		}
	}
	s.dbs = append(s.dbs, &dbHandle{
		db:    db,
		src:   source,
		label: opts.label(),
		named: opts.namedQueries(),
	})
	return false
}

// Close closes all the database handles held by s and returns the join of
// their errors.
func (s *Server) Close() error {
	dbs := s.getHandles()
	errs := make([]error, len(dbs))
	for i, db := range dbs {
		errs[i] = db.close()
	}
	return errors.Join(errs...)
}

// NewMux constructs an HTTP router for the service.
func (s *Server) NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveUI)
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	return mux
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != httpm.GET {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	src := r.FormValue("src")
	if src == "" {
		dbs := s.getHandles()
		if len(dbs) != 0 {
			src = dbs[0].Source() // default to the first source
		}
	}

	// Reject query strings that are egregiously too long.
	const maxQueryBytes = 4000

	query := strings.TrimSpace(r.FormValue("q"))
	if len(query) > maxQueryBytes {
		http.Error(w, "query too long", http.StatusBadRequest)
		return
	}

	caller, isAuthorized := s.checkAuth(w, r, src, query)
	if !isAuthorized {
		authErrorCount.Add(1)
		return
	}

	var err error
	switch r.URL.Path {
	case "/":
		htmlRequestCount.Add(1)
		err = s.serveUIInternal(w, r, caller, src, query)
	case "/csv":
		csvRequestCount.Add(1)
		err = s.serveCSVInternal(w, r, caller, src, query)
	case "/json":
		jsonRequestCount.Add(1)
		err = s.serveJSONInternal(w, r, caller, src, query)
	case "/meta":
		metaRequestCount.Add(1)
		err = s.serveMetaInternal(w, r)
	default:
		badRequestErrorCount.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		code := errorCode(err)
		if code == http.StatusFound {
			http.Redirect(w, r, r.URL.String(), code)
			return
		} else if code >= 400 && code < 500 {
			badRequestErrorCount.Add(1)
		} else {
			internalErrorCount.Add(1)
		}
		http.Error(w, err.Error(), errorCode(err))
		return
	}
}

// serveUIInternal handles the root GET "/" route.
func (s *Server) serveUIInternal(w http.ResponseWriter, r *http.Request, caller, src, query string) error {
	http.SetCookie(w, siteAccessCookie)
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	w.Header().Set("X-Frame-Options", "DENY")

	// If a non-empty query is present, require either a site access cookie or a
	// no-browsers header.
	if query != "" && !requestHasSecureHeader(r) && !requestHasSiteAccess(r) {
		return statusErrorf(http.StatusFound, "access cookie not found (redirecting)")
	}

	w.Header().Set("Content-Type", "text/html")
	data := &uiData{
		Query:   query,
		Source:  src,
		Sources: s.getHandles(),
		Links:   s.links,
	}
	out, err := s.queryContext(r.Context(), caller, src, query)
	if errors.Is(err, errTooManyRows) {
		out.More = true
	} else if err != nil {
		queryErrorCount.Add(1)
		msg := err.Error()
		data.Error = &msg
		return ui.Execute(w, data)
	}

	// Don't send too many rows to the UI, the DOM only has one gerbil on its
	// wheel. Note we leave NumRows alone, so it can be used to report the real
	// number of results the query returned.
	const maxUIRows = 500
	if out != nil && out.NumRows > maxUIRows {
		out.Rows = out.Rows[:maxUIRows]
		out.Trunc = true
	}
	data.Output = out.uiOutput("(null)", s.rules)
	return ui.Execute(w, data)
}

// serveCSVInternal handles the GET /csv route.
func (s *Server) serveCSVInternal(w http.ResponseWriter, r *http.Request, caller, src, query string) error {
	if query == "" {
		return statusErrorf(http.StatusBadRequest, "no query provided")
	}

	// Require either a site access cookie or a no-browsers header.
	if !requestHasSecureHeader(r) && !requestHasSiteAccess(r) {
		return statusErrorf(http.StatusForbidden, "query access denied")
	}

	out, err := s.queryContext(r.Context(), caller, src, query)
	if errors.Is(err, errTooManyRows) {
		// fall through to serve what we got
	} else if err != nil {
		queryErrorCount.Add(1)
		return err
	}

	return writeResponse(w, r, "text/csv", func(w io.Writer) error {
		cw := csv.NewWriter(w)
		cw.WriteAll(out.csvOutput())
		cw.Flush()
		return cw.Error()
	})
}

// serveJSONInternal handles the GET /json route.
func (s *Server) serveJSONInternal(w http.ResponseWriter, r *http.Request, caller, src, query string) error {
	if query == "" {
		return statusErrorf(http.StatusBadRequest, "no query provided")
	}
	if !requestHasSecureHeader(r) {
		return statusErrorf(http.StatusForbidden, "query access denied")
	}

	out, err := s.queryContextJSON(r.Context(), caller, src, query)
	if err != nil {
		queryErrorCount.Add(1)
		return err
	}

	return writeResponse(w, r, "application/json", func(w io.Writer) error {
		enc := json.NewEncoder(w)
		for _, row := range out {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	})
}

// serveMetaInternal handles the GET /meta route.
func (s *Server) serveMetaInternal(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "application/json")
	opts := &Options{UILinks: s.links, QueryTimeout: Duration(s.qtimeout)}
	for _, h := range s.getHandles() {
		opts.Sources = append(opts.Sources, DBSpec{
			Source: h.Source(),
			Label:  h.Label(),
			Named:  h.Named(),

			// N.B. Don't report the URL or the KeyFile location.
		})
	}
	return json.NewEncoder(w).Encode(struct {
		Meta *Options `json:"meta"`
	}{Meta: opts})
}

// errTooManyRows is a sentinel error reported by queryContextAny when a
// sensible bound on the size of the result set is exceeded.
var errTooManyRows = errors.New("too many rows")

// queryContextAny executes query using the database handle identified by src.
// The results have whatever types were assigned by the database scanner.
//
// If the number of rows exceeds a sensible limit, it reports errTooManyRows.
// In that case, the result set is still valid, and contains the results that
// were read up to that point.
func (s *Server) queryContext(ctx context.Context, caller, src, query string) (*dbResult, error) {
	if query == "" {
		return nil, nil
	}

	// As a special case, treat a query prefixed with "meta:" as a meta-query to
	// be answered regardless of source.
	if strings.HasPrefix(query, "meta:") {
		return s.queryMeta(ctx, query)
	}

	h := s.dbHandleForSource(src)
	if h == nil {
		return nil, statusErrorf(http.StatusBadRequest, "unknown source %q", src)
	}
	// Verify that the query does not contain statements we should not ask the
	// database to execute.
	if err := checkQuery(query); err != nil {
		return nil, statusErrorf(http.StatusBadRequest, "invalid query: %w", err)
	}

	const maxRowsPerQuery = 10000

	if s.qtimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.qtimeout)
		defer cancel()
	}

	return runQueryInTx(ctx, h,
		func(fctx context.Context, tx *sql.Tx) (_ *dbResult, err error) {
			start := time.Now()
			var out dbResult
			defer func() {
				out.Elapsed = time.Since(start)
				s.logf("[tailsql] query src=%q query=%q elapsed=%v err=%v",
					src, query, out.Elapsed.Round(time.Millisecond), err)

				// Record successful queries in the persistent log.  But don't log
				// queries to the state database itself.
				if err == nil && src != s.self {
					serr := s.state.LogQuery(ctx, caller, src, query, out.Elapsed)
					if serr != nil {
						s.logf("[tailsql] WARNING: Error logging query: %v", serr)
					}
				}
			}()

			// Check for a named query.
			if name, ok := strings.CutPrefix(query, "named:"); ok {
				real, ok := lookupNamedQuery(fctx, name)
				if !ok {
					return nil, statusErrorf(http.StatusBadRequest, "named query %q not recognized", name)
				}
				s.logf("[tailsql] resolved named query %q to %#q", name, real)
				query = real
			}

			rows, err := tx.QueryContext(fctx, query)
			if err != nil {
				return nil, err
			}
			defer rows.Close()

			cols, err := rows.ColumnTypes()
			if err != nil {
				return nil, fmt.Errorf("listing column types: %w", err)
			}
			for _, col := range cols {
				out.Columns = append(out.Columns, col.Name())
			}

			var tooMany bool
			for rows.Next() && !tooMany {
				if len(out.Rows) == maxRowsPerQuery {
					tooMany = true
					break
				} else if fctx.Err() != nil {
					return nil, fmt.Errorf("scanning row: %w", fctx.Err())
				}
				vals := make([]any, len(cols))
				vptr := make([]any, len(cols))
				for i := range cols {
					vptr[i] = &vals[i]
				}
				if err := rows.Scan(vptr...); err != nil {
					return nil, fmt.Errorf("scanning row: %w", err)
				}
				out.Rows = append(out.Rows, vals)
			}
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("scanning rows: %w", err)
			}
			out.NumRows = len(out.Rows)

			if tooMany {
				return &out, errTooManyRows
			}
			return &out, nil
		})
}

// queryMeta handles meta-queries for internal state.
func (s *Server) queryMeta(ctx context.Context, metaQuery string) (*dbResult, error) {
	switch metaQuery {
	case "meta:named":
		// Report all the named queries
		res := &dbResult{
			Columns: []string{"source", "label", "queryName", "sql"},
		}
		for _, h := range s.getHandles() {
			source, label := h.Source(), h.Label()
			for name, sql := range h.Named() {
				res.Rows = append(res.Rows, []any{source, label, name, sql})
			}
		}
		res.NumRows = len(res.Rows)
		return res, nil
	default:
		return nil, statusErrorf(http.StatusBadRequest, "unknown meta-query %q", metaQuery)
	}
}

// queryContextJSON calls s.queryContextAny and, if it succeeds, converts its
// results into values suitable for JSON encoding.
func (s *Server) queryContextJSON(ctx context.Context, caller, src, query string) ([]jsonRow, error) {
	if query == "" {
		return nil, nil
	}

	out, err := s.queryContext(ctx, caller, src, query)
	if errors.Is(err, errTooManyRows) {
		// fall through to serve what we got
	} else if err != nil {
		return nil, err
	}
	rows := make([]jsonRow, len(out.Rows))
	for i, row := range out.Rows {
		jr := make(jsonRow, len(row))
		for j, col := range row {
			// Treat human-readable byte slices as strings.
			if b, ok := col.([]byte); ok && utf8.Valid(b) {
				col = string(b)
			}
			jr[out.Columns[j]] = col
		}
		rows[i] = jr
	}
	return rows, nil
}

// dbHandleForSource returns the database handle matching the specified src, or
// nil if no matching handle is found.
func (s *Server) dbHandleForSource(src string) *dbHandle {
	for _, h := range s.getHandles() {
		if h.Source() == src {
			return h
		}
	}
	return nil
}

// checkAuth reports the name of the caller and whether they have access to the
// given source.  If the caller does not have access, checkAuth logs an error
// to w and returns false.  The reported caller name will be "" if no caller
// can be identified.
func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request, src, query string) (string, bool) {
	// If there is no local client, allow everything.
	if s.lc == nil {
		return "", true
	}
	whois, err := s.lc.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return "", false
	} else if whois == nil {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return "", false
	}
	var caller string
	if whois.Node.IsTagged() {
		caller = whois.Node.Name
	} else {
		caller = whois.UserProfile.LoginName
	}

	// If the caller wants the UI and didn't send a query, allow it.
	// The source does not matter when there is no query.
	if r.URL.Path == "/" && query == "" {
		return caller, true
	}
	if err := s.authorize(src, whois); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return caller, false
	}
	return caller, true
}

// getHandles returns the current slice of database handles.  THe caller must
// not mutate the slice, but it is safe to read it without a lock.
func (s *Server) getHandles() []*dbHandle {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for pending updates.
	for _, h := range s.dbs {
		h.tryUpdate()
	}

	// It is safe to return the slice because we never remove any elements, new
	// data are only ever appended to the end.
	return s.dbs
}
