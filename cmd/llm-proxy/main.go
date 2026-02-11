package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"llm-proxy/internal/api"
	"llm-proxy/internal/openapiv1"
	"llm-proxy/internal/proxy"
	"llm-proxy/internal/tui"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	router := proxy.NewRouter(proxy.NewClaudeAdapter(), proxy.NewCodexAdapter())
	apiServer := api.NewServer(router)
	metrics := api.NewMetrics()

	handler := openapiv1.HandlerFromMux(apiServer, http.NewServeMux())
	handler = metrics.Middleware(handler)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
	}
	errCh := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	log.Printf("llm-proxy listening on %s", addr)

	app := tui.New(addr, metrics, httpServer, errCh)
	runErr := app.Run()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := app.Shutdown(ctx)
	if shutdownErr != nil {
		log.Printf("shutdown error: %v", shutdownErr)
	}

	if runErr != nil {
		log.Fatal(runErr)
	}
}
