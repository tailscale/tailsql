// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tailscale/tailsql/server/tailsql"
	"golang.org/x/exp/slices"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"

	_ "embed"

	_ "modernc.org/sqlite"
)

//go:embed testdata/init.sql
var initSQL string

func mustInitSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("Open memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(initSQL); err != nil {
		t.Fatalf("Initialize database: %v", err)
	}
	return db
}

func mustGet(t *testing.T, cli *http.Client, url string) []byte {
	rsp, err := cli.Get(url)
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

func TestServer(t *testing.T) {
	db := mustInitSQLite(t)

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
			"https://tailscale.com/cap/tailsql": []json.RawMessage{
				json.RawMessage(`{"src":["*"]}`),
			},
		},
	}
	s, err := tailsql.NewServer(tailsql.Options{
		LocalClient: fc,
		UILinks: []tailsql.UILink{
			{Anchor: testAnchor, URL: testURL},
		},
		UIRewriteRules: testUIRules,
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
		q := make(url.Values)
		q.Set("q", "select * from misc")
		url := htest.URL + "?" + q.Encode()
		ui := string(mustGet(t, cli, url))

		// As a rough smoke test, look for expected substrings.
		for _, want := range []string{
			// Stripe IDs should get wrapped in links.
			`<a href="https://dashboard.stripe.com/customers/cus_Fak3Cu6t0m3rId"`,
			`<a href="https://dashboard.stripe.com/invoices/in_1f4k31nv0Ic3Num83r"`,
			// JSON text should be escaped and teletyped.
			`<tt>{&#34;json&#34;:true}</tt>`,
			// SQL should be formatted verbatim.
			`<code><pre>CREATE TABLE misc (x);</pre></code>`,
			// Go documentation should link to godoc.org.q
			`<a href="https://godoc.org?q=tailscale.com/tailcfg.User"`,
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
		got := strings.TrimSpace(string(mustGet(t, cli, url)))
		if got != want {
			t.Errorf("JSON result: got %q, want %q", got, want)
		}
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
		userProfile2 = &tailcfg.UserProfile{
			ID:          404, // i.e., not testUser
			LoginName:   "otherbody@fakesite.example.com",
			DisplayName: "Aloysius P. von Testenschön",
			Groups:      []string{"group:special"},
		}
	)

	db := mustInitSQLite(t)

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
			} else if src == "special" && slices.Contains(wr.UserProfile.Groups, "group:special") {
				return nil // this group is explicitly allowed for "special"
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
		rsp, err := cli.Get(url)
		if err != nil {
			t.Fatalf("Get %q: unexpected error: %v", url, err)
		}
		defer rsp.Body.Close()
		if got := rsp.StatusCode; got != want {
			t.Errorf("Get %q: got %d, want %d", url, got, want)
		}
	}

	// Check for a user who is not logged in.
	t.Run("NotLogged", func(t *testing.T) {
		mustCall(t, htest.URL, http.StatusUnauthorized)
	})

	fc.isLogged = true
	fc.result = &apitype.WhoIsResponse{
		Node:        taggedNode,
		UserProfile: userProfile1,
	}

	// Check for a response for a tagged node not granted access by
	// capabilities.
	t.Run("TaggedNode", func(t *testing.T) {
		mustCall(t, htest.URL, http.StatusForbidden)
	})

	fc.result.Node = untaggedNode

	// Check for a valid user who is authorized.
	t.Run("ValidAuth", func(t *testing.T) {
		mustCall(t, htest.URL, http.StatusOK)
	})

	fc.result.UserProfile.ID = 678910

	// Check for a valid user who is not authorized.
	t.Run("ValidUnauth", func(t *testing.T) {
		mustCall(t, htest.URL+"?src=other", http.StatusForbidden)
	})

	// Authorization by group fails for a user not in the group.
	t.Run("NonGroupMember", func(t *testing.T) {
		mustCall(t, htest.URL+"?src=special", http.StatusForbidden)
	})

	fc.result.UserProfile = userProfile2

	// Authorization succeeds for a user in the group.
	t.Run("GroupMember", func(t *testing.T) {
		mustCall(t, htest.URL+"?src=special", http.StatusOK)
	})
}
