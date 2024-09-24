// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/setec/client/setec"
	"github.com/tailscale/setec/setectest"
	"github.com/tailscale/tailsql/authorizer"
	"github.com/tailscale/tailsql/server/tailsql"
	"github.com/tailscale/tailsql/uirules"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"

	_ "embed"

	_ "modernc.org/sqlite"
)

//go:embed testdata/init.sql
var initSQL string

func mustInitSQLite(t *testing.T) (url string, _ *sql.DB) {
	t.Helper()
	url = "file:" + filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", url)
	if err != nil {
		t.Fatalf("Open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(initSQL); err != nil {
		t.Fatalf("Initialize database: %v", err)
	}
	return url, db
}

func mustGetRequest(t *testing.T, url string, headers ...string) *http.Request {
	t.Helper()
	if len(headers)%2 != 0 {
		t.Fatal("Invalid header list")
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("NewRequest %q: %v", url, err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return req
}

func mustGetFail(t *testing.T, cli *http.Client, url string, want int, headers ...string) {
	t.Helper()
	req := mustGetRequest(t, url, headers...)
	rsp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Get %q: unexpected error: %v", url, err)
	} else if got := rsp.StatusCode; got != want {
		t.Fatalf("Get %q: got status %v, want %v", url, got, want)
	}
}

func mustGet(t *testing.T, cli *http.Client, url string, headers ...string) []byte {
	t.Helper()
	req := mustGetRequest(t, url, headers...)
	req.AddCookie(&http.Cookie{Name: "tailsqlQuery", Value: "1"})
	rsp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Get %q failed: %v", url, err)
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		t.Fatalf("Get %q: status code %d", url, rsp.StatusCode)
	}
	data, err := io.ReadAll(rsp.Body)
	if err != nil {
		t.Fatalf("Get %q: read body: %v", url, err)
	}
	return data
}

// An ordered list of rewrite rules for rendering text for the UI.
// If a value matches the regular expression, the function is called with the
// original string and the match results to generate a replacement value.
var testUIRules = []tailsql.UIRewriteRule{
	uirules.StripeIDLink,
	uirules.FormatSQLSource,
	uirules.FormatJSONText,
	uirules.LinkURLText,

	// Decorate references to Go documentation.
	{
		Value: regexp.MustCompile(`^godoc:(.+)$`),
		Apply: func(col, s string, match []string) any {
			return template.HTML(fmt.Sprintf(
				`<a href="https://godoc.org?q=%[1]s"`+
					` title="look up Go documentation"`+
					`>%[1]s</a>`, match[1],
			))
		},
	},

	// This rule matches what the previous one did, to test that we stop once we
	// find a matching rule.
	{
		Value: regexp.MustCompile(`^godoc:.+$`),
		Apply: func(col, s string, match []string) any {
			return "some bogus nonsense"
		},
	},
}

func TestSecrets(t *testing.T) {
	// Register a fake driver so we can probe for connection URLs.
	// We have to use a new name each time, because there is no way to
	// unregister and duplicate names trigger a panic.
	driver := new(fakeDriver)
	driverName := fmt.Sprintf("%s-driver-%d", t.Name(), rand.Int())
	sql.Register(driverName, driver)
	t.Logf("Test driver name is %q", driverName)

	const secretName = "connection-string"
	db := setectest.NewDB(t, nil)
	db.MustPut(db.Superuser, secretName, "string 1")

	ss := setectest.NewServer(t, db, nil)
	hs := httptest.NewServer(ss.Mux)
	defer hs.Close()

	opts := tailsql.Options{
		Sources: []tailsql.DBSpec{{
			Source: "test",
			Label:  "Test Database",
			Driver: driverName,
			Secret: secretName,
		}},
		RoutePrefix: "/tsql",
	}

	// Verify we found the expected secret names in the options.
	secrets, err := opts.CheckSources()
	if err != nil {
		t.Fatalf("Invalid sources: %v", err)
	}

	tick := setectest.NewFakeTicker()
	st, err := setec.NewStore(context.Background(), setec.StoreConfig{
		Client:     setec.Client{Server: hs.URL},
		Secrets:    secrets,
		PollTicker: tick,
	})
	if err != nil {
		t.Fatalf("Creating setec store: %v", err)
	}
	opts.SecretStore = st

	ts, err := tailsql.NewServer(opts)
	if err != nil {
		t.Fatalf("Creating tailsql server: %v", err)
	}
	ss.Mux.Handle("/tsql/", ts.NewMux()) // so we can call /meta below
	defer ts.Close()

	// After opening the server, the database should have the initial secret
	// value provided on initialization.
	if got, want := driver.OpenedURL, "string 1"; got != want {
		t.Errorf("Initial URL: got %q, want %q", got, want)
	}

	// Update the secret.
	db.MustActivate(db.Superuser, secretName, db.MustPut(db.Superuser, secretName, "string 2"))
	tick.Poll()

	// Make the database fetch the latest value.
	if _, err := hs.Client().Get(hs.URL + "/tsql/meta"); err != nil {
		t.Errorf("Get tailsql meta: %v", err)
	}

	// After the update, the database should have the new secret value.
	if got, want := driver.OpenedURL, "string 2"; got != want {
		t.Errorf("Updated URL: got %q, want %q", got, want)
	}
}

func TestServer(t *testing.T) {
	_, db := mustInitSQLite(t)

	const testLabel = "hapax legomenon"
	const testAnchor = "wizboggle-gobsprocket"
	const testURL = "http://bongo.com?inevitable=true"
	fc := new(fakeClient)
	fc.isLogged = true
	fc.result = &apitype.WhoIsResponse{
		Node: &tailcfg.Node{Name: "fake.ts.net"},
		UserProfile: &tailcfg.UserProfile{
			ID:          1,
			LoginName:   "someuser@example.com",
			DisplayName: "some user",
		},
		CapMap: tailcfg.PeerCapMap{
			"tailscale.com/cap/tailsql": []tailcfg.RawMessage{
				`{"src":["*"]}`,
			},
		},
	}
	s, err := tailsql.NewServer(tailsql.Options{
		LocalClient: fc,
		UILinks: []tailsql.UILink{
			{Anchor: testAnchor, URL: testURL},
		},
		CheckQuery: func(q tailsql.Query) (tailsql.Query, error) {
			// Rewrite a source named "alias" as a spelling for "main" and add a
			// comment at the front.
			if q.Source == "alias" {
				q.Source = "main"
				q.Query = "-- Hello, world\n" + q.Query
			}
			return tailsql.DefaultCheckQuery(q)
		},
		UIRewriteRules: testUIRules,
		Authorize:      authorizer.ACLGrants(nil),
		Logf:           t.Logf,
	})
	if err != nil {
		t.Fatalf("NewServer: unexpected error: %v", err)
	}
	s.SetDB("main", db, &tailsql.DBOptions{
		Label: testLabel,
		NamedQueries: map[string]string{
			"sample": fmt.Sprintf("select '%s'", testLabel),
		},
	})
	defer s.Close()

	htest := httptest.NewServer(s.NewMux())
	defer htest.Close()
	cli := htest.Client()

	t.Run("UI", func(t *testing.T) {
		q := make(url.Values)
		q.Set("q", "select location from users where name = 'alice'")
		url := htest.URL + "?" + q.Encode()
		ui := string(mustGet(t, cli, url))

		// As a rough smoke test, look for expected substrings.
		for _, want := range []string{testAnchor, testURL, testLabel, "amsterdam"} {
			if !strings.Contains(ui, want) {
				t.Errorf("Missing UI substring %q", want)
			}
		}
	})

	t.Run("UIDecoration", func(t *testing.T) {
		q := url.Values{
			"q":   {"select * from misc"},
			"src": {"alias"},
		}
		url := htest.URL + "?" + q.Encode()
		ui := string(mustGet(t, cli, url))

		// As a rough smoke test, look for expected substrings.
		for _, want := range []string{
			// The query should include its injected comment from the check function.
			`-- Hello, world`,
			// Stripe IDs should get wrapped in links.
			`<a href="https://dashboard.stripe.com/customers/cus_Fak3Cu6t0m3rId"`,
			`<a href="https://dashboard.stripe.com/invoices/in_1f4k31nv0Ic3Num83r"`,
			`<a href="https://dashboard.stripe.com/subscriptions/sub_fAk34sH3l1anDMn0tgNatKT"`,
			// JSON text should be escaped and teletyped.
			`<tt>{&#34;json&#34;:true}</tt>`,
			// SQL should be formatted verbatim.
			`<code><pre>CREATE TABLE misc (x);</pre></code>`,
			// Go documentation should link to godoc.org.
			`<a href="https://godoc.org?q=tailscale.com/tailcfg.User"`,
			// HTTP(S) URLs should be wrapped in links.
			`<a href="https://github.com?q=1&r=2" `,
			`https://github.com?q=1&amp;r=2</a>`,
		} {
			if !strings.Contains(ui, want) {
				t.Errorf("Missing UI substring %q", want)
			}
		}
		if t.Failed() {
			t.Logf("UI output:\n%s", ui)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		q := make(url.Values)
		q.Set("q", "select name from users where title = 'mascot'")
		q.Set("src", "main")
		url := htest.URL + "/json?" + q.Encode()

		const want = `{"name":"amelie"}`
		got := strings.TrimSpace(string(mustGet(t, cli, url, "sec-tailsql", "1")))
		if got != want {
			t.Errorf("JSON result: got %q, want %q", got, want)
		}
	})

	t.Run("JSON_noHeader", func(t *testing.T) {
		q := url.Values{"q": {"select * from whatever"}}
		url := htest.URL + "/json?" + q.Encode()

		mustGetFail(t, cli, url, http.StatusForbidden) // no forbidden header
	})

	t.Run("CSV", func(t *testing.T) {
		q := make(url.Values)
		q.Set("q", "select count(*) n from users")
		url := htest.URL + "/csv?" + q.Encode()

		const want = "n\n10\n" // one column, one row plus header
		got := string(mustGet(t, cli, url))
		if got != want {
			t.Errorf("CSV result: got %q, want %q", got, want)
		}
	})

	t.Run("CSV_noHeader", func(t *testing.T) {
		q := url.Values{"q": {"select * from whatever"}}
		url := htest.URL + "/csv?" + q.Encode()

		mustGetFail(t, cli, url, http.StatusForbidden) // no forbidden header
	})

	t.Run("Named", func(t *testing.T) {
		q := url.Values{"q": {"named:sample"}}
		url := htest.URL + "?" + q.Encode()
		got := string(mustGet(t, cli, url))
		if !strings.Contains(got, testLabel) {
			t.Errorf("Missing result substring %q:\n%s", testLabel, got)
		}
	})

	t.Run("UnknownNamed", func(t *testing.T) {
		q := url.Values{"q": {"named:nonesuch"}}
		url := htest.URL + "?" + q.Encode()
		got := string(mustGet(t, cli, url))
		wantError := html.EscapeString(`named query "nonesuch" not recognized`)
		if !strings.Contains(got, wantError) {
			t.Errorf("Missing result substring %q:\n%s", wantError, got)
		}
	})

	t.Run("OverLongQuery", func(t *testing.T) {
		q := url.Values{"q": {fmt.Sprintf("select '%s';", strings.Repeat("f", 4000))}}
		url := htest.URL + "/csv?" + q.Encode()

		mustGetFail(t, cli, url, http.StatusBadRequest, "sec-tailsql", "1") // query too long
	})
}

// A fakeClient implements the tailsql.LocalClient interface.
// It reports success if its argument matches addr; otherwise it reports an
// error.
type fakeClient struct {
	isLogged bool
	err      error
	result   *apitype.WhoIsResponse
}

func (f *fakeClient) WhoIs(_ context.Context, addr string) (*apitype.WhoIsResponse, error) {
	if f.err != nil {
		return nil, f.err
	} else if f.isLogged {
		return f.result, nil
	}
	return nil, nil
}

func TestAuth(t *testing.T) {
	const testUser = 1234567
	var (
		taggedNode   = &tailcfg.Node{Name: "fake.ts.net", Tags: []string{"tag:nonesuch"}}
		untaggedNode = &tailcfg.Node{Name: "fake.ts.net"}
		userProfile1 = &tailcfg.UserProfile{
			ID:          testUser,
			LoginName:   "user@fakesite.example.com",
			DisplayName: "Hermanita Q. Testwaller",
		}
	)

	_, db := mustInitSQLite(t)

	// An initially-empty fake client, which we will update between tests.
	fc := new(fakeClient)

	s, err := tailsql.NewServer(tailsql.Options{
		LocalClient: fc,

		Authorize: func(src string, wr *apitype.WhoIsResponse) error {
			if wr.Node.IsTagged() && len(wr.CapMap) == 0 {
				return errors.New("tagged node without capabilities rejected")
			} else if src == "main" {
				return nil // no authorization required for "main"
			} else if wr.UserProfile.ID == testUser {
				return nil // this user is explicitly allowed
			} else {
				return errors.New("authorization denied") // fail closed
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer: unexpected error: %v", err)
	}
	s.SetDB("main", db, &tailsql.DBOptions{
		Label: "Main test data",
	})
	defer s.Close()

	htest := httptest.NewServer(s.NewMux())
	defer htest.Close()
	cli := htest.Client()

	mustCall := func(t *testing.T, url string, want int) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			t.Fatalf("New request for %q: %v", url, err)
		}
		req.Header.Set("Sec-Tailsql", "ok")
		rsp, err := cli.Do(req)
		if err != nil {
			t.Fatalf("Get %q: unexpected error: %v", url, err)
		}
		defer rsp.Body.Close()
		if got := rsp.StatusCode; got != want {
			t.Errorf("Get %q: got %d, want %d", url, got, want)
		}
	}
	testQuery := url.Values{"q": {"select 'ok'"}}.Encode()

	// Check for a user who is not logged in.
	t.Run("NotLogged", func(t *testing.T) {
		mustCall(t, htest.URL, http.StatusUnauthorized)
	})

	fc.isLogged = true
	fc.result = &apitype.WhoIsResponse{
		Node:        taggedNode,
		UserProfile: userProfile1,
	}

	// A tagged node cannot query a source it's not granted.
	// However, anyone can get the UI with no query.
	t.Run("TaggedNode/Query", func(t *testing.T) {
		mustCall(t, htest.URL+"?"+testQuery, http.StatusForbidden)
	})
	t.Run("TaggedNode/UI", func(t *testing.T) {
		mustCall(t, htest.URL, http.StatusOK)
	})

	fc.result.Node = untaggedNode

	// Check for a valid user who is authorized.
	t.Run("ValidAuth", func(t *testing.T) {
		mustCall(t, htest.URL+"?"+testQuery, http.StatusOK)
	})

	fc.result.UserProfile.ID = 678910

	// Check for a valid user who is not authorized.
	t.Run("ValidUnauth", func(t *testing.T) {
		mustCall(t, htest.URL+"?src=other&"+testQuery, http.StatusForbidden)
	})
}

// Verify that context cancellation is correctly propagated.
// This test is specific to SQLite, but the point is to make sure the context
// plumbing in tailsql is correct.
func TestQueryTimeout(t *testing.T) {
	_, db := mustInitSQLite(t)

	// This test query runs forever until interrupted.
	const testQuery = `WITH RECURSIVE inf(n) AS (
    SELECT 1
    UNION ALL
    SELECT n+1 FROM inf
) SELECT * FROM inf WHERE n = 0`

	done := make(chan struct{})
	go func() {
		defer close(done)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		rows, err := db.QueryContext(ctx, testQuery)
		if err == nil {
			t.Errorf("QueryContext: got rows=%v, want error", rows)
		} else if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Got error %v, wanted %v", err, context.DeadlineExceeded)
		}
	}()

	select {
	case <-done:
		// OK
	case <-time.After(30 * time.Second):
		t.Fatal("Timeout waiting for query to end")
	}
}

func TestQueryable(t *testing.T) {
	_, db := mustInitSQLite(t)

	s, err := tailsql.NewServer(tailsql.Options{
		Sources: []tailsql.DBSpec{{
			Source: "quux",
			Label:  "Happy fun database",
			DB:     sqlDB{DB: db},

			// Note: Omit Driver to exercise that this is allowed when a
			// programmatic data source is given.
		}},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer s.Close()

	htest := httptest.NewServer(s.NewMux())
	defer htest.Close()
	cli := htest.Client()

	t.Run("Smoke", func(t *testing.T) {
		const testProbe = "mindlesprocket"
		q := url.Values{
			"src": {"quux"},
			"q":   {fmt.Sprintf(`select '%s'`, testProbe)},
		}
		rsp := string(mustGet(t, cli, htest.URL+"/csv?"+q.Encode()))
		if !strings.Contains(rsp, testProbe) {
			t.Errorf("Query failed: got %q, want %q", rsp, testProbe)
		}
	})
}

type sqlDB struct{ *sql.DB }

func (s sqlDB) Query(ctx context.Context, query string, params ...any) (tailsql.RowSet, error) {
	return s.DB.QueryContext(ctx, query, params...)
}

func TestRoutePrefix(t *testing.T) {
	s, err := tailsql.NewServer(tailsql.Options{
		RoutePrefix: "/sub/dir",
	})
	if err != nil {
		t.Fatalf("NewServer: unexpected error: %v", err)
	}
	defer s.Close()

	hs := httptest.NewServer(s.NewMux())
	defer hs.Close()
	cli := hs.Client()

	t.Run("NotFound", func(t *testing.T) {
		rsp, err := cli.Get(hs.URL + "/static/logo.svg")
		if err != nil {
			t.Fatalf("Get: unexpected error: %v", err)
		}
		_, err = io.ReadAll(rsp.Body)
		rsp.Body.Close()
		if err != nil {
			t.Errorf("Read body: %v", err)
		}
		if code := rsp.StatusCode; code != http.StatusNotFound {
			t.Errorf("Get: got response %v, want %v", code, http.StatusNotFound)
		}
	})

	t.Run("OK", func(t *testing.T) {
		txt := string(mustGet(t, cli, hs.URL+"/sub/dir/static/logo.svg"))
		if !strings.HasPrefix(txt, "<svg ") {
			t.Errorf("Get logo.svg: output missing SVG prefix:\n%s", txt)
		}
	})

	t.Run("UI", func(t *testing.T) {
		txt := string(mustGet(t, cli, hs.URL+"/sub/dir"))
		if !strings.Contains(txt, "Tailscale SQL Playground") {
			t.Errorf("Get UI: missing expected title:\n%s", txt)
		}
	})
}

type fakeDriver struct {
	OpenedURL string
}

func (f *fakeDriver) Open(url string) (driver.Conn, error) {
	f.OpenedURL = url
	return fakeConn{}, nil
}

// fakeConn is a fake implementation of driver.Conn to satisfy the interface,
// it will panic if actually used.
type fakeConn struct{ driver.Conn }

func (fakeConn) Close() error { return nil }
