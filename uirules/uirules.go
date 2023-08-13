// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Package uirules defines useful UIRewriteRule values for use with the tailsql
// server package.
package uirules

import (
	"encoding/json"
	"fmt"
	"html/template"
	"regexp"

	"github.com/tailscale/tailsql/server/tailsql"
)

// StripeIDLink is a UI rewrite rule that wraps Stripe customer and invoice ID
// strings to the Stripe dashboard.
var StripeIDLink = tailsql.UIRewriteRule{
	Value: regexp.MustCompile(`^(cus_|in_1)\w+$`),
	Apply: func(col, s string, m []string) any {
		var kind string
		switch m[1] {
		case "cus_":
			kind = "customer"
		case "in_1":
			kind = "invoice"
		default:
			return s
		}
		return template.HTML(fmt.Sprintf(
			`<a href="https://dashboard.stripe.com/%[2]ss/%[1]s" `+
				`title="%[2]s details in Stripe" `+
				`referrerpolicy=no-referrer rel=noopener>%[1]s</a>`,
			s, kind))
	},
}

// FormatSQLSource is a UI rewrite rule to render SQL query text preformatted.
var FormatSQLSource = tailsql.UIRewriteRule{
	Value: regexp.MustCompile(`(?is)\b(select\s+.*from|create\s+(table|view))\b`),
	Apply: func(col, s string, _ []string) any {
		esc := template.HTMLEscapeString(s)
		return template.HTML(fmt.Sprintf(`<code><pre>%s</pre></code>`, esc))
	},
}

// FormatJSONText is a UI rewrite rule to render JSON text preformatted.
var FormatJSONText = tailsql.UIRewriteRule{
	Value: regexp.MustCompile(`null|true|false|[{},:\[\]]`),
	Apply: func(col, s string, _ []string) any {
		if json.Valid([]byte(s)) {
			esc := template.HTMLEscapeString(s)
			return template.HTML(fmt.Sprintf(`<tt>%s</tt>`, esc))
		}
		return s
	},
}
