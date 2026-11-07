package limiter

import (
	"context"
	"time"
)

// Decision é o resultado de uma verificação.
type Decision struct {
	Allowed    bool
	Remaining  int           // fichas inteiras que sobram depois deste pedido
	RetryAfter time.Duration // se negado: quanto esperar até haver 1 ficha
}

// Limiter decide se um pedido da chave `key` pode passar.
// ctx e error existem porque a implementação Redis (Fase 3) faz I/O.
type Limiter interface {
	Allow(ctx context.Context, key string) (Decision, error)
}