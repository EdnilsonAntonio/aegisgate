package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/EdnilsonAntonio/aegisgate/internal/limiter"
	"go.uber.org/goleak"
)

func TestSlowBackend_Returns504AndBackendSeesCancel(t *testing.T) {
	cancelled := make(chan struct{})

	// Backend que demora 5 s, mas avisa se o pedido for cancelado.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
			close(cancelled)
		}
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	gw := httptest.NewServer(New(Config{Upstream: u, RequestTimeout: 1 * time.Second}))
	defer gw.Close()

	start := time.Now()
	resp, err := http.Get(gw.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("esperava 504, obtive %d", resp.StatusCode)
	}
	if elapsed < 900*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("esperava ~1s, demorou %v", elapsed)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("o backend não viu o cancelamento")
	}
}

func TestBackendDown_Returns502Fast(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u, _ := url.Parse(backend.URL)
	backend.Close()

	gw := httptest.NewServer(New(Config{Upstream: u, RequestTimeout: 1 * time.Second}))
	defer gw.Close()

	start := time.Now()
	resp, err := http.Get(gw.URL + "/fast")

	if err != nil {
		t.Fatalf("não esperava erro de transporte do cliente: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("esperava 502, obtive %d", resp.StatusCode)
	}

	elapsed := time.Since(start)
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("esperava resposta em menos de 200ms, demorou %v", elapsed)
	}
}

func TestClientCancel_CancelsBackend(t *testing.T) {
	defer goleak.VerifyNone(t)

	cancelled := make(chan struct{})

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
			close(cancelled)
		}
	}))
	defer backend.Close()

	u, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	gw := httptest.NewServer(New(Config{
		Upstream:       u,
		RequestTimeout: 1 * time.Second,
	}))
	defer gw.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", gw.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("esperava erro porque o contexto do cliente expirou")
	}

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("o backend não viu o cancelamento")
	}
}

func TestRateLimit_Returns429WithRetryAfter(t *testing.T) {
	// Backend simples que responde sempre 200.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	upstream, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Relógio fixo: o tempo nunca avança durante o teste.
	t0 := time.Now()

	tb := limiter.NewTokenBucket(limiter.TokenBucketConfig{
		Capacity: 2,
		Rate:     1,
		Now:      func() time.Time { return t0 },
	})

	// Gateway com rate limiter.
	gw := httptest.NewServer(New(Config{
		Upstream:       upstream,
		RequestTimeout: 1 * time.Second,
		Limiter:        tb,
	}))
	defer gw.Close()

	client := gw.Client()
	defer client.CloseIdleConnections()

	expectedStatuses := []int{
		http.StatusOK,
		http.StatusOK,
		http.StatusTooManyRequests,
	}

	for i, expected := range expectedStatuses {
		resp, err := client.Get(gw.URL + "/fast")
		if err != nil {
			t.Fatalf("pedido %d: erro inesperado: %v", i+1, err)
		}

		if resp.StatusCode != expected {
			resp.Body.Close()
			t.Fatalf(
				"pedido %d: esperava HTTP %d, obtive %d",
				i+1,
				expected,
				resp.StatusCode,
			)
		}

		// O terceiro pedido tem de incluir Retry-After: 1.
		if i == 2 {
			retryAfter := resp.Header.Get("Retry-After")
			if retryAfter != "1" {
				resp.Body.Close()
				t.Fatalf("esperava Retry-After: 1, obtive %q", retryAfter)
			}
		}

		resp.Body.Close()
	}
}

func TestHealthz_BypassesRateLimit(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	upstream, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()

	tb := limiter.NewTokenBucket(limiter.TokenBucketConfig{
		Capacity: 2,
		Rate:     1,
		Now:      func() time.Time { return t0 },
	})

	gw := httptest.NewServer(New(Config{
		Upstream:       upstream,
		RequestTimeout: 1 * time.Second,
		Limiter:        tb,
	}))
	defer gw.Close()

	client := gw.Client()
	defer client.CloseIdleConnections()

	// Primeiro, esgotamos as duas fichas através de /fast.
	for i := 0; i < 2; i++ {
		resp, err := client.Get(gw.URL + "/fast")
		if err != nil {
			t.Fatalf("erro ao esgotar o limiter: %v", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("esperava HTTP 200, obtive %d", resp.StatusCode)
		}

		resp.Body.Close()
	}

	// Mesmo com o limiter esgotado, /healthz deve continuar a responder.
	for i := 0; i < 10; i++ {
		resp, err := client.Get(gw.URL + "/healthz")
		if err != nil {
			t.Fatalf("pedido %d a /healthz falhou: %v", i+1, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf(
				"pedido %d a /healthz: esperava HTTP 200, obtive %d",
				i+1,
				resp.StatusCode,
			)
		}

		resp.Body.Close()
	}
}
