// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/hujson"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

// Options describes settings for a Server.
type Options struct {
	// The tailnet hostname the server should run on (required).
	Hostname string `json:"hostname,omitempty"`

	// The directory for tailscale state and configurations (optional).
	// If omitted or empty, the default location is used.
	StateDir string `json:"stateDir,omitempty"`

	// If true, serve HTTPS instead of HTTP.
	ServeHTTPS bool `json:"serveHTTPS,omitempty"`

	// If non-empty, a SQLite database URL to use for local state.
	LocalState string `json:"localState,omitempty"`

	// If non-empty, and LocalState is defined, export a read-only copy of the
	// local state database as a source with this name.
	LocalSource string `json:"localSource,omitempty"`

	// Databases that the server will allow queries against (optional).
	Sources []DBSpec `json:"sources,omitempty"`

	// Additional links that should be propagated to the UI.
	UILinks []UILink `json:"links,omitempty"`

	// The maximum timeout for a database query (0 means no timeout).
	QueryTimeout Duration `json:"queryTimeout,omitempty"`

	// The fields below are not encoded for storage.

	// A connection to tailscaled for authorization checks. If nil, no
	// authorization checks are performed and all requests are permitted.
	LocalClient LocalClient `json:"-"`

	// If non-nil, the server will add metrics to this map. The caller is
	// responsible for ensuring the map is published.
	Metrics *expvar.Map `json:"-"`

	// If non-nil and a LocalClient is available, Authorize is called for each
	// request giving the requested database src and the caller's WhoIs record.
	// If it reports an error, the request is failed.
	//
	// If Authorize is nil and a LocalClient is available, a default rule is
	// used that checks the caller's capabilities.
	//
	// If no LocalClient is available, this field is ignored, no authorization
	// checks are performed, and all requests are accepted.
	Authorize func(src string, info *apitype.WhoIsResponse) error `json:"-"`

	// Optional rules to apply when rendering text for presentation in the UI.
	// After generating the value string, each rule is matched in order, and the
	// first match (if any) is applied to rewrite the output. The value returned
	// by the rule replaces the original string.
	UIRewriteRules []UIRewriteRule `json:"-"`

	// If non-nil, send logs to this logger. If nil, use log.Printf.
	Logf logger.Logf `json:"-"`
}

// Construct database handles to serve queries from. This returns nil without
// error if no sources are defined.
func (o Options) sources() ([]*dbHandle, error) {
	if len(o.Sources) == 0 {
		return nil, nil
	}
	srcs := make([]*dbHandle, len(o.Sources))
	for i, spec := range o.Sources {
		if err := spec.checkValid(); err != nil {
			return nil, err
		} else if spec.Label == "" {
			spec.Label = "(unidentified database)"
		}

		db, err := sql.Open(spec.Driver, spec.URL)
		if err != nil {
			return nil, fmt.Errorf("open %s %q: %w", spec.Driver, spec.URL, err)
		} else if err := db.PingContext(context.Background()); err != nil {
			db.Close()
			return nil, fmt.Errorf("ping %s %q: %w", spec.Driver, spec.URL, err)
		}
		srcs[i] = &dbHandle{
			src:   spec.Source,
			label: spec.Label,
			named: spec.Named,
			db:    db,
		}
	}
	return srcs, nil
}

func (o Options) localState() (*localState, error) {
	if o.LocalState == "" {
		return nil, nil
	}
	url := os.ExpandEnv(o.LocalState)
	db, err := sql.Open("sqlite", url)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", url, err)
	} else if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %q: %w", url, err)
	}
	return newLocalState(db)
}

func (o Options) readOnlyLocalState() (*sql.DB, error) {
	if o.LocalState == "" {
		return nil, errors.New("no local state")
	}
	url := "file:" + os.ExpandEnv(o.LocalState) + "?mode=ro"
	return sql.Open("sqlite", url)
}

const tailsqlCap = "https://tailscale.com/cap/tailsql"

// authorize returns an authorization callback based on the Access field of o.
func (o Options) authorize() func(src string, who *apitype.WhoIsResponse) error {
	if o.Authorize != nil {
		return o.Authorize
	}

	logf := o.Logf
	if logf == nil {
		logf = log.Printf
	}
	return func(dataSrc string, who *apitype.WhoIsResponse) (err error) {
		caller := who.UserProfile.LoginName
		if who.Node.IsTagged() {
			caller = who.Node.Name
		}
		defer func() {
			logf("[tailsql] auth src=%q who=%q err=%v", dataSrc, caller, err)
		}()
		type rule struct {
			DataSrc []string `json:"src"`
		}
		rules, err := tailcfg.UnmarshalCapJSON[rule](who.CapMap, tailsqlCap)
		if err != nil || len(rules) == 0 {
			return errors.New("not authorized for access tailsql")
		}
		for _, rule := range rules {
			for _, s := range rule.DataSrc {
				if s == "*" || s == dataSrc {
					return nil
				}
			}
		}
		return fmt.Errorf("not authorized for access to %q", dataSrc)
	}
}

// UILink carries anchor text and a target URL for a hyperlink.
type UILink struct {
	Anchor string `json:"anchor"`
	URL    string `json:"url"`
}

// UIRewriteRule is a rewriting rule for rendering output in HTML.
//
// A rule matches a value if:
//
//   - Its Column regexp is empty or matches the column name, and
//   - Its Value regexp is empty or matches the value string
type UIRewriteRule struct {
	Column *regexp.Regexp // pattern for the column name (nil matches all)
	Value  *regexp.Regexp // pattern for the value (nil matches all)

	// The Apply function takes the name of a column, the input value, and the
	// result of matching the value regexp (if any). Its return value replaces
	// the input when the value is rendered. If Apply == nil, the input is not
	// modified.
	Apply func(column, input string, valueMatch []string) any
}

// checkApply reports whether u matches the specified column and input, and if
// so returns the result of applying u to it.
func (u UIRewriteRule) checkApply(column, input string) (bool, any) {
	if u.Column != nil && !u.Column.MatchString(column) {
		return false, nil // no match for this column name
	}

	var m []string
	if u.Value != nil {
		// If there is a regexp but it doesn't match, fail this rule.
		// If there is no regexp we accept all values (with an empty match).
		m = u.Value.FindStringSubmatch(input)
		if m == nil {
			return false, nil
		}
	}
	if u.Apply == nil {
		return true, input
	}
	return true, u.Apply(column, input, m)
}

// A DBHandle wraps an open SQL database with descriptive metadata.
// The handle permits a provider, which creates the handle, to share the
// database with a reader, and to safely swap to a new database.
//
// This is used to allow a data source being used by a Server to safely be
// updated with a new underlying database. The Swap method ensures the new
// value is exchanged without races.
type dbHandle struct {
	src string

	// mu protects the fields below.
	// Hold shared to read the label and issue queries against db.
	// Hold exclusive to replace or close db or to update label.
	mu    sync.RWMutex
	label string
	db    *sql.DB
	named map[string]string
}

// Source returns the source name defined for h.
func (h *dbHandle) Source() string { return h.src }

// Label returns the label defined for h.
func (h *dbHandle) Label() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.label
}

// Named returns the named queries for h, nil if there are none.
func (h *dbHandle) Named() map[string]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.named
}

// Tx calls f with a connection to the wrapped database while holding the lock.
// Any error reported by f is returned to the caller of Tx.
// Multiple callers can safely invoke Tx concurrently.
// Tx reports an error without calling f if h is closed.
// The context passed to f can be used to look up named queries on h using
// lookupNamedQuery.
func (h *dbHandle) Tx(ctx context.Context, f func(context.Context, *sql.Tx) error) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.db == nil {
		return errors.New("handle is closed")
	}

	// We hold the lock here not to exclude concurrent connections, which are
	// safe, but to prevent the handle from being swapped (and the database
	// closed) while connections are in-flight.
	//
	// For our uses we could mark transactions ReadOnly, but not all database
	// drivers support that option (notably Snowflake does not).

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Attach the handle to the context during the lifetime of f.  This ensures
	// that f has access to named queries and other options from h while holding
	// the lock on h.
	fctx := context.WithValue(ctx, dbHandleKey{}, h)
	return f(fctx, tx) // we only read, no commit is needed
}

type dbHandleKey struct{}

// lookupNamedQuery reports whether the database handle associated with ctx has
// a named query with the given name, and if so returns the text of the query.
// If ctx does not have a database handle, it returns ("", false) always.  The
// context passed to the callback of Tx has a handle attached.
func lookupNamedQuery(ctx context.Context, name string) (string, bool) {
	if v := ctx.Value(dbHandleKey{}); v != nil {
		// Precondition: The handle lock is held.
		q, ok := v.(*dbHandle).named[name]
		return q, ok
	}
	return "", false
}

// swap locks the handle, swaps the current contents of the handle with newDB
// and newLabel, and returns the original value. The caller is responsible for
// closing a database handle when it is no longer in use.  It will panic if
// newDB == nil, or if h is closed.
func (h *dbHandle) swap(newDB *sql.DB, newOpts *DBOptions) *sql.DB {
	if newDB == nil {
		panic("new database is nil")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.db
	if old == nil {
		panic("handle is closed")
	}
	h.db = newDB
	h.label = newOpts.label()
	h.named = newOpts.namedQueries()
	return old
}

// close closes the handle, calling Close on the underlying database and
// reporting its result. It is safe to call close multiple times; successive
// calls will report nil.
func (h *dbHandle) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.db != nil {
		err := h.db.Close()
		h.db = nil
		return err
	}
	return nil
}

// UnmarshalOptions unmarshals a HuJSON Config value into opts.
func UnmarshalOptions(data []byte, opts *Options) error {
	data, err := hujson.Standardize(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &opts)
}

// Duration is a wrapper for a time.Duration that allows it to marshal more
// legibly in JSON, using the standard Go notation.
type Duration time.Duration

// Duration converts d to a standard time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d *Duration) UnmarshalText(data []byte) error {
	td, err := time.ParseDuration(string(data))
	if err != nil {
		return err
	}
	*d = Duration(td)
	return nil
}

// A DBSpec describes a database that the server should use.
type DBSpec struct {
	Source string `json:"source"`           // UI slug
	Label  string `json:"label,omitempty"`  // descriptive label
	Driver string `json:"driver,omitempty"` // e.g., "sqlite", "snowflake"

	// Named is an optional map of named SQL queries the database should expose.
	Named map[string]string `json:"named,omitempty"`

	// Exactly one of the following fields must be set.
	// If URL is set, it is used directly; otherwise KeyFile names the location
	// of a file from which the connection string is read.
	// The KeyFile, if set, will be expanded by os.ExpandEnv.

	URL     string `json:"url,omitempty"`     // path or connection URL
	KeyFile string `json:"keyFile,omitempty"` // path to key file
}

func (d *DBSpec) checkValid() error {
	switch {
	case d.Source == "":
		return errors.New("missing source")
	case d.Driver == "":
		return errors.New("missing driver name")
	case d.URL == "" && d.KeyFile == "":
		return errors.New("no URL or key file")
	case d.URL != "" && d.KeyFile != "":
		return errors.New("both URL and a key file are set")
	}
	if d.KeyFile != "" {
		key, err := os.ReadFile(os.ExpandEnv(d.KeyFile))
		if err != nil {
			return fmt.Errorf("read key file: %w", err)
		}
		d.URL = strings.TrimSpace(string(key))
	}
	return nil
}

// DBOptions are optional settings for a database. A nil *DBoptions is ready
// for use and provides defaults as described.
type DBOptions struct {
	// Label is a human-readable descriptive label to show to users when
	// rendering this database in a UI.
	Label string

	// NamedQueries is a map from names to SQL query text, that the service
	// should allow as pre-defined queries for this database.
	//
	// Unlike user-saved queries, named queries allow the database to change the
	// query when the underlying schema changes while preserving the semantics
	// the user observes.
	NamedQueries map[string]string
}

func (o *DBOptions) label() string {
	if o == nil {
		return ""
	}
	return o.Label
}

func (o *DBOptions) namedQueries() map[string]string {
	if o == nil {
		return nil
	}
	return o.NamedQueries
}
