// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package authorizer

import (
	"errors"
	"fmt"
	"log"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

// tailsqlCap is the default name of the tailsql capability.
const tailsqlCap = "tailscale.com/cap/tailsql"

// PeerCaps is a temporary migration alias for ACLGrants.
// Deprecated: Use ACLGrants directly for new code.
func PeerCaps(logf logger.Logf) func(string, *apitype.WhoIsResponse) error {
	return ACLGrants(logf)
}

// ACLGrants returns an authorization function that uses ACL grants from the
// tailnet to check access for query sources.
// If logf == nil, logs are sent to log.Printf.
func ACLGrants(logf logger.Logf) func(string, *apitype.WhoIsResponse) error {
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

		// TODO(creachadair): As a temporary measure to allow us to migrate
		// capability names away from the https:// prefix, if we don't get a
		// result without the prefix, try again with it. Remove this once the
		// policy has been updated on the server side.
		if err == nil && len(rules) == 0 {
			rules, err = tailcfg.UnmarshalCapJSON[rule](who.CapMap, "https://"+tailsqlCap)
		}
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
