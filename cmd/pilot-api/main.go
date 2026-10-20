package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime-app/pilotapi"
	"syscall"
	"time"
)

func main() {
	paid := flag.Bool("enable-paid", false, "allow registered DeepSeek submissions; worker must also have an explicit governed budget")
	address := flag.String("listen", "127.0.0.1:8090", "loopback gateway; terminate TLS at an explicitly approved proxy")
	registrations := flag.String("repositories", "", "operator-owned JSON repository registrations")
	capacity := flag.Int("max-queued", 100, "maximum unfinished queued runs across this database")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("loopback listen address required")
	}
	data, err := os.ReadFile(*registrations)
	if err != nil {
		log.Fatal("repository registrations required")
	}
	var repositories map[string]pilotapi.Repository
	if err = json.Unmarshal(data, &repositories); err != nil {
		log.Fatal("invalid repository registrations")
	}
	dsn := os.Getenv("DAY12_DATABASE_URL")
	if dsn == "" {
		log.Fatal("DAY12_DATABASE_URL required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal("database configuration rejected")
	}
	defer pool.Close()
	handler, err := (&pilotapi.Server{AllowPaid: *paid, Pool: pool, Repositories: repositories, MaxQueued: *capacity}).Handler(ctx)
	if err != nil {
		log.Fatal("pilot configuration/database check failed; inspect operator configuration")
	}
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(cleanup)
	}()
	log.Print("authenticated loopback API ready; coding workers require separately validated sandbox")
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("HTTP listener failed")
	}
}
