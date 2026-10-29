package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

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
