package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime-app/artifactstore"
	"syscall"
	"time"
)

func run() error {
	root := flag.String("root", "", "canonical private artifact directory")
	listen := flag.String("listen", "127.0.0.1:8095", "private listener; use TLS reverse proxy outside isolated lab")
	flag.Parse()
	token := os.Getenv("ARTIFACT_TOKEN")
	if len(token) < 24 {
		return fmt.Errorf("private ARTIFACT_TOKEN of >=24 characters required")
	}
	store, e := artifactstore.NewDisk(*root)
	if e != nil {
		return e
	}
	server := http.Server{Addr: *listen, Handler: artifactstore.Handler(store, token), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(c)
	}()
	e = server.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
