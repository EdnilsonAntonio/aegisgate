package limiter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// Relógio manual: só avança quando o teste manda.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTB(capacity int, rate float64) (*TokenBucket, *fakeClock) {
	clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	return NewTokenBucket(TokenBucketConfig{Capacity: capacity, Rate: rate, Now: clk.Now}), clk
}

func mustAllow(t *testing.T, tb *TokenBucket, key string) Decision {
	t.Helper()
	d, err := tb.Allow(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestBurstThenDeny(t *testing.T) {
	tb, _ := newTB(5, 1)
	for i := 0; i < 5; i++ {
		d := mustAllow(t, tb, "a")
		if !d.Allowed || d.Remaining != 4-i {
			t.Fatalf("pedido %d: %+v", i+1, d)
		}
	}
	d := mustAllow(t, tb, "a")
	if d.Allowed {
		t.Fatal("o 6º pedido devia ser negado")
	}
	if d.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %v, esperava 1s", d.RetryAfter)
	}
}

func TestRefillOverTime(t *testing.T) {
	tb, clk := newTB(5, 1)
	for i := 0; i < 5; i++ {
		mustAllow(t, tb, "a")
	}
	clk.Advance(2500 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if !mustAllow(t, tb, "a").Allowed {
			t.Fatalf("pedido %d depois de 2,5s devia passar", i+1)
		}
	}
	d := mustAllow(t, tb, "a")
	if d.Allowed {
		t.Fatal("o 3º pedido devia ser negado: só havia 2,5 fichas")
	}
	if d.RetryAfter != 500*time.Millisecond {
		t.Fatalf("RetryAfter = %v, esperava 500ms", d.RetryAfter)
	}
}

func TestNeverExceedsCapacity(t *testing.T) {
	tb, clk := newTB(5, 1)
	for i := 0; i < 5; i++ {
		mustAllow(t, tb, "a")
	}
	clk.Advance(time.Hour) // muito mais do que o necessário para encher
	for i := 0; i < 5; i++ {
		if !mustAllow(t, tb, "a").Allowed {
			t.Fatalf("pedido %d devia passar", i+1)
		}
	}
	if mustAllow(t, tb, "a").Allowed {
		t.Fatal("o balde nunca pode ter mais do que Capacity fichas")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	tb, _ := newTB(1, 1)
	mustAllow(t, tb, "a")
	if mustAllow(t, tb, "a").Allowed {
		t.Fatal("a devia estar esgotada")
	}
	if !mustAllow(t, tb, "b").Allowed {
		t.Fatal("b não devia ser afetada por a")
	}
}

// O teste mais importante: 1000 goroutines contra a MESMA chave.
// Com relógio parado não há reposição, logo passam exatamente `capacity`.
func TestConcurrent_NoOverAdmission(t *testing.T) {
	const capacity = 100
	tb, _ := newTB(capacity, 1)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 1000; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				d, _ := tb.Allow(context.Background(), "ip-atacante")
				if d.Allowed {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != capacity {
		t.Fatalf("passaram %d pedidos, esperava exatamente %d", got, capacity)
	}
}

func TestSweepEvictsOnlyFullBuckets(t *testing.T) {
	tb, clk := newTB(5, 1)
	mustAllow(t, tb, "a") // a: 4 fichas
	for i := 0; i < 5; i++ {
		mustAllow(t, tb, "b") // b: 0 fichas
	}
	if tb.Len() != 2 {
		t.Fatalf("Len = %d, esperava 2", tb.Len())
	}

	clk.Advance(1 * time.Second) // a voltou a 5 (cheio); b tem 1
	tb.Sweep()
	if tb.Len() != 1 {
		t.Fatalf("depois de 1s: Len = %d, esperava 1 (só b)", tb.Len())
	}

	clk.Advance(4 * time.Second) // b voltou a 5
	tb.Sweep()
	if tb.Len() != 0 {
		t.Fatalf("depois de 5s: Len = %d, esperava 0", tb.Len())
	}

	// Chave apagada e chave nova são indistinguíveis.
	if d := mustAllow(t, tb, "a"); !d.Allowed || d.Remaining != 4 {
		t.Fatalf("decisão depois de apagar: %+v", d)
	}
}

func TestJanitorStops(t *testing.T) {
	defer goleak.VerifyNone(t)
	tb, _ := newTB(5, 1)
	stop := tb.StartJanitor(10 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	stop()
}
