# TailSQL

TailSQL is a self-contained SQL playground service that runs on [Tailscale](https://tailscale.com).
It permits users to query SQL databases from a basic web-based UI, with support for any database
that can plug in to the Go [`database/sql`](https://godoc.org/database/sql) package.
