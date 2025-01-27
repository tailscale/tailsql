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
			for _, s := range rule.DataSrc {
				if s == "*" || s == dataSrc {
					return nil
				}
			}
		}
		return fmt.Errorf("not authorized for access to %q", dataSrc)
	}
}
