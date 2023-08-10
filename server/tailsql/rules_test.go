package tailsql_test

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"regexp"

	"github.com/tailscale/tailsql/server/tailsql"
)

// An ordered list of rewrite rules for rendering text for the UI.
// If a value matches the regular expression, the function is called with the
// original string and the match results to generate a replacement value.
var testUIRules = []tailsql.UIRewriteRule{
	// Rewrite Stripe customer and invoice IDs to include a link to Stripe.
	{
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
	},
	// Decorate references to Go documentation.
	{
		Value: regexp.MustCompile(`^godoc:(.+)$`),
		Apply: func(col, s string, match []string) any {
			log.Printf("MJF :: col=%q s=%q match=%+v", col, s, match)
			return template.HTML(fmt.Sprintf(
				`<a href="https://godoc.org?q=%[1]s"`+
					` title="look up Go documentation"`+
					`>%[1]s</a>`, match[1],
			))
		},
	},
	// Treat text that looks like SQL as preformatted.
	{
		Value: regexp.MustCompile(`(?is)\b(select\s+.*from|create\s+(table|view))\b`),
		Apply: func(col, s string, _ []string) any {
			return template.HTML(fmt.Sprintf(`<code><pre>%s</pre></code>`, s))
		},
	},
	// Render text that looks like JSON as fixed-width (teletype).
	{
		Value: regexp.MustCompile(`null|true|false|[{},:\[\]]`),
		Apply: func(col, s string, _ []string) any {
			if json.Valid([]byte(s)) {
				esc := template.HTMLEscapeString(s)
				return template.HTML(fmt.Sprintf(`<tt>%s</tt>`, esc))
			}
			return s
		},
	},
}
