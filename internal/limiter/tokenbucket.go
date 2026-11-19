package limiter

import (
	"context"
	"math"
	"sync"
	"time"

	"hash/fnv"
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
    cfg    TokenBucketConfig
    shards []shard
}

const shardCount = 64

type shard struct {
    mu      sync.Mutex
    buckets map[string]*bucket
}

func NewTokenBucket(cfg TokenBucketConfig) *TokenBucket {
    if cfg.Now == nil {
        cfg.Now = time.Now
    }

    shards := make([]shard, shardCount)

    for i := range shards {
        shards[i].buckets = make(map[string]*bucket)
    }

    return &TokenBucket{
        cfg:    cfg,
        shards: shards,
    }
}

func (tb *TokenBucket) shardIndex(key string) int {
    h := fnv.New32a()
    _, _ = h.Write([]byte(key))

    return int(h.Sum32() % uint32(len(tb.shards)))
}

// Len devolve quantas chaves estão guardadas (usado nos testes).
func (tb *TokenBucket) Len() int {
    total := 0

    for i := range tb.shards {
        s := &tb.shards[i]

        s.mu.Lock()
        total += len(s.buckets)
        s.mu.Unlock()
    }

    return total
}

func (tb *TokenBucket) Allow(_ context.Context, key string) (Decision, error) {
    now := tb.cfg.Now()

    s := &tb.shards[tb.shardIndex(key)]

    s.mu.Lock()
    defer s.mu.Unlock()

    b, ok := s.buckets[key]
    if !ok {
        b = &bucket{
            tokens: float64(tb.cfg.Capacity),
            last:   now,
        }
        s.buckets[key] = b
    }

    elapsed := now.Sub(b.last).Seconds()

    if elapsed < 0 {
        elapsed = 0
    } else {
        b.last = now
    }

    b.tokens = math.Min(
        float64(tb.cfg.Capacity),
        b.tokens+elapsed*tb.cfg.Rate,
    )

    if b.tokens >= 1 {
        b.tokens--
        return Decision{
            Allowed:   true,
            Remaining: int(b.tokens),
        }, nil
    }

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

    for i := range tb.shards {
        s := &tb.shards[i]

        s.mu.Lock()

        for key, b := range s.buckets {
            elapsed := now.Sub(b.last).Seconds()

            if elapsed < 0 {
                elapsed = 0
            }

            tokensNow := b.tokens + elapsed*tb.cfg.Rate

            if tokensNow >= float64(tb.cfg.Capacity) {
                delete(s.buckets, key)
            }
        }

        s.mu.Unlock()
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
