package limiter

import (
	"context"
	"strconv"
	"testing"
)

func BenchmarkAllow_SameKey(b *testing.B) {
	tb := NewTokenBucket(TokenBucketConfig{Capacity: 1 << 30, Rate: 1e9})
	ctx := context.Background()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tb.Allow(ctx, "k")
		}
	})
}

func BenchmarkAllow_ManyKeys(b *testing.B) {
	tb := NewTokenBucket(TokenBucketConfig{Capacity: 1 << 30, Rate: 1e9})
	ctx := context.Background()

	keys := make([]string, 10_000)
	for i := range keys {
		keys[i] = "ip-" + strconv.Itoa(i)
	}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tb.Allow(ctx, keys[i%len(keys)])
			i++
		}
	})
}
