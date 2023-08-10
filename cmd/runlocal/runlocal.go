// Program tailsql runs a standalone SQL playground.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/tailscale/tailsql/tailsql"
	"tailscale.com/tsweb"

	// SQLite driver for database/sql.
	_ "modernc.org/sqlite"
)

var (
	port       = flag.Int("port", 8080, "Service port")
	configPath = flag.String("config", "", "Configuration file (HuJSON, required)")
	initConfig = flag.String("init-config", "",
		"Generate a basic configuration file in the given path and exit")

	// TODO(creachadair): Allow starting on tsnet.
)

func main() {
	flag.Parse()

	if *initConfig != "" {
		generateBasicConfig(*initConfig)
		log.Printf("Generated sample config in %s", *initConfig)
		return
	}
	if *port <= 0 {
		log.Fatal("You must provide a --port > 0")
	} else if *configPath == "" {
		log.Fatal("You must provide a --config path")
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Reading tailsql config: %v", err)
	}

	var opts tailsql.Options
	if err := tailsql.UnmarshalOptions(data, &opts); err != nil {
		log.Fatalf("Parsing tailsql config: %v", err)
	}

	tsql, err := tailsql.NewServer(opts)
	if err != nil {
		log.Fatalf("Creating tailsql server: %v", err)
	}

	mux := tsql.NewMux()
	tsweb.Debugger(mux)
	if opts.Hostname == "" {
		opts.Hostname = "localhost"
	}
	hsrv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", opts.Hostname, *port),
		Handler: mux,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		log.Print("Signal received, stopping")
		hsrv.Shutdown(context.Background()) // ctx is already terminated
		tsql.Close()
	}()
	log.Printf("Starting tailsql at http://%s", hsrv.Addr)
	if err := hsrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func generateBasicConfig(path string) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Create config: %v", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	eerr := enc.Encode(tailsql.Options{
		Hostname:    "localhost",
		LocalState:  "runlocal-state.db",
		LocalSource: "local",
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
			Anchor: "source",
			URL:    "https://github.com/tailscale/tailsql/tree/main/tailsql",
		}},
	})
	cerr := f.Close()
	if err := errors.Join(eerr, cerr); err != nil {
		log.Fatalf("Write config: %v", err)
	}
}
