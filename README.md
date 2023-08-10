# TailSQL

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

Feel free to edit this configuration file to suit your tastes, then run:

```shell
# The --local flag starts an HTTP server on localhost.
./tailsql --local 8080 --config demo.conf
```

This starts up the server on localhost. Visit the UI at http://localhost:8080,
or call it from the command-line using `curl`:

```shell
# Fetch output as comma-separated values.
curl -s http://localhost:8080/csv --url-query 'q=select * from users'

# Fetch output as JSON objects.
curl -s http://localhost:8080/json --url-query 'q=select location, count(*) n from users where location is not null group by location order by n desc'

# Check the query log.
curl -s http://localhost:8080/json --url-query 'q=select * from query_log' --url-query src=local
```
