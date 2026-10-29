package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/EdnilsonAntonio/aegisgate/internal/proxy"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	upstream, err := url.Parse(getenv("UPSTREAM_URL", "http://localhost:9000"))
	if err != nil {
		log.Fatalf("UPSTREAM_URL inválida: %v", err)
	}
	reqTimeout, err := time.ParseDuration(getenv("REQUEST_TIMEOUT", "1s"))
	if err != nil {
		log.Fatalf("REQUEST_TIMEOUT inválido: %v", err)
	}
	idlePerHost, err := strconv.Atoi(getenv("MAX_IDLE_PER_HOST", "0"))
	if err != nil {
		log.Fatalf("MAX_IDLE_PER_HOST inválido: %v", err)
	}

	srv := &http.Server{
		Addr: getenv("LISTEN_ADDR", ":8080"),
		Handler: proxy.New(proxy.Config{
			Upstream:            upstream,
			RequestTimeout:      reqTimeout,
			MaxIdleConnsPerHost: idlePerHost,
		}),
		// Metade "cliente": sem isto, clientes lentos prendem ligações para sempre.
		ReadHeaderTimeout: 2 * time.Second,  // defesa contra clientes que enviam cabeçalhos a conta-gotas
		ReadTimeout:       5 * time.Second,  // ler o pedido inteiro
		WriteTimeout:      15 * time.Second, // tem de ser MAIOR que REQUEST_TIMEOUT, senão o 504 nunca chega ao cliente
		IdleTimeout:       60 * time.Second, // ligações keep-alive paradas
	}

	// Ctrl+C / SIGTERM cancelam este contexto.
	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	go func() {
		log.Printf("gateway em %s → %s", srv.Addr, upstream)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("servidor falhou: %v", err)
		}
	}()

	<-ctx.Done() // bloqueia até chegar o sinal
	log.Println("a terminar: a deixar os pedidos em curso acabarem...")

	// Shutdown deixa de aceitar ligações novas e espera pelos pedidos em curso (até 10 s).
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("encerramento forçado: %v", err)
	}
	log.Println("terminado")
}
