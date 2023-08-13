# TailSQL

http://tailsql-dev?q=meta:named

TailSQL is a self-contained SQL playground service that runs on [Tailscale](https://tailscale.com).
It permits users to query SQL databases from a basic web-based UI, with support for any database
that can plug in to the Go [`database/sql`](https://godoc.org/database/sql) package.

## Running Locally

Run the commands below from a checkout of https://github.com/tailscale/tailsql.

To run locally, you will need a SQLite database to serve data from. If you do
not already have one, you can create one using the test data for this package:

```shell
# Creates test.db in the current working directory.
sqlite3 test.db -init ./server/tailsql/testdata/init.sql .quit
```

Now build the `tailsql` tool, and create a HuJSON (JWCC) configuration file for it:

```shell
go build ./cmd/tailsql

# The --init-config flag generates a stub config pointing to "test.db".
./tailsql --init-config demo.conf
```

Feel free to edit this configuration file to suit your tastes. The file encodes
an [Options](./server/tailsql/options.go#L27) value. Once you are satisfied, run:

```shell
# The --local flag starts an HTTP server on localhost.
./tailsql --local 8080 --config demo.conf
```

This starts up the server on localhost. Visit the UI at http://localhost:8080,
or call it from the command-line using `curl`:

```shell
# Note that you must provide a Sec-Tailsql header with API calls.

# Fetch output as comma-separated values.
curl -s -H 'sec-tailsql: 1' http://localhost:8080/csv --url-query 'q=select * from users'

# Fetch output as JSON objects.
curl -s -H 'sec-tailsql: 1' http://localhost:8080/json --url-query 'q=select location, count(*) n from users where location is not null group by location order by n desc'

# Check the query log.
curl -s -H 'sec-tailsql: 1' http://localhost:8080/json --url-query 'q=select * from query_log' --url-query src=local
```

## Running on Tailscale

To run as a Tailscale node, make sure the `"hostname"` field is set in the
configuration file, then run `tailsql` without the `--local` flag.

Note that the first time you start up a node, you may need to provide a key to
authorize your node on the tailnet, e.g.:

```shell
# Get a key from https://login.tailscale.com/admin/settings/keys.
# The first time you start the node, provide the key via the TS_AUTHKEY environment.
TS_AUTHKEY=tskey-XXXX ./tailsql --config demo.conf

# Note: Omit --local to start on Tailscale.
```

Subsequent runs can omit the key.
