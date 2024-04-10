// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	txmu sync.RWMutex
	db   *sql.DB
}

// newLocalState constructs a new LocalState helper using the given database.
func newLocalState(db *sql.DB) (*localState, error) {
	if err := schema.Apply(context.Background(), db); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	return &localState{db: db}, nil
}

// LogQuery adds the specified query to the query log.
// The user is the login of the user originating the query, source is the
// target database, and query is the SQL of the query itself.
// If elapsed > 0, it is recorded as the elapsed execution time.
//
// If s == nil, the query is discarded without error.
func (s *localState) LogQuery(ctx context.Context, user, source, query string, elapsed time.Duration) error {
	if s == nil {
		return nil // OK, nothing to do
	}
	s.txmu.Lock()
	defer s.txmu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Look up or insert the query into the queries table to get an ID.
	var queryID int64
	err = tx.QueryRow(`SELECT query_id FROM queries WHERE query = ?`,
		query).Scan(&queryID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`INSERT INTO QUERIES (query) VALUES (?) RETURNING (query_id)`,
			query).Scan(&queryID)
	}
	if err != nil {
		return fmt.Errorf("update query ID: %w", err)
	}

	// Add a log entry referencing the query ID.
	ecol := sql.NullInt64{Int64: int64(elapsed / time.Microsecond), Valid: elapsed > 0}
	_, err = tx.Exec(`INSERT INTO raw_query_log (author, source, query_id, elapsed) VALUES (?, ?, ?, ?)`,
		user, source, queryID, ecol)
	if err != nil {
		return fmt.Errorf("update query log: %w", err)
	}

	return tx.Commit()
}
