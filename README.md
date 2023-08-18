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
curl -s -H 'sec-tailsql: 1' http://localhost:8080/json --url-query 'q=select * from query_log' --url-query src=self
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

## Configuration and Extension

The command-line tool in `cmd/tailsql` implements a standalone SQL playground server with some default options. The [tailsql][tailsql] package is designed to work as a library, however, and for specific use cases you will probably want to build your own server binary. It is also possible to run a TailSQL server "inside" another process, using [tsnet][tsnet]. The example binary includes code you can crib from to suit your own needs.

The basic workflow to set up a new TailSQL server is:

1. Create the server with your desired [`tailsql.Options`][options]:

    ```go
    tsql, err := tailsql.NewServer(tailsql.Options{
       // ...
    })
    ```

2. Serve the mux via HTTP:

    ```go
    http.ListenAndServe("localhost:8080", tsql.NewMux())
    ```

Note that the server does not do any authentication or encryption of its own. If you want to share the playground beyond your own local machine, you will need to provide appropriate access controls. One easy way to do this is using Tailscale (which handles encryption, access control, and TLS), but if you prefer you can handle those details separately.

### Database Drivers

By default, only SQLite databases are supported (via https://modernc.org/sqlite). Any driver compatible with the [database/sql][dbsql] package should work, however. To add drivers, add additional imports to the main package (`tailsql.go`) and recompile the program.

Adding imports:

```go
import (
  // ...

  // If you want to support other source types with this tool, you will need
  // to import other database drivers below.

  // SQLite driver for database/sql.
  _ "modernc.org/sqlite"

  // PostgreSQL driver for database/sql.
  _ "github.com/lib/pq"
)
```

Rebuilding the program:

```shell
go build ./cmd/tailsql
```

To configure a database using this driver, populate the `Driver` field of the `DBSpec` in the options:

```go
opts := tailsql.Options{
   Sources: []tailsql.DBSpec{{
     Source: "info",         // used to select this source in queries (src=info)
     Label:  "Information",  // a human-readable label, shown in the source picker
     Driver: "postgres",     // must be a driver registered with database/sql

     URL: connectionString,  // the connection string for the database
   }},
}
```

Any number of sources can be configured this way. It is also possible to add new data sources dynamically at runtime using the `SetDB` method of the server. It is _not_ currently possible to remove data sources once added, however.

### Tailscale Integration

The `Hostname`, `StateDir`, and `ServeHTTPS` options are not interpreted directly by the library, but are provided to make it easier to connect a TailSQL server to [tsnet][tsnet]. The `cmd/tailsql` program shows how these can be used to run the server on a Tailscale node, either with or without TLS support.

### Query Logging

The `LocalState` option permits you to enable logging of successful queries in a separate SQLite database maintained by TailSQL itself. If  this option is set, the server will use the specified database to record each query using [`state-schema.sql`][stschema]. If this option is not set, queries are logged only as text (see the `Logf` option).

In addition, if the `LocalSource` option is set, a read-only view of the the query log database will be included in the list of available data sources, so users can query the log directly in the playground:

```sql
-- List the five most-recent successful queries.
select * from query_log order by timestamp desc limit 5;
```

### Static Links

The playground UI is defined in [ui.tmpl][uitmpl], and includes an optional section for static links. These are populated from the `UILinks` option. This is a good place to put links to documentation, for example:

```go
opts := tailsql.Options{
   UILinks: []tailsql.UILink{
     {
        Anchor: "Blog",
        URL:    "https://tailscale.com/blog",
     },
     {
        Anchor: "Repo",
        URL:    "https://github.com/tailscale/tailsql",
     },
   },
}
```

### Authorization

By default, the server does not do any authorization. The `LocalClient` option allows you to plug the server in to Tailscale: If this option is set, it is used to resolve callers and only logged-in users will be permitted to make queries. (You could theoretically also implement your own thing without Tailscale, but that would be a lot of work for very little benefit).

To further customize authorization, you can provide a callback via the `Authorize` option. The [authorizer][authz] package provides some pre-defined implementations, or you can roll your own. This is useful if you want to expose multiple data sources, some of which have more restrictive access policies.

### UI Rewrite Rules

The server renders column values for the UI as plain strings, using some simple built-in rules for common data types. The `UIRewriteRules` option allows you to extend these rules with custom behaviour. The [uirules][uirules] package provides some pre-defined implementations, or you can roll your own.

Rewrite rules are applied in the order they are listed in the options. As each value is rendered, the rules are checked: The first rule that **matches** the column value is **applied** to that value, and the result replaces the default.

For example:

```go
blotSecrets := tailsql.UIRewriteRule{
   // If the column name contains "password" or "passwd" ...
   Column: regexp.MustCompile(`(?i)passw(or)?d`),

   // Replace the input text with a redaction marker.
   Apply: func(column, input string, match []string) any {
      return "<redacted-password>"
   },
}
```

Only the first matching rule is applied; subsequent rules are skipped.


<!-- references -->
[authz]: https://godoc.org/github.com/tailscale/tailsql/authorizer
[dbsql]: https://godoc.org/database/sql
[options]: https://godoc.org/github.com/tailscale/tailsql/server/tailsql#Options
[stschema]: ./server/tailsql/state-schema.sql
[tailsql]: https://godoc.org/github.com/tailscale/tailsql/server/tailsql
[tsnet]: https://godoc.org/tailscale.com/tsnet
[uirules]: https://godoc.org/github.com/tailscale/tailsql/uirules
[uitmpl]: ./server/tailsql/ui.tmpl
