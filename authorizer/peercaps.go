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
const tailsqlCap = "https://tailscale.com/cap/tailsql"

// PeerCaps returns an authorization function that uses peer capabilities from
// the tailnet to check access for query sources.
// If logf == nil, logs are sent to log.Printf.
//
// TODO(creachadair): As of 10-Aug-2023 peer capabilities are an experimental
// feature that only works on tailnets where enaled.
func PeerCaps(logf logger.Logf) func(string, *apitype.WhoIsResponse) error {
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
