package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type Config struct {
	Upstream            *url.URL
	RequestTimeout      time.Duration // prazo total de cada pedido, de ponta a ponta
	MaxIdleConnsPerHost int           // 0 = valor por omissão do Go (2)
}

func New(cfg Config) http.Handler {
	// Metade "backend": pool de ligações e timeouts de ligação.
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   500 * time.Millisecond, // abrir TCP não deve demorar mais do que isto
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        1024,                    // limite global de ligações inativas
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost, // limite por backend: é aqui que o Passo 9 mexe
		IdleConnTimeout:     90 * time.Second,        // fecha ligações paradas há muito tempo
	}

	rp := &httputil.ReverseProxy{
		// Rewrite (e não Director): ver Passo 10.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(cfg.Upstream) // aponta o pedido para o backend
			pr.SetXForwarded()      // X-Forwarded-For = IP real da ligação TCP
		},
		Transport:    transport,
		ErrorHandler: errorHandler,
	}

	mux := http.NewServeMux()
	// /healthz é respondido pelo próprio gateway, sem tocar no backend.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/", rp)

	// Ordem: requestID (fora) → timeout → mux (dentro).
	return requestID(withTimeout(cfg.RequestTimeout, mux))
}

// withTimeout impõe um prazo total ao pedido. Ao expirar, o contexto é
// cancelado e o ReverseProxy fecha a ligação ao backend.
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestID reutiliza o X-Request-ID recebido ou gera um novo, e devolve-o
// ao cliente. O proxy copia-o para o backend porque é um cabeçalho normal.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
			r.Header.Set("X-Request-ID", id)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

// errorHandler traduz erros do proxy em códigos HTTP.
func errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	id := r.Header.Get("X-Request-ID")
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		log.Printf("[%s] 504: %v", id, err)
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
	case errors.Is(err, context.Canceled):
		// O cliente desistiu. Ninguém está a ouvir, por isso só registamos.
		log.Printf("[%s] cliente cancelou", id)
	default:
		log.Printf("[%s] 502: %v", id, err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}
}
