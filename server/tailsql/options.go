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
	"sync/atomic"
	"time"

	"github.com/tailscale/hujson"
	"github.com/tailscale/setec/client/setec"
	"tailscale.com/client/tailscale/apitype"
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
	// If Authorize is nil and a LocalClient is available, the default rule is
	// to accept any logged-in user, rejecting tagged nodes.
	//
	// If no LocalClient is available, this field is ignored, no authorization
	// checks are performed, and all requests are accepted.
	Authorize func(src string, info *apitype.WhoIsResponse) error `json:"-"`

	// If non-nil, use this store to fetch secret values. This is required if
	// any of the sources specifies a named secret for its connection string.
	SecretStore *setec.Store `json:"-"`

	// Optional rules to apply when rendering text for presentation in the UI.
	// After generating the value string, each rule is matched in order, and the
	// first match (if any) is applied to rewrite the output. The value returned
	// by the rule replaces the original string.
	UIRewriteRules []UIRewriteRule `json:"-"`

	// If non-nil, send logs to this logger. If nil, use log.Printf.
	Logf logger.Logf `json:"-"`
}

// openSources opens database handles to each of the sources defined by o.
// Sources that require secrets will get them from store.
// Precondition: All the sources of o have already been validated.
func (o Options) openSources(store *setec.Store) ([]*dbHandle, error) {
	if len(o.Sources) == 0 {
		return nil, nil
	}

	srcs := make([]*dbHandle, len(o.Sources))
	for i, spec := range o.Sources {
		if spec.Label == "" {
			spec.Label = "(unidentified database)"
		}

		// Resolve the connection string.
		var connString string
		var w setec.Watcher
		switch {
		case spec.URL != "":
			connString = spec.URL
		case spec.KeyFile != "":
			data, err := os.ReadFile(os.ExpandEnv(spec.KeyFile))
			if err != nil {
				return nil, fmt.Errorf("read key file for %q: %w", spec.Source, err)
			}
			connString = strings.TrimSpace(string(data))
		case spec.Secret != "":
			w = store.Watcher(spec.Secret)
			connString = string(w.Get())
		default:
			panic("unexpected: no connection source is defined after validation")
		}

		db, err := openAndPing(spec.Driver, connString)
		if err != nil {
			return nil, err
		}
		srcs[i] = &dbHandle{
			src:    spec.Source,
			driver: spec.Driver,
			label:  spec.Label,
			named:  spec.Named,
			db:     db,
		}
		if spec.Secret != "" {
			go srcs[i].handleUpdates(spec.Secret, w, o.logf())
		}
	}
	return srcs, nil
}

func openAndPing(driver, connString string) (*sql.DB, error) {
	db, err := sql.Open(driver, connString)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	} else if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}
	return db, nil
}

// CheckSources validates the sources of o. If this succeeds, it also returns a
// slice of any secret names required by the specified sources, if any.
func (o Options) CheckSources() ([]string, error) {
	var secrets []string
	for i := range o.Sources {
		if err := o.Sources[i].checkValid(); err != nil {
			return nil, err
		}
		if s := o.Sources[i].Secret; s != "" {
			secrets = append(secrets, s)
		}
	}
	return secrets, nil
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

func (o Options) logf() logger.Logf {
	if o.Logf == nil {
		return log.Printf
	}
	return o.Logf
}

// authorize returns an authorization callback based on the Access field of o.
func (o Options) authorize() func(src string, who *apitype.WhoIsResponse) error {
	if o.Authorize != nil {
		return o.Authorize
	}

	logf := o.logf()
	return func(dataSrc string, who *apitype.WhoIsResponse) (err error) {
		caller := who.UserProfile.LoginName
		if who.Node.IsTagged() {
			caller = who.Node.Name
		}
		defer func() {
			logf("[tailsql] auth src=%q who=%q err=%v", dataSrc, caller, err)
		}()
		if who.Node.IsTagged() {
			return errors.New("tagged node is not authorized")
		}
		return nil
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
//
// If a rule matches, its Apply function is called.
type UIRewriteRule struct {
	Column *regexp.Regexp // pattern for the column name (nil matches all)
	Value  *regexp.Regexp // pattern for the value (nil matches all)

	// The Apply function takes the name of a column, the input value, and the
	// result of matching the value regexp (if any). Its return value replaces
	// the input when the value is rendered. If Apply == nil, the input is not
	// modified.
	//
	// As a special case, if Apply returns a nil value, the rule evaluator skips
	// the rule as if it had not matched, and goes on to the next rule.
	Apply func(column, input string, valueMatch []string) any
}

// CheckApply reports whether u matches the specified column and input, and if
// so returns the result of applying u to it.
func (u UIRewriteRule) CheckApply(column, input string) (bool, any) {
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
	v := u.Apply(column, input, m)
	if v == nil {
		return false, nil
	}
	return true, v
}

// A DBHandle wraps an open SQL database with descriptive metadata.
// The handle permits a provider, which creates the handle, to share the
// database with a reader, and to safely swap to a new database.
//
// This is used to allow a data source being used by a Server to safely be
// updated with a new underlying database. The Swap method ensures the new
// value is exchanged without races.
type dbHandle struct {
	src    string
	driver string

	// If not nil, the value of this field is a database update that arrived
	// while the handle was busy running a query. The concrete type is *dbUpdate
	// once initialized.
	update atomic.Value

	// mu protects the fields below.
	// Hold shared to read the label and issue queries against db.
	// Hold exclusive to replace or close db or to update label.
	mu    sync.RWMutex
	label string
	db    *sql.DB
	named map[string]string
}

// handleUpdates polls w indefinitely for updates to the connection string for
// h, and reopens the database with the new string when a new value arrives.
// This method should be called in a goroutine.
func (h *dbHandle) handleUpdates(name string, w setec.Watcher, logf logger.Logf) {
	logf("[tailsql] starting updater for secret %q", name)
	for range w.Ready() {
		// N.B. Don't log the secret value itself. It's fine to log the name of
		// the secret and the source, those are already in the config.
		connString := string(w.Get())
		db, err := openAndPing(h.driver, connString)
		if err != nil {
			logf("WARNING: opening new database for %q: %v", h.src, err)
			continue
		}
		logf("[tailsql] opened new connection for source %q", h.src)
		h.mu.Lock()
		// Close the existing active handle.
		h.db.Close()
		// If there's a pending update, close it too.
		if up := h.checkUpdate(); up != nil {
			up.newDB.Close()
		}
		h.db = db
		h.mu.Unlock()
	}
}

// checkUpdate returns nil if there is no pending update, otherwise it swaps
// out the pending database update and returns it.
func (h *dbHandle) checkUpdate() *dbUpdate {
	if up := h.update.Swap((*dbUpdate)(nil)); up != nil {
		return up.(*dbUpdate)
	}
	return nil
}

// tryUpdate checks whether h is busy with a query. If not, and there is a
// handle update pending, tryUpdate applies it.
func (h *dbHandle) tryUpdate() {
	if h.mu.TryLock() { // if not, the handle is busy; try again later
		defer h.mu.Unlock()
		if up := h.checkUpdate(); up != nil {
			h.applyUpdateLocked(up)
		}
	}
}

// applyUpdateLocked applies up to h, which must be locked exclusively.
func (h *dbHandle) applyUpdateLocked(up *dbUpdate) {
	h.label = up.label
	h.named = up.named
	h.db.Close()
	h.db = up.newDB
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
// and newLabel, and closes the original value. The caller is responsible for
// closing a database handle when it is no longer in use.  It will panic if
// newDB == nil, or if h is closed.
func (h *dbHandle) swap(newDB *sql.DB, newOpts *DBOptions) {
	if newDB == nil {
		panic("new database is nil")
	}

	up := &dbUpdate{
		newDB: newDB,
		label: newOpts.label(),
		named: newOpts.namedQueries(),
	}

	// If the handle is not busy, do the swap now.
	if h.mu.TryLock() {
		defer h.mu.Unlock()
		if h.db == nil {
			panic("handle is closed")
		}
		h.applyUpdateLocked(up)
		return
	}

	// Reaching here, the handle is busy on a query. Record an update to be
	// plumbed in later. It's possible we already had a pending update -- if
	// that happens close out the old one.
	if old := h.update.Swap(up); old != nil {
		if up := old.(*dbUpdate); up != nil {
			up.newDB.Close()
		}
	}
}

// A dbUpdate is an open database handle, label, and set of named queries that
// are ready to be installed in a database handle.
type dbUpdate struct {
	newDB *sql.DB
	label string
	named map[string]string
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
	//
	// If URL is set, it is used directly as the connection string.
	//
	// If KeyFile is set, it names the location of a file containing the
	// connection string.  If set, KeyFile is expanded by os.ExpandEnv.
	//
	// Otherwise, Secret is the name of a secret to fetch from the secrets
	// service, whose value is the connection string. This requires that a
	// secrets server be configured in the options.

	URL     string `json:"url,omitempty"`     // path or connection URL
	KeyFile string `json:"keyFile,omitempty"` // path to key file
	Secret  string `json:"secret,omitempty"`  // name of secret
}

func (d *DBSpec) countFields() (n int) {
	for _, s := range []string{d.URL, d.KeyFile, d.Secret} {
		if s != "" {
			n++
		}
	}
	return
}

func (d *DBSpec) checkValid() error {
	switch {
	case d.Source == "":
		return errors.New("missing source")
	case d.Driver == "":
		return errors.New("missing driver name")
	case d.countFields() != 1:
		return errors.New("exactly one connection source must be set")
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
