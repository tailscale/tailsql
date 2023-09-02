// Package tailsql implements a basic client for the TailSQL service.
package tailsql

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	server "github.com/tailscale/tailsql/server/tailsql"
)

// A Client is a client for the TailSQL HTTP API.
type Client struct {
	// Server is the base URL of the server hosting the API (required).
	Server string

	// DoHTTP implements the equivalent of http.Client.Do.
	// If nil, http.DefaultClient.Do is used.
	DoHTTP func(*http.Request) (*http.Response, error)
}

// ServerInfo is a record of server information reported by the TailSQL meta API.
type ServerInfo struct {
	// Sources are the named data sources supported by the server.
	Sources []server.DBSpec `json:"sources,omitempty"`

	// Links are links exposed in the UI>
	Links []server.UILink `json:"links,omitempty"`

	// QueryTimeout is the maximimum runtime allowed for a single query.
	QueryTimeout server.Duration `json:"queryTimeout,omitempty"`
}

// ServerInfo returns metadata about the server.
func (c Client) ServerInfo(ctx context.Context) (*ServerInfo, error) {
	rc, err := callGET(ctx, c.DoHTTP, c.Server+"/meta", nil)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var meta struct {
		Data *ServerInfo `json:"meta"`
	}
	if err := json.NewDecoder(rc).Decode(&meta); err != nil {
		return nil, err
	}
	return meta.Data, nil
}

// QueryJSON invokes an SQL query against the specified dataSrc and returns
// matching rows as a slice of the specified type T. A pointer to T must be a
// valid input to json.Unmarshal.
func QueryJSON[T any](ctx context.Context, c Client, dataSrc, sql string) ([]T, error) {
	rc, err := callGET(ctx, c.DoHTTP, c.Server+"/json", url.Values{
		"src": {dataSrc},
		"q":   {sql},
	})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	var out []T
	for i := 1; ; i++ {
		var row T
		if err := dec.Decode(&row); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, fmt.Errorf("decode row %d: %w", i, err)
		}
		out = append(out, row)
	}
}

// Rows is the result of a successful Query call.
type Rows struct {
	Columns []string   // column names
	Rows    [][]string // rows of columns
}

// Query invokes an SQL query against the specified dataSrc.
func (c Client) Query(ctx context.Context, dataSrc, sql string) (Rows, error) {
	rc, err := callGET(ctx, c.DoHTTP, c.Server+"/csv", url.Values{
		"src": {dataSrc},
		"q":   {sql},
	})
	if err != nil {
		return Rows{}, err
	}
	defer rc.Close()
	rows, err := csv.NewReader(rc).ReadAll()
	if err != nil {
		return Rows{}, fmt.Errorf("decoding rows: %w", err)
	} else if len(rows) == 0 {
		return Rows{}, nil
	}
	return Rows{
		Columns: rows[0],
		Rows:    rows[1:],
	}, nil
}

func callGET(ctx context.Context, do func(*http.Request) (*http.Response, error), base string, query url.Values) (io.ReadCloser, error) {
	url := base
	if len(query) != 0 {
		url += "?" + query.Encode()
	}
	if do == nil {
		do = http.DefaultClient.Do
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Sec-Tailsql", "ok")
	rsp, err := do(req)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", base, err)
	}
	if rsp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(rsp.Body)
		rsp.Body.Close()
		line := strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
		return nil, errors.New(line)
	}
	return rsp.Body, nil
}
