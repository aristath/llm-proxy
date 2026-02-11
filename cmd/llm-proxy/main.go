package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"llm-proxy/internal/api"
	"llm-proxy/internal/openapiv1"
	"llm-proxy/internal/proxy"
	"llm-proxy/internal/tui"
)

func main() {
	var (
		flagAddr     = flag.String("addr", "", "listen address (overrides ADDR env)")
		flagHeadless = flag.Bool("headless", false, "run without terminal UI")
		flagYOLO     = flag.Bool("yolo", false, "enable YOLO mode (disable CLI permission prompts)")
	)
	flag.Parse()

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if *flagAddr != "" {
		addr = *flagAddr
	}
	headless := *flagHeadless || os.Getenv("LLM_PROXY_HEADLESS") == "1"
	yolo := *flagYOLO || envBool("LLM_PROXY_YOLO")
	proxy.SetYOLO(yolo)

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
	if yolo {
		log.Printf("YOLO mode enabled")
	}

	if headless {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		select {
		case err := <-errCh:
			if err != nil {
				log.Fatal(err)
			}
		case <-ctx.Done():
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("shutdown error: %v", err)
		}
		return
	}

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

func envBool(key string) bool {
	v := os.Getenv(key)
	switch v {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	default:
		return false
	}
}
