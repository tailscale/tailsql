// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

// Program tailsql runs a standalone SQL playground.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/creachadair/ctrl"
	"github.com/tailscale/tailsql/server/tailsql"
	"github.com/tailscale/tailsql/uirules"
	"tailscale.com/tsnet"
	"tailscale.com/tsweb"
	"tailscale.com/types/logger"

	// If you want to support other source types with this tool, you will need
	// to import other database drivers below.

	// SQLite driver for database/sql.
	_ "modernc.org/sqlite"
)

var (
	localPort  = flag.Int("local", 0, "Local service port")
	configPath = flag.String("config", "", "Configuration file (HuJSON, required)")
	doDebugLog = flag.Bool("debug", false, "Enable very verbose tsnet debug logging")
	initConfig = flag.String("init-config", "",
		"Generate a basic configuration file in the given path and exit")
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: %[1]s [options] --config config.json
       %[1]s --init-config demo.json

Run a TailSQL service with the specified --config file.

If --local > 0, the service is run on localhost at that port.
Otherwise, the server starts a Tailscale node at the configured hostname.

When run with --init-config set, %[1]s generates an example configuration file
with defaults suitable for running a local service and then exits.

Options:
`, filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()
	ctrl.Run(func() error {
		// Case 1: Generate a semple configuration file and exit.
		if *initConfig != "" {
			return generateBasicConfig(*initConfig)
		} else if *configPath == "" {
			ctrl.Fatalf("You must provide a non-empty --config path")
		}

		// For all the cases below, we need a valid configuration file.
		data, err := os.ReadFile(*configPath)
		if err != nil {
			ctrl.Fatalf("Reading tailsql config: %v", err)
		}
		var opts tailsql.Options
		if err := tailsql.UnmarshalOptions(data, &opts); err != nil {
			ctrl.Fatalf("Parsing tailsql config: %v", err)
		}
		opts.Metrics = expvar.NewMap("tailsql")
		opts.UIRewriteRules = []tailsql.UIRewriteRule{
			uirules.FormatSQLSource,
			uirules.FormatJSONText,
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		// Case 2: Run unencrypted on a local port.
		if *localPort > 0 {
			return runLocalService(ctx, opts, *localPort)
		}

		// Case 3: Run on a tailscale node.
		if opts.Hostname == "" {
			ctrl.Fatalf("You must provide a non-empty Tailscale hostname")
		}
		return runTailscaleService(ctx, opts)
	})
}

func runLocalService(ctx context.Context, opts tailsql.Options, port int) error {
	tsql, err := tailsql.NewServer(opts)
	if err != nil {
		ctrl.Fatalf("Creating tailsql server: %v", err)
	}

	mux := tsql.NewMux()
	tsweb.Debugger(mux)
	hsrv := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		log.Print("Signal received, stopping")
		hsrv.Shutdown(context.Background()) // ctx is already terminated
		tsql.Close()
	}()
	log.Printf("Starting local tailsql at http://%s", hsrv.Addr)
	if err := hsrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		ctrl.Fatalf(err.Error())
	}
	return nil
}

func runTailscaleService(ctx context.Context, opts tailsql.Options) error {
	tsNode := &tsnet.Server{
		Dir:      os.ExpandEnv(opts.StateDir),
		Hostname: opts.Hostname,
		Logf:     logger.Discard,
	}
	if *doDebugLog {
		tsNode.Logf = log.Printf
	}
	defer tsNode.Close()

	log.Printf("Starting tailscale (hostname=%q)", opts.Hostname)
	lc, err := tsNode.LocalClient()
	if err != nil {
		ctrl.Fatalf("Connect local client: %v", err)
	}
	opts.LocalClient = lc // for authentication

	// Make sure the Tailscale node starts up. It might not, if it is a new node
	// and the user did not provide an auth key.
	if st, err := tsNode.Up(ctx); err != nil {
		ctrl.Fatalf("Starting tailscale: %v", err)
	} else {
		log.Printf("Tailscale started, node state %q", st.BackendState)
	}

	// Reaching here, we have a running Tailscale node, now we can set up the
	// HTTP and/or HTTPS plumbing for TailSQL itself.
	tsql, err := tailsql.NewServer(opts)
	if err != nil {
		ctrl.Fatalf("Creating tailsql server: %v", err)
	}

	lst, err := tsNode.Listen("tcp", ":80")
	if err != nil {
		ctrl.Fatalf("Listen port 80: %v", err)
	}

	if opts.ServeHTTPS {
		// When serving TLS, add a redirect from HTTP on port 80 to HTTPS on 443.
		certDomains := tsNode.CertDomains()
		if len(certDomains) == 0 {
			ctrl.Fatalf("No cert domains available for HTTPS")
		}
		base := "https://" + certDomains[0]
		go http.Serve(lst, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := base + r.RequestURI
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		}))
		log.Printf("Redirecting HTTP to HTTPS at %q", base)

		// For the real service, start a separate listener.
		// Note: Replaces the port 80 listener.
		var err error
		lst, err = tsNode.ListenTLS("tcp", ":443")
		if err != nil {
			ctrl.Fatalf("Listen TLS: %v", err)
		}
		log.Print("Enabled serving via HTTPS")
	}

	mux := tsql.NewMux()
	tsweb.Debugger(mux)
	go http.Serve(lst, mux)
	log.Printf("TailSQL started")
	<-ctx.Done()
	log.Print("TailSQL shutting down...")
	return tsNode.Close()
}

func generateBasicConfig(path string) error {
	f, err := os.Create(path)
	if err != nil {
		ctrl.Fatalf("Create config: %v", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	eerr := enc.Encode(tailsql.Options{
		Hostname:    "tailsql-dev",
		LocalState:  "tailsql-state.db",
		LocalSource: "self",
		Sources: []tailsql.DBSpec{{
			Source: "main",
			Label:  "Test database",
			Driver: "sqlite",
			URL:    "file:test.db?mode=ro",
			Named: map[string]string{
				"schema": `select * from sqlite_schema`,
			},
		}},
		UILinks: []tailsql.UILink{{
			Anchor: "source code",
			URL:    "https://github.com/tailscale/tailsql",
		}},
	})
	cerr := f.Close()
	if err := errors.Join(eerr, cerr); err != nil {
		ctrl.Fatalf("Write config: %v", err)
	}
	log.Printf("Generated sample config in %s", path)
	return nil
}
