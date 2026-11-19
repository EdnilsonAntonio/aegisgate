package proxy

import (
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/EdnilsonAntonio/aegisgate/internal/limiter"
)

// clientKey devolve a chave de limite: o IP da LIGAÇÃO TCP.
// Não olha para X-Forwarded-For (ver docs/fase1.md §7).
// IPv6 é agrupado por /64, porque um cliente costuma ter um bloco inteiro.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap() // ::ffff:1.2.3.4 → 1.2.3.4
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

func rateLimit(l limiter.Limiter, failOpen bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := l.Allow(r.Context(), clientKey(r))
		if err != nil {
			log.Printf("[%s] limiter falhou: %v", r.Header.Get("X-Request-ID"), err)
			if !failOpen {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))
		if !d.Allowed {
			secs := int(math.Ceil(d.RetryAfter.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
