// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/squibble"

	_ "embed"
)

//go:embed state-schema.sql
var localStateSchema string

// schema is the schema migrator for the local state database.
var schema = &squibble.Schema{
	Current: localStateSchema,

	Updates: []squibble.UpdateRule{
		{
			Source: "afc381e1ddcdf41af700bcf24d8d40d99722827d766c1cd65c8799ea51d3e600",
			Target: "bc780f7ed5ce806cd9c413e657c29c0a2b6770b1a2c28ba4ecdd5724a5fbfbdd",
			Apply: squibble.Exec(
				`ALTER TABLE raw_query_log ADD COLUMN elapsed INTEGER NULL`,
				`DROP VIEW query_log`,
				`CREATE VIEW query_log AS SELECT author, source, query, timestamp, elapsed `+
					`FROM raw_query_log JOIN queries USING (query_id)`,
			),
		},
	},
}

// localState represetns a local database used by the service to track optional
// state information while running.
type localState struct {
	// Exclusive: Write transaction
	// Shared: Read transaction
	txmu   sync.RWMutex
	rw, ro *sql.DB
}

// newLocalState constructs a new LocalState helper for the given database URL.
func newLocalState(url string) (*localState, error) {
	if !strings.HasPrefix(url, "file:") {
		url = "file:" + url
	}
	urlRO := url + "?mode=ro"

	// Open separate copies of the database for writing query logs vs. serving
	// queries to the UI.
	rw, err := openAndPing("sqlite", url)
	if err != nil {
		return nil, err
	}
	if err := schema.Apply(context.Background(), rw); err != nil {
		rw.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	ro, err := openAndPing("sqlite", urlRO)
	if err != nil {
		rw.Close()
		return nil, err
	}

	return &localState{rw: rw, ro: ro}, nil
}

// LogQuery adds the specified query to the query log.
// The user is the login of the user originating the query, q is the source
// database and query SQL text.
// If elapsed > 0, it is recorded as the elapsed execution time.
//
// If s == nil, the query is discarded without error.
func (s *localState) LogQuery(ctx context.Context, user string, q Query, elapsed time.Duration) error {
	if s == nil {
		return nil // OK, nothing to do
	}
	s.txmu.Lock()
	defer s.txmu.Unlock()
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Look up or insert the query into the queries table to get an ID.
	var queryID int64
	err = tx.QueryRow(`SELECT query_id FROM queries WHERE query = ?`, q.Query).Scan(&queryID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`INSERT INTO QUERIES (query) VALUES (?) RETURNING (query_id)`, q.Query).Scan(&queryID)
	}
	if err != nil {
		return fmt.Errorf("update query ID: %w", err)
	}

	// Add a log entry referencing the query ID.
	ecol := sql.NullInt64{Int64: int64(elapsed / time.Microsecond), Valid: elapsed > 0}
	_, err = tx.Exec(`INSERT INTO raw_query_log (author, source, query_id, elapsed) VALUES (?, ?, ?, ?)`,
		user, q.Source, queryID, ecol)
	if err != nil {
		return fmt.Errorf("update query log: %w", err)
	}

	return tx.Commit()
}

// Query satisfies part of the Queryable interface. It supports only read queries.
func (s *localState) Query(ctx context.Context, query string, params ...any) (RowSet, error) {
	s.txmu.RLock()
	defer s.txmu.RUnlock()
	return s.ro.QueryContext(ctx, query, params...)
}

// Close satisfies part of the Queryable interface.  For this database the
// implementation is a no-op without error.
func (*localState) Close() error { return nil }
