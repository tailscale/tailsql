// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package authorizer implements access control helpers for tailsql.
package authorizer

import (
	"fmt"
	"log"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/types/logger"
)

// A Map maps source labels to lists of usernames who are granted access to
// issue queries against that source.
type Map map[string][]string

// Authorize returns an authorization function suitable for tailsql.Options.
//
// If a source label is not present in the map, all logged-in users are
// permitted to query the source.  If a source label is present in the map,
// only logged-in users in the list are permitted to query the source.  Tagged
// nodes are not permitted to query any source.
//
// If logf == nil, logs are sent to log.Printf.
func (m Map) Authorize(logf logger.Logf) func(string, *apitype.WhoIsResponse) error {
	if logf == nil {
		logf = log.Printf
	}
	return func(src string, who *apitype.WhoIsResponse) (err error) {
		caller := who.UserProfile.LoginName
		if who.Node.IsTagged() {
			caller = who.Node.Name
		}
		defer func() {
			logf("[tailsql] auth src=%q who=%q err=%v", src, caller, err)
		}()
		if who.Node.IsTagged() {
			return fmt.Errorf("tagged nodes cannot query %q", src)
		}
		users, ok := m[src]
		if !ok {
			return nil // no restriction on this source
		}
		for _, u := range users {
			if u == caller {
				return nil // this user is permitted access
			}
		}
		return fmt.Errorf("not authorized for access to %q", src)
	}
}
