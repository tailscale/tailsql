// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql

import (
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/tailscale/tailsql/authorizer"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestCheckQuerySyntax(t *testing.T) {
	tests := []struct {
		query string
		ok    bool
	}{
		{"", true},
		{"  ", true},

		// Basic disallowed stuff.
		{`ATTACH DATABASE "foo" AS bar;`, false},
		{`DETACH DATABASE bar;`, false},

		// Things that should not be disallowed despite looking sus.
		{`SELECT 'ATTACH DATABASE "foo" AS bar;' FROM hell;`, true},
		{`-- attach database not really
        select * from a join b using (uid); -- ok  `, true},

		// Things that should be disallowed despite being sneaky.
		{` -- hide me
        attach -- blah blah
          database "bad" -- you can't see me
        as demon_spawn;`, false},
	}
	for _, tc := range tests {
		err := checkQuerySyntax(tc.query)
		if tc.ok && err != nil {
			t.Errorf("Query %q: unexpected error: %v", tc.query, err)
		} else if !tc.ok && err == nil {
			t.Errorf("Query %q: unexpectedly passed", tc.query)
		}
	}
}

func TestOptions(t *testing.T) {
	data, err := os.ReadFile("testdata/config.hujson")
	if err != nil {
		t.Fatalf("Read config: %v", err)
	}
	var opts Options
	if err := UnmarshalOptions(data, &opts); err != nil {
		t.Fatalf("Parse config: %v", err)
	}
	want := Options{
		Hostname: "tailsql-test",
		Sources: []DBSpec{{
			Source: "test1",
			Label:  "Test DB 1",
			Driver: "sqlite",
			URL:    "file::memory:",
		}, {
			Source:  "test2",
			Label:   "Test DB 2",
			Driver:  "sqlite",
			KeyFile: "testdata/fake-test.key",
		}},
		UILinks: []UILink{
			{Anchor: "foo", URL: "http://foo"},
			{Anchor: "bar", URL: "http://bar"},
		},
	}
	if diff := cmp.Diff(want, opts); diff != "" {
		t.Errorf("Parsed options (-want, +got)\n%s", diff)
	}
	opts.Authorize = authorizer.ACLGrants(nil)

	// Test that we can populate options from the config.
	t.Run("Options", func(t *testing.T) {
		dbs, err := opts.openSources(nil)
		if err != nil {
			t.Fatalf("Options: unexpected error: %v", err)
		}

		// The handles should be equinumerous and in the same order as the config.
		for i, h := range dbs {
			if got, want := h.Source(), opts.Sources[i].Source; got != want {
				t.Errorf("Database %d: got src %q, want %q", i+1, got, want)
			}
			h.close()
		}
	})

	// Test that the authorizer works.
	t.Run("Authorize", func(t *testing.T) {
		const tailsqlCap = "tailscale.com/cap/tailsql"

		admin := &apitype.WhoIsResponse{
			Node: new(tailcfg.Node), // must be non-nil in a valid response
			UserProfile: &tailcfg.UserProfile{
				ID:          12345,
				LoginName:   "admin@example.com",
				DisplayName: "A. D. Ministratrix",
			},
			CapMap: tailcfg.PeerCapMap{
				tailsqlCap: []tailcfg.RawMessage{
					`{"src":["test1","test2"]}`,
				},
			},
		}
		hoiPolloi := &apitype.WhoIsResponse{
			Node: new(tailcfg.Node), // must be non-nil in a valid response
			UserProfile: &tailcfg.UserProfile{
				ID:          67890,
				LoginName:   "pian@example.com",
				DisplayName: "P. Ian McWhorker",
			},
			CapMap: tailcfg.PeerCapMap{
				tailsqlCap: []tailcfg.RawMessage{
					`{"src":["test2"]}`,
				},
			},
		}
		auth := opts.authorize()

		// test1 has access only to admin.
		if err := auth("test1", admin); err != nil {
			t.Errorf("Authorize admin for test1 failed: %v", err)
		}
		if err := auth("test1", hoiPolloi); err == nil {
			t.Error("Authorize other for test1 should not have succeeded")
		}

		// test2 has acces to any user.
		if err := auth("test2", admin); err != nil {
			t.Errorf("Authorize admin for test2 failed: %v", err)
		}
		if err := auth("test2", hoiPolloi); err != nil {
			t.Errorf("Authorize other for test2 failed: %v", err)
		}
	})
}
