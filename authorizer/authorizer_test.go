// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package authorizer_test

import (
	"testing"

	"github.com/tailscale/tailsql/authorizer"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

const tailsqlCap = "tailscale.com/cap/tailsql"

var (
	taggedNode = &apitype.WhoIsResponse{
		Node: &tailcfg.Node{Name: "fake.ts.net", Tags: []string{"tag:special"}},
		UserProfile: &tailcfg.UserProfile{
			ID: 1, LoginName: "user@example.com", DisplayName: "Some P. User",
		},
		CapMap: tailcfg.PeerCapMap{
			tailsqlCap: []tailcfg.RawMessage{
				`{"src":["main","alt"]}`,
			},
		},
	}
	loggedInUser = &apitype.WhoIsResponse{
		Node: &tailcfg.Node{Name: "fake.ts.net"},
		UserProfile: &tailcfg.UserProfile{
			ID: 1, LoginName: "user@example.com", DisplayName: "Some P. User",
		},
		CapMap: tailcfg.PeerCapMap{
			tailsqlCap: []tailcfg.RawMessage{
				`{"src":["main"]}`,
			},
		},
	}
)

func TestACLGrants(t *testing.T) {
	auth := authorizer.ACLGrants(t.Logf)
	tests := []struct {
		src string
		rsp *apitype.WhoIsResponse
		ok  bool
	}{
		{"main", taggedNode, true},
		{"alt", taggedNode, true},
		{"other", taggedNode, false},
		{"main", loggedInUser, true},
		{"alt", loggedInUser, false},
		{"other", loggedInUser, false},
	}

	for _, tc := range tests {
		err := auth(tc.src, tc.rsp)
		if tc.ok && err != nil {
			t.Errorf("Authorize %q: unexpected error: %v", tc.src, err)
		} else if !tc.ok && err == nil {
			t.Errorf("Authorize %q: got nil, want error", tc.src)
		}
	}
}

func TestMap(t *testing.T) {
	auth := authorizer.Map{
		"main":  []string{"user@example.com"},  // access by this user
		"other": []string{"other@example.com"}, // access by some other user
		"none":  nil,                           // no access by anyone
	}.Authorize(t.Logf)
	tests := []struct {
		src string
		rsp *apitype.WhoIsResponse
		ok  bool
	}{
		// Tagged nodes should not get any access.
		{"main", taggedNode, false},
		{"alt", taggedNode, false},
		{"other", taggedNode, false},

		{"main", loggedInUser, true},   // explicitly granted
		{"alt", loggedInUser, true},    // by default
		{"other", loggedInUser, false}, // this user is not on the list
		{"none", loggedInUser, false},  // empty access list
	}

	for _, tc := range tests {
		err := auth(tc.src, tc.rsp)
		if tc.ok && err != nil {
			t.Errorf("Authorize %q: unexpected error: %v", tc.src, err)
		} else if !tc.ok && err == nil {
			t.Errorf("Authorize %q: got nil, want error", tc.src)
		}
	}
}
