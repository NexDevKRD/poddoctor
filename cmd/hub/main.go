// Command hub runs the PodDoctor fleet hub: a central service that
// ingests diagnoses posted from many clusters' PodDoctor operators and
// serves one fleet-wide dashboard/API over them.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chenar/poddoctor/internal/hub"
	"github.com/chenar/poddoctor/internal/tracelink"
)

func main() {
	var listenAddr string
	var dsn string
	var token string
	var grafanaURL string
	var tempoDatasourceUID string

	flag.StringVar(&listenAddr, "listen-address", ":8090", "Address the hub HTTP server binds to.")
	flag.StringVar(&dsn, "db-dsn", "", "Postgres connection string. Falls back to $DATABASE_URL.")
	flag.StringVar(&token, "token", "", "Bearer token required on every endpoint (ingest, API, dashboard). Falls back to $PODDOCTOR_HUB_TOKEN. Empty disables auth — not recommended once reachable from more than one trusted cluster.")
	flag.StringVar(&grafanaURL, "grafana-url", "", "Base URL of a Grafana instance with a Tempo datasource, for a \"View Traces\" link on each diagnosis. Requires --tempo-datasource-uid too.")
	flag.StringVar(&tempoDatasourceUID, "tempo-datasource-uid", "", "UID of the Tempo datasource in Grafana. Required alongside --grafana-url for trace links.")
	flag.Parse()

	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		log.Fatal("no Postgres DSN: set --db-dsn or $DATABASE_URL")
	}
	if token == "" {
		token = os.Getenv("PODDOCTOR_HUB_TOKEN")
	}
	if token == "" {
		log.Print("WARNING: no bearer token set (--token / $PODDOCTOR_HUB_TOKEN) — every endpoint is unauthenticated")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	store, err := hub.Open(openCtx, dsn)
	cancel()
	if err != nil {
		log.Fatalf("connecting to Postgres: %v", err)
	}
	defer func() { _ = store.Close() }()

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           hub.NewServer(store, token, tracelink.Config{GrafanaURL: grafanaURL, TempoDatasourceUID: tempoDatasourceUID}).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("poddoctor-hub listening on %s", listenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
