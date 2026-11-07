package limiter

import (
	"context"
	"math"
	"sync"
	"time"
)

type TokenBucketConfig struct {
	Capacity int              // máximo de fichas (tamanho do burst)
	Rate     float64          // fichas repostas por segundo
	Now      func() time.Time // relógio injetável; nil = time.Now
}

type bucket struct {
	tokens float64
	last   time.Time
}

type TokenBucket struct {
	cfg     TokenBucketConfig
	mu      sync.Mutex
	buckets map[string]*bucket
}

func NewTokenBucket(cfg TokenBucketConfig) *TokenBucket {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &TokenBucket{
		cfg:     cfg,
		buckets: make(map[string]*bucket),
	}
}

// Len devolve quantas chaves estão guardadas (usado nos testes).
func (tb *TokenBucket) Len() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return len(tb.buckets)
}

func (tb *TokenBucket) Allow(_ context.Context, key string) (Decision, error) {
	now := tb.cfg.Now()

	tb.mu.Lock()
	defer tb.mu.Unlock()

	b, ok := tb.buckets[key]
	if !ok {
		// TODO 1: chave nova → balde CHEIO. Cria um bucket com
		// tokens = Capacity e last = now, e guarda-o no mapa.
		b = &bucket{
			tokens: float64(tb.cfg.Capacity),
			last:   now,
		}
		tb.buckets[key] = b
	}

	// TODO 2: repor fichas. elapsed = now.Sub(b.last).Seconds();
	// tokens = min(Capacity, tokens + elapsed*Rate); last = now.
	// (Falta importar "math" para math.Min.) Se elapsed < 0 (o relógio recuou),
	// trata como 0. Porquê?
	elapsed := now.Sub(b.last).Seconds()
	if elapsed < 0 {
		elapsed = 0
	} else {
		b.last = now
	}

	b.tokens = math.Min(float64(tb.cfg.Capacity), b.tokens+elapsed*tb.cfg.Rate)

	// TODO 3: se b.tokens >= 1 → gasta 1 ficha e devolve
	// Decision{Allowed: true, Remaining: int(b.tokens)}.
	if b.tokens >= 1 {
		b.tokens--
		return Decision{
			Allowed:   true,
			Remaining: int(b.tokens),
		}, nil
	}

	// TODO 4: senão devolve Decision{Allowed: false, RetryAfter: ...}
	// com RetryAfter = (1 - tokens) / Rate segundos (converte para time.Duration).
	retryAfterSeconds := (1 - b.tokens) / tb.cfg.Rate

	retryAfter := time.Duration(retryAfterSeconds * float64(time.Second))

	return Decision{
		Allowed:    false,
		Remaining:  int(b.tokens),
		RetryAfter: retryAfter,
	}, nil
}

// Sweep apaga os baldes que já voltaram à capacidade máxima.
func (tb *TokenBucket) Sweep() {
	now := tb.cfg.Now()
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// TODO 5: para cada chave, calcula as fichas que o balde TERIA agora
	// (tokens + elapsed*Rate, sem alterar o balde). Se >= Capacity, apaga-o.
	// Em Go é seguro fazer delete(m, k) dentro de um `for k, b := range m`.
	for key, b := range tb.buckets {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed < 0 {
			elapsed = 0
		}
		tokensNow := b.tokens + elapsed * tb.cfg.Rate
		if tokensNow >= float64(tb.cfg.Capacity) {
			delete(tb.buckets, key)
		}
	}
}

// StartJanitor chama Sweep periodicamente. stop() para-o e espera que termine.
func (tb *TokenBucket) StartJanitor(interval time.Duration) (stop func()) {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				tb.Sweep()
			case <-quit:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-done
	}
}
