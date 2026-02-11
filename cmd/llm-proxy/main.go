package main

import (
	"log"
	"net/http"
	"os"

	"llm-proxy/internal/api"
	"llm-proxy/internal/openapiv1"
	"llm-proxy/internal/proxy"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	router := proxy.NewRouter(&proxy.ClaudeAdapter{}, &proxy.CodexAdapter{})
	server := api.NewServer(router)

	handler := openapiv1.HandlerFromMux(server, http.NewServeMux())

	log.Printf("llm-proxy listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}
