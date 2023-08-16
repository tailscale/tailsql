// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package uirules_test

import (
	"html/template"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/tailscale/tailsql/server/tailsql"
	"github.com/tailscale/tailsql/uirules"
)

func TestRules(t *testing.T) {
	tests := []struct {
		rule  tailsql.UIRewriteRule
		input string
		ok    bool
		want  any
	}{
		{uirules.StripeIDLink, "cus_abcdef", true,
			template.HTML(`<a href="https://dashboard.stripe.com/customers/cus_abcdef" ` +
				`title="customer details in Stripe" referrerpolicy=no-referrer rel=noopener>` +
				`cus_abcdef</a>`)},
		{uirules.StripeIDLink, "in_12345", true,
			template.HTML(`<a href="https://dashboard.stripe.com/invoices/in_12345" ` +
				`title="invoice details in Stripe" referrerpolicy=no-referrer rel=noopener>` +
				`in_12345</a>`)},
		{uirules.StripeIDLink, "nonesuch", false, nil},

		{uirules.FormatSQLSource, "select x < 3;", true,
			template.HTML(`<code><pre>select x &lt; 3;</pre></code>`)},
		{uirules.FormatSQLSource, "select x <> 3 from y", true,
			template.HTML(`<code><pre>select x &lt;&gt; 3 from y</pre></code>`)},
		{uirules.FormatSQLSource, "create table t (x);", true,
			template.HTML(`<code><pre>create table t (x);</pre></code>`)},
		{uirules.FormatSQLSource, "it is easier to create than destroy", false, nil},

		{uirules.FormatJSONText, "invalid null", false, nil},
		{uirules.FormatJSONText, "[null]", true, template.HTML(`<tt>[null]</tt>`)},
		{uirules.FormatJSONText, `{"incomplete":`, false, nil},
		{uirules.FormatJSONText, "unrelated", false, nil},
		{uirules.FormatJSONText, `{"x":"&"}`, true,
			template.HTML(`<tt>{&#34;x&#34;:&#34;&amp;&#34;}</tt>`)},

		{uirules.LinkURLText, "unrelated", false, nil},
		{uirules.LinkURLText, "s3://non-http.src", false, nil},
		{uirules.LinkURLText, "https://invalid link", false, nil},
		{uirules.LinkURLText, "http://localhost:8080/foo?bar", true,
			template.HTML(`<a href="http://localhost:8080/foo?bar" referrerpolicy=no-referrer ` +
				`rel=noopener>http://localhost:8080/foo?bar</a>`)},
	}
	for _, tc := range tests {
		ok, got := tc.rule.CheckApply("colName", tc.input)
		if ok != tc.ok {
			t.Errorf("CheckApply rule=%v OK: got %v, want %v", tc.rule, ok, tc.ok)
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("CheckApply rule=%v result (-want, +got):\n%s", tc.rule, diff)
		}
	}
}
