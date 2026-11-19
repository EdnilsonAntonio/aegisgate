package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/EdnilsonAntonio/aegisgate/internal/limiter"
	"github.com/EdnilsonAntonio/aegisgate/internal/proxy"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	// Configuração do Token Bucket.
	capacity, err := strconv.Atoi(getenv("RATE_CAPACITY", "20"))
	if err != nil {
		log.Fatalf("RATE_CAPACITY inválido: %v", err)
	}
	if capacity <= 0 {
		log.Fatal("RATE_CAPACITY deve ser maior que zero")
	}

	rate, err := strconv.ParseFloat(getenv("RATE_PER_SEC", "10"), 64)
	if err != nil {
		log.Fatalf("RATE_PER_SEC inválido: %v", err)
	}
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		log.Fatal("RATE_PER_SEC deve ser positivo e finito")
	}

	tb := limiter.NewTokenBucket(limiter.TokenBucketConfig{
		Capacity: capacity,
		Rate:     rate,
	})

	// Inicia a limpeza periódica dos buckets inativos.
	stopJanitor := tb.StartJanitor(30 * time.Second)
	defer stopJanitor()

	// Configuração do upstream.
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

	// Configuração do servidor HTTP.
	srv := &http.Server{
		Addr: getenv("LISTEN_ADDR", ":8080"),
		Handler: proxy.New(proxy.Config{
			Upstream:            upstream,
			RequestTimeout:      reqTimeout,
			MaxIdleConnsPerHost: idlePerHost,
			Limiter:             tb,
			FailOpen:            true,
		}),
		// Metade "cliente": sem isto, clientes lentos prendem ligações para sempre.
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
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

	<-ctx.Done()
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
