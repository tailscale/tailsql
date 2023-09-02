package tailsql_test

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/tailscale/tailsql/client/tailsql"
	server "github.com/tailscale/tailsql/server/tailsql"

	_ "modernc.org/sqlite"
)

func TestClient(t *testing.T) {
	// Set up a small database to exercise queries.
	dbPath := "file:" + filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`create table test ( id integer, value text )`); err != nil {
		t.Fatalf("Create table: %v", err)
	}
	if _, err := db.Exec(`insert into test (id, value) values
       (1, 'apple'), (2, 'pear'), (3, 'plum'), (4, 'cherry')`); err != nil {
		t.Fatalf("Insert data: %v", err)
	}

	// Start a tailsql server with known values for testing.
	s, err := server.NewServer(server.Options{
		UILinks: []server.UILink{
			{Anchor: "anchor", URL: "url"},
		},
		Sources: []server.DBSpec{{
			Source: "main",
			Label:  "label",
			Driver: "sqlite",
			Named: map[string]string{
				"foo": "select count(*) n from test",
			},
			URL: dbPath,
		}},
		QueryTimeout: server.Duration(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer s.Close()

	hs := httptest.NewServer(s.NewMux())
	defer hs.Close()

	cli := tailsql.Client{Server: hs.URL, DoHTTP: hs.Client().Do}
	ctx := context.Background()

	t.Run("ServerInfo", func(t *testing.T) {
		si, err := cli.ServerInfo(ctx)
		if err != nil {
			t.Fatalf("ServerInfo failed: %v", err)
		}
		if diff := cmp.Diff(si, &tailsql.ServerInfo{
			Sources: []server.DBSpec{
				{Source: "main", Label: "label", Named: map[string]string{
					"foo": "select count(*) n from test",
				}},
			},
			Links:        []server.UILink{{Anchor: "anchor", URL: "url"}},
			QueryTimeout: server.Duration(5 * time.Second),
		}); diff != "" {
			t.Errorf("Result (-got, +want):\n%s", diff)
		}
	})

	t.Run("QueryJSON", func(t *testing.T) {
		type row struct {
			ID    int    `json:"id"`
			Value string `json:"value"`
		}

		got, err := tailsql.QueryJSON[row](ctx, cli, "main", `select id, value from test order by 1`)
		if err != nil {
			t.Errorf("QueryJSON failed: %v", err)
		}
		if diff := cmp.Diff(got, []row{
			{1, "apple"}, {2, "pear"}, {3, "plum"}, {4, "cherry"},
		}); diff != "" {
			t.Errorf("Result (-got, +want):\n%s", diff)
		}
	})

	t.Run("QueryJSON_Named", func(t *testing.T) {
		type row struct {
			Count int `json:"n"`
		}
		got, err := tailsql.QueryJSON[row](ctx, cli, "main", `named:foo`)
		if err != nil {
			t.Errorf("QueryJSON failed: %v", err)
		}
		if diff := cmp.Diff(got, []row{{4}}); diff != "" {
			t.Errorf("Result (-got, +want):\n%s", diff)
		}
	})

	t.Run("Query", func(t *testing.T) {
		rows, err := cli.Query(ctx, "main", `select id, value from test order by 1`)
		if err != nil {
			t.Errorf("Query failed: %v", err)
		}
		if diff := cmp.Diff(rows, tailsql.Rows{
			Columns: []string{"id", "value"},
			Rows: [][]string{
				{"1", "apple"}, {"2", "pear"}, {"3", "plum"}, {"4", "cherry"},
			},
		}); diff != "" {
			t.Errorf("Result (-got, +want):\n%s", diff)
		}
	})

	t.Run("Query_Named", func(t *testing.T) {
		rows, err := cli.Query(ctx, "main", `named:foo`)
		if err != nil {
			t.Errorf("Query failed: %v", err)
		}
		if diff := cmp.Diff(rows, tailsql.Rows{
			Columns: []string{"n"},
			Rows:    [][]string{{"4"}},
		}); diff != "" {
			t.Errorf("Result (-got, +want):\n%s", diff)
		}
	})

	t.Run("QueryError", func(t *testing.T) {
		rows, err := cli.Query(ctx, "nonesuch", "select 1")
		if err == nil {
			t.Errorf("Got %+v, want error", rows)
		} else {
			t.Logf("Got expected error: %v", err)
		}
	})
}
