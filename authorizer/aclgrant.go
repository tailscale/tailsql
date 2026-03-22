// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package authorizer

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

// tailsqlCap is the default name of the tailsql capability.
const tailsqlCap = "tailscale.com/cap/tailsql"

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
		if err != nil || len(rules) == 0 {
			return errors.New("not authorized for access to tailsql")
		}
		for _, rule := range rules {
			for _, grant := range rule.DataSrc {
				if MatchSource(grant, dataSrc) {
					return nil
				}
			}
		}
		return fmt.Errorf("not authorized for access to %q", dataSrc)
	}
}

// MatchSource reports whether grant pattern 'grant' matches dataSrc.
// It supports exact matches, "*" to match everything, and prefix patterns
// like "shard*" where the pattern ends in "*" and the prefix must match.
func MatchSource(grant, dataSrc string) bool {
	if grant == "*" || grant == dataSrc {
		return true
	}
	if prefix, ok := strings.CutSuffix(grant, "*"); ok && len(prefix) > 0 {
		return strings.HasPrefix(dataSrc, prefix)
	}
	return false
}
