package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
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
