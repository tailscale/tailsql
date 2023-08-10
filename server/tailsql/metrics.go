// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package tailsql

import (
	"expvar"

	"tailscale.com/metrics"
)

var (
	apiRequestCount      = &metrics.LabelMap{Label: "type"}
	htmlRequestCount     = new(expvar.Int)
	csvRequestCount      = new(expvar.Int)
	jsonRequestCount     = new(expvar.Int)
	metaRequestCount     = new(expvar.Int)
	apiErrorCount        = &metrics.LabelMap{Label: "type"}
	authErrorCount       = new(expvar.Int)
	badRequestErrorCount = new(expvar.Int)
	internalErrorCount   = new(expvar.Int)
	queryErrorCount      = new(expvar.Int)
)

func init() {
	apiRequestCount.Set("html", htmlRequestCount)
	apiRequestCount.Set("csv", csvRequestCount)
	apiRequestCount.Set("json", jsonRequestCount)
	apiRequestCount.Set("meta", metaRequestCount)

	apiErrorCount.Set("auth", authErrorCount)
	apiErrorCount.Set("bad_request", badRequestErrorCount)
	apiErrorCount.Set("internal", internalErrorCount)
	apiErrorCount.Set("query", queryErrorCount)
}

func addMetrics(m *expvar.Map) {
	m.Set("counter_api_request", apiRequestCount)
	m.Set("counter_api_error", apiErrorCount)
}
