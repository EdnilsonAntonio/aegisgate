## Benchmark — previsão

Previsão: (c) o ns/op tende a subir quando aumentamos o número de CPUs.

Justificação:

A implementação atual utiliza um único sync.Mutex para proteger
todo o mapa de buckets.

Consequentemente, mesmo quando pedidos diferentes utilizam chaves
diferentes, todas as goroutines precisam de adquirir o mesmo mutex.

Com o aumento de GOMAXPROCS, podem existir mais goroutines a competir
simultaneamente pelo mesmo mutex, aumentando a contenção e os custos
de sincronização.

Espero observar este efeito nos dois benchmarks, sobretudo em
BenchmarkAllow_SameKey.


## Benchmark — resultados e análise

### Resultados

| Benchmark | CPUs | ns/op | B/op | allocs/op |
|---|---:|---:|---:|---:|
| SameKey | 1 | 163.8 | 0 | 0 |
| SameKey | 4 | 205.0 | 0 | 0 |
| SameKey | 8 | 260.8 | 0 | 0 |
| ManyKeys | 1 | 217.4 | 0 | 0 |
| ManyKeys | 4 | 257.3 | 0 | 0 |
| ManyKeys | 8 | 302.3 | 0 | 0 |

### Análise

A previsão (c) confirmou-se: o ns/op aumentou com o número de CPUs disponíveis.

No BenchmarkAllow_SameKey, o custo passou de 163.8 ns/op
com uma CPU para 260.8 ns/op com oito CPUs, um aumento
de aproximadamente 59.2%.

No BenchmarkAllow_ManyKeys, passou de 217.4 ns/op para
302.3 ns/op, um aumento de aproximadamente 39%.

Mesmo utilizando 10.000 chaves diferentes, as goroutines
continuam a disputar o mesmo sync.Mutex.

Isto revela que o mutex global constitui um potencial
estrangulamento à escalabilidade do Token Bucket.

Outro resultado positivo é a ausência de alocações por
operação nos dois benchmarks: 0 B/op e 0 allocs/op.

Conclusão: a implementação atual é eficiente em termos
de alocações, mas apresenta contenção na sincronização
quando aumenta o paralelismo.


## Passo 11 — Redução da contenção com Sharding

### Implementação

O mutex e o mapa globais foram substituídos por 64
fragmentos independentes, cada um com o seu próprio
sync.Mutex e mapa de buckets.

A seleção do fragmento é feita através de FNV-1a,
calculando o hash da chave do cliente e aplicando
módulo 64.

### Resultados — comparação

| Benchmark | CPUs | Mutex global (ns/op) | Sharding (ns/op) |
|---|---:|---:|---:|
| SameKey | 1 | 163.8 | 171.8 |
| SameKey | 4 | 205.0 | 220.8 |
| SameKey | 8 | 260.8 | 235.1 |
| ManyKeys | 1 | 217.4 | 233.0 |
| ManyKeys | 4 | 257.3 | 69.44 |
| ManyKeys | 8 | 302.3 | 58.68 |

### Análise

O Sharding melhorou significativamente o benchmark
ManyKeys: redução de aproximadamente 73.0% com quatro
CPUs e 80.6% com oito CPUs.

Com apenas uma CPU, houve um aumento de aproximadamente
7.2% no custo por operação, devido ao trabalho adicional
de calcular o hash e selecionar o fragmento.

No benchmark SameKey, não se verificou uma melhoria
significativa. Todas as operações da mesma chave são
encaminhadas para o mesmo fragmento e continuam a
disputar o respetivo mutex.

### Limitação: Hot Keys

Uma chave que recebe muitas operações concentra a
contenção num único fragmento.

Um atacante que envie milhares de pedidos a partir do
mesmo IP continuará a provocar contenção nesse fragmento.

O Sharding melhora a distribuição de operações entre
clientes diferentes, mas não elimina a contenção de
uma única chave.

### Alocações

Os benchmarks continuam a apresentar 0 B/op e
0 allocs/op nos dois cenários.

### Conclusão

O Sharding melhora substancialmente a escalabilidade
quando existem muitos clientes diferentes, mantendo
a segurança da concorrência.

O custo adicional de hashing é compensado pelos
benefícios do paralelismo em cenários com várias chaves.
