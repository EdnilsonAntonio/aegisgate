# Fase 1 — Robustez, cancelamento e desempenho

## Objetivo

Validar o comportamento básico do AegisGate perante timeouts, indisponibilidade do backend, cancelamento de pedidos, encerramento gracioso e reutilização de ligações HTTP. Registar também o comportamento dos cabeçalhos de encaminhamento e as limitações observadas nas experiências.

## 1. Testes automatizados

### 1.1 Timeout do pedido e cancelamento no backend

**Teste:** `TestSlowBackend_Returns504AndBackendSeesCancel`

O backend lento espera pela conclusão do atraso ou pelo cancelamento do contexto do pedido. O Gateway é configurado com `RequestTimeout` de 1 segundo, enquanto o backend demora mais tempo a responder.

**Comportamento esperado:**

- O cliente recebe `504 Gateway Timeout` quando o timeout do Gateway expira.
- O cancelamento é propagado ao backend através do contexto do pedido.
- O teste confirma que o backend observa o cancelamento.

Este teste verifica que o Gateway não mantém indefinidamente pedidos que já excederam o tempo permitido.

### 1.2 Backend indisponível

**Teste:** `TestBackendDown_Returns502Fast`

O backend é fechado antes de o Gateway ser criado. O teste envia um pedido ao Gateway e verifica o tratamento de uma falha de ligação ao upstream.

**Comportamento esperado:**

- O Gateway devolve `502 Bad Gateway`.
- A falha é devolvida rapidamente, com o limite de 200 ms usado pelo teste.

Este teste distingue uma falha de comunicação com o backend de um timeout de processamento do pedido.

### 1.3 Cancelamento iniciado pelo cliente e fugas de goroutines

O teste usa um backend lento e um contexto do cliente com timeout de 200 ms. O cliente deve terminar a espera quando o seu contexto é cancelado, e o backend deve observar o cancelamento do pedido.

Foi usado `go.uber.org/goleak` com `defer goleak.VerifyNone(t)` para detetar goroutines que permaneçam indevidamente ativas no final do teste.

**Comportamento esperado:**

- O cliente observa o erro de cancelamento ou timeout.
- O backend observa o cancelamento do contexto.
- Não ficam goroutines inesperadamente ativas no final do teste.

A verificação da ausência de fugas de goroutines complementa os testes funcionais, mas não substitui a verificação explícita do comportamento HTTP esperado.

### Executar os testes

Para executar os testes do pacote do proxy com o detector de corridas:

```bash
go test -race -v ./internal/proxy/
```

Para executar todos os testes do projeto:

```bash
go test ./... -v
```

Durante o desenvolvimento, foi confirmado que três testes estavam a passar. Os testes de mutação descritos mais abaixo foram usados para confirmar que alguns dos testes falham quando se introduzem regressões propositadas.

## 2. Experiências de desempenho com `hey`

### 2.1 Método

O endpoint usado foi `/fast`, através do Gateway. Cada execução de carga durou 10 segundos. Para comparar o comportamento do pool de ligações, foram testadas duas configurações com 200 clientes concorrentes:

```bash
hey -z 10s -c 200 http://localhost:8080/fast
```

Após cada execução, foi consultado o contador de novas ligações TCP do backend:

```bash
curl -s localhost:9000/stats
```

O endpoint `/stats` conta novas ligações TCP (`http.StateNew`), e não o número de pedidos HTTP. A ligação usada pelo próprio `curl` para consultar as estatísticas foi descontada da contagem, conforme a metodologia utilizada na experiência.

No código, `MAX_IDLE_PER_HOST=0` é convertido num pool efetivo de 2 ligações inativas por host.

### 2.2 Resultados com 200 clientes concorrentes

Foram repetidas as medições reiniciando o stub e o Gateway entre os cenários. Em cada cenário, consultei `/stats` antes e depois do `hey`. O contador inicial foi `1` nos dois testes, correspondente à ligação aberta pela consulta inicial a `/stats`; a consulta final também abriu uma ligação e foi descontada no cálculo.

Cálculo usado:

```text
ligações novas durante o hey = B - A - 1
```

Em que `A` é o contador antes do `hey` e `B` o contador depois do `hey`. O `-1` desconta a ligação da consulta final a `/stats`.

| Métrica | `MAX_IDLE_PER_HOST=0` (pool efetivo 2) | `MAX_IDLE_PER_HOST=256` |
|---|---:|---:|
| Duração reportada | 10,0500 s | 10,0083 s |
| Respostas HTTP 200 | 34 962 | 112 785 |
| Pedidos por segundo | 3 478,79 | 11 269,15 |
| Latência média | 57,4 ms | 17,7 ms |
| Latência p99 | 281,2 ms | 65,0 ms |
| Latência mínima | 0,5 ms | 0,4 ms |
| Latência máxima | 462,3 ms | 159,8 ms |
| `/stats` antes (`A`) | 1 | 1 |
| `/stats` depois (`B`) | 20 662 | 202 |
| Ligações novas estimadas (`B-A-1`) | 20 660 | 200 |
| Ligações novas por 100 respostas | 59,09 | 0,177 |

Todas as respostas contabilizadas pelo `hey` nas duas execuções foram `200 OK`.

**Nota sobre a medição:** o rácio de ligações novas por resposta é uma métrica agregada de comparação. Não significa que exatamente essa percentagem dos pedidos, considerados individualmente, tenha aberto uma ligação TCP. O contador do stub mede novas ligações TCP (`http.StateNew`), não pedidos HTTP.

### 2.3 Interpretação

Na medição repetida, o pool de 256 registou cerca de **3,24 vezes mais pedidos por segundo** do que o pool efetivo de 2. A latência média baixou de 57,4 ms para 17,7 ms e o p99 baixou de 281,2 ms para 65,0 ms.

A estimativa de novas ligações foi de 20 660 no caso do pool efetivo de 2 e de 200 no caso do pool de 256. Normalizado pelo número de respostas, isso corresponde a cerca de 59,09 ligações novas por cada 100 respostas no primeiro caso e 0,177 no segundo. Este padrão é compatível com muito maior reutilização de ligações no segundo cenário.

A explicação é que `MaxIdleConnsPerHost` limita quantas ligações inativas são conservadas para reutilização; não limita o número total de ligações ativas. Com 200 clientes concorrentes, o Gateway pode precisar de abrir aproximadamente 200 ligações em simultâneo. Com um pool efetivo de 2, muitas ligações concluídas não cabem na pool de inativas e não ficam disponíveis para pedidos seguintes, provocando criação repetida de ligações TCP. Com capacidade 256, as cerca de 200 ligações podem ser conservadas e reutilizadas.

Os resultados são específicos destas execuções e deste ambiente; não são uma garantia de desempenho para qualquer máquina ou carga.

### 2.4 Baseline com 10 clientes concorrentes através do Gateway

Foi também feita uma execução com `MAX_IDLE_PER_HOST=256` e 10 clientes concorrentes:

```bash
hey -z 10s -c 10 http://localhost:8080/fast
```

| Métrica | Resultado |
|---|---:|
| Duração reportada | 10,0008 s |
| Respostas HTTP 200 | 100 818 |
| Pedidos por segundo | 10 080,96 |
| Latência média | 1,0 ms |
| Latência p99 | 3,3 ms |
| Latência mínima | 0,2 ms |
| Latência máxima | 44,3 ms |

Todas as respostas observadas foram `200 OK`.

A estimativa `10 / 10080,96 ≈ 0,992 ms` é coerente com a média reportada de aproximadamente 1,0 ms. Num teste de ciclo fechado, em que cada um dos 10 clientes aguarda pela resposta antes de emitir o pedido seguinte, `concorrência / throughput` deriva diretamente destes valores e não é uma validação independente do resultado. O p99 de 3,3 ms continua a ser relevante porque mostra uma cauda de latência superior à média e ao objetivo de referência de 2 ms usado nesta análise.

### 2.5 Baseline direto ao stub, sem Gateway

Para separar a medição direta do backend da medição através do Gateway, foi executado:

```bash
hey -z 10s -c 10 http://localhost:9000/fast
```

| Métrica | Stub direto |
|---|---:|
| Duração reportada | 10,0006 s |
| Respostas HTTP 200 | 300 084 |
| Pedidos por segundo | 30 006,71 |
| Latência média | 0,3 ms |
| Latência p99 | 1,1 ms |
| Latência mínima | 0,1 ms |
| Latência máxima | 123,6 ms |

Todas as respostas observadas foram `200 OK`.

Comparando com o baseline anterior de 10 clientes através do Gateway, o p99 observado foi de 3,3 ms através do Gateway e 1,1 ms diretamente ao stub, uma diferença de 2,2 ms. A média foi de aproximadamente 1,0 ms através do Gateway e 0,3 ms diretamente, uma diferença de aproximadamente 0,7 ms. Estes valores são uma comparação empírica entre execuções, não uma medição exata e isolada do custo interno do Gateway: percentis de duas distribuições não podem ser subtraídos para calcular o percentil de um componente, e as execuções podem sofrer variação de carga e de ambiente. Ainda assim, a comparação ajuda a definir o que deve ser monitorizado pelo SLO: a latência observada pelo cliente através do Gateway.

### 2.6 Previsão original, resultado e aprendizagem

A previsão original era de **2 ligações novas para o cenário com pool efetivo de 2 e 10 para o cenário com pool 256**. A previsão estava errada: as medições controladas deram, respetivamente, 20 660 e 200 ligações novas estimadas.

O erro foi confundir a capacidade da pool de ligações inativas com o total de ligações TCP que podem ser abertas durante uma execução. Um pool efetivo de 2 não limita o Gateway a abrir apenas duas ligações; limita quantas ligações inativas podem ficar guardadas para reutilização por host. Sob concorrência de 200 clientes, podem ser abertas muitas ligações ativas, e uma pool muito pequena provoca churn quando as ligações terminam. Por outro lado, o cenário com pool 256 precisou de aproximadamente 200 ligações novas, valor coerente com a concorrência usada no teste, e pôde reutilizá-las nos pedidos seguintes.

## 3. Testes de mutação

Os testes de mutação consistiram em introduzir deliberadamente regressões, executar o teste relevante e confirmar que o teste detetava o comportamento incorreto. As alterações de mutação devem ser revertidas após cada experiência.

### Mutação A — Remover `withTimeout`

Foi removida temporariamente a aplicação de `withTimeout` e executado:

```bash
go test -v ./internal/proxy -run TestSlowBackend_Returns504AndBackendSeesCancel
```

Resultado observado:

```text
=== RUN   TestSlowBackend_Returns504AndBackendSeesCancel
    proxy_test.go:40: esperava 504, obtive 200
--- FAIL: TestSlowBackend_Returns504AndBackendSeesCancel (5.00s)
FAIL
```

**Interpretação:** a mutação foi detetada. Sem o timeout, o pedido deixou de devolver o `504` esperado e terminou com `200` após o backend completar o trabalho. O teste protege contra a remoção acidental do timeout do Gateway.

### Mutação B — Trocar `502` por `500`

Foi alterado temporariamente o código de tratamento de erros para devolver `500` em vez de `502`. Em seguida, foi executado:

```bash
go test -v ./internal/proxy -run TestBackendDown_Returns502Fast
```

Resultado observado:

```text
=== RUN   TestBackendDown_Returns502Fast
2026/10/29 01:32:49 [7416f761ce6463af] 500: dial tcp 127.0.0.1:51770: connect: connection refused
    proxy_test.go:69: esperava 502, obtive 500
--- FAIL: TestBackendDown_Returns502Fast (0.00s)
FAIL
```

**Interpretação:** a mutação foi detetada. O teste identificou o código HTTP incorreto e protege contra uma regressão que transforme uma falha de comunicação com o backend num `500 Internal Server Error`.

As falhas destes dois comandos eram o resultado esperado durante as experiências de mutação; não eram falhas inesperadas da implementação original. Depois das experiências, devem ser repostas as implementações corretas: timeout ativo e resposta `502` para backend indisponível.


### Porque é que o teste de cancelamento do cliente não substitui o teste de timeout do Gateway?

O teste `TestClientCancel_CancelsBackend` usa um contexto do cliente com timeout de 200 ms. É o próprio cliente que cancela o pedido; o `net/http` e o `ReverseProxy` propagam esse cancelamento ao backend. Por isso, esse teste pode continuar a passar mesmo que o `withTimeout` do Gateway seja removido: o cancelamento tem outra origem.

O teste `TestSlowBackend_Returns504AndBackendSeesCancel` cobre o caso distinto em que o próprio Gateway impõe `RequestTimeout` e deve devolver `504`. Os dois testes verificam comportamentos diferentes e são complementares.

## 4. Passo E — Encerramento gracioso

Foi iniciado o Gateway e executado um pedido ao endpoint lento:

```bash
curl -v "http://localhost:8080/slow?ms=4000"
```

Enquanto o pedido estava em curso, foi enviado `Ctrl+C` ao terminal do Gateway. O Gateway apresentou:

```text
2026/10/29 01:41:33 gateway em :8080 → http://localhost:9000
^C2026/10/29 01:41:53 a terminar: a deixar os pedidos em curso acabarem...
2026/10/29 01:41:55 terminado
```

No terminal do `curl`, foi recebida a resposta completa:

```text
< HTTP/1.1 200 OK
< Content-Length: 4
< Content-Type: text/plain; charset=utf-8
< Date: Thu, 29 Oct 2026 01:41:55 GMT
< X-Request-Id: a32ba75bc1dbd204
< Connection: close

done
```

**Resultado: passou.** O Gateway iniciou o encerramento gracioso, aguardou pelo pedido em curso e terminou depois de o cliente receber `200 OK` e o corpo completo `done`. O cabeçalho `Connection: close` é compatível com o fecho da ligação após a resposta.

Esta experiência confirma o comportamento neste cenário específico. Não demonstra, por si só, o comportamento quando o período de encerramento excede o limite configurado ou quando existem muitos pedidos ativos.

## 5. Passo F — Experiência com `WriteTimeout`

O `WriteTimeout` foi temporariamente reduzido para 500 ms, mantendo `REQUEST_TIMEOUT=1s`. O pedido de teste pediu ao backend um atraso de 2 segundos:

```bash
curl -v --max-time 4 "http://localhost:8080/slow?ms=2000"
```

O cliente estabeleceu a ligação e enviou o pedido, mas terminou com:

```text
* Empty reply from server
* Closing connection
curl: (52) Empty reply from server
```

**Resultado observado:** o cliente não recebeu uma resposta HTTP válida antes de a ligação ser fechada. O registo do Gateway para o tratamento do timeout apresentou o estado `504` e o erro `context deadline exceeded`, enquanto o `curl` recebeu o erro 52 e não um HTTP `504`.

Isto é compatível com a expiração do `WriteTimeout` antes de o Gateway conseguir escrever a resposta de erro. O log interno pode dizer que o Gateway tentou tratar o pedido como `504`, mas isso não prova que o cliente recebeu esse código. O output do `curl`, por si só, também não permite isolar definitivamente a causa do fecho.

**Porque importa:** dashboards baseados apenas nos logs internos podem contar pedidos classificados como `504` sem distinguir se o cliente recebeu um `504` HTTP válido ou uma falha de transporte sem resposta. Em produção, é útil distinguir erro interno, código HTTP pretendido, resposta efetivamente escrita e erro observado pelo cliente.

**Conclusão:** um timeout de escrita demasiado curto pode impedir o cliente de receber uma resposta HTTP útil. Após a experiência, a configuração deve ser reposta para:

```go
WriteTimeout: 15 * time.Second,
```

## 6. Experiência com `X-Forwarded-For`

Foi enviado um valor manual no cabeçalho `X-Forwarded-For`:

```bash
curl -s -H "X-Forwarded-For: 1.2.3.4" \
  http://localhost:8080/headers
```

O backend apresentou, entre outros, os seguintes cabeçalhos:

```text
X-Forwarded-Host: [localhost:8080]
X-Forwarded-Proto: [http]
X-Forwarded-For: [::1]
```

Apesar de o cliente ter enviado `1.2.3.4`, o backend recebeu `::1`, o endereço IPv6 de loopback. Neste cenário local, o resultado é compatível com o comportamento de `SetXForwarded()` no `ReverseProxy`, que define o cabeçalho com base no endereço remoto observado pelo Gateway, em vez de preservar cegamente o valor fornecido pelo cliente.

**Consideração de segurança:** `X-Forwarded-For` não deve ser tratado automaticamente como prova do IP real do cliente. Um cliente pode enviar valores arbitrários. Ao interpretar uma cadeia de endereços, deve começar-se pela direita e recuar apenas através de proxies explicitamente considerados de confiança. Se o Gateway estiver atrás de um proxy de confiança, será necessária uma política explícita para validar esse proxy e determinar que endereços encaminhados podem ser aceites.

Esta experiência testa o cenário local com uma ligação direta ao Gateway. Não valida ainda cenários com vários proxies ou uma política de confiança configurada.


## 7. `Rewrite` versus `Director` e confiança em proxies

A documentação oficial de [`net/http/httputil.ReverseProxy`](https://pkg.go.dev/net/http/httputil#ReverseProxy) marca `Director` como obsoleto e recomenda `Rewrite`.

Há duas razões de segurança principais:

1. **Cabeçalhos hop-by-hop:** com `Director`, esses cabeçalhos eram removidos depois de a função terminar. Um cliente podia listar um cabeçalho no cabeçalho `Connection`, fazendo com que fosse considerado hop-by-hop e removido, mesmo que o `Director` o tivesse acabado de adicionar. `Rewrite` é chamado depois da remoção dos cabeçalhos hop-by-hop, pelo que os cabeçalhos adicionados pela função não ficam expostos a esse mesmo problema de ordem.
2. **Cabeçalhos de encaminhamento falsificados:** com `Director`, os cabeçalhos `X-Forwarded-For`, `X-Forwarded-Host` e `X-Forwarded-Proto` recebidos do cliente eram preservados por omissão. `Rewrite` remove os cabeçalhos de encaminhamento recebidos antes de executar a função; `SetXForwarded()` pode então construir valores novos a partir da informação disponível ao proxy.

A experiência local enviou `X-Forwarded-For: 1.2.3.4`, mas o backend recebeu `X-Forwarded-For: [::1]`. `::1` é o endereço IPv6 de loopback. Este resultado é compatível com `SetXForwarded()` ter usado o endereço remoto observado pelo Gateway, em vez de aceitar cegamente o valor fornecido pelo cliente.

**Regra de confiança:** não se deve aceitar um `X-Forwarded-For` como prova do IP do cliente apenas porque o cabeçalho existe. A aplicação deve validar se o `RemoteAddr` é um proxy explicitamente confiável e recuar pela cadeia de endereços da direita para a esquerda apenas através de proxies de confiança conhecidos. A experiência local não valida uma topologia com múltiplos proxies.

### Nota sobre IPv6 e prefixos `/64`

Os endereços IPv6 temporários podem mudar ao longo do tempo dentro de um prefixo, uma técnica descrita no [RFC 8981](https://www.rfc-editor.org/rfc/rfc8981.html). Uma política de rate limiting que use apenas o endereço IPv6 exato pode, em certos cenários, ser contornada por mudanças de endereço dentro do mesmo prefixo. Agrupar por `/64` é uma opção de desenho a avaliar, não uma regra universal: pode reduzir essa evasão, mas também pode agrupar vários dispositivos legítimos e provocar limites partilhados. A decisão deve considerar a origem do tráfego, os proxies confiáveis, os requisitos de privacidade e o risco de afetar utilizadores diferentes que partilham um prefixo.

## 8. Resumo e verificações finais

| Área | Resultado |
|---|---|
| Timeout do Gateway e cancelamento no backend | Teste concebido para verificar `504` e propagação de cancelamento; a mutação sem timeout foi detetada. |
| Backend indisponível | O teste verifica `502` rápido; a mutação para `500` foi detetada. |
| Cancelamento iniciado pelo cliente e goroutines | `TestClientCancel_CancelsBackend` passou; o teste foi executado com sucesso no pacote e não foi reportada uma data race. |
| Pool de ligações | Com 200 clientes, o pool 256 registou 11 269,15 pedidos/s e p99 de 65,0 ms, contra 3 478,79 pedidos/s e p99 de 281,2 ms no pool efetivo 2; a estimativa foi de 200 contra 20 660 ligações novas. |
| Encerramento gracioso | Confirmado: o cliente recebeu `200 OK` e `done` antes do fim do encerramento. |
| `WriteTimeout` curto | O cliente recebeu `curl: (52) Empty reply from server`, conforme observado. |
| `X-Forwarded-For` | O backend recebeu `::1` em vez do valor manual `1.2.3.4` neste cenário local; a confiança deve depender de proxies explicitamente confiáveis. |
| Latência do Gateway vs stub | Com 10 clientes, p99 observado de 3,3 ms através do Gateway e 1,1 ms diretamente ao stub; diferença descritiva de 2,2 ms entre execuções. |

### Verificações executadas em 29 de outubro de 2026

Foi executado o comando pedido para uma execução sem cache, com o detector de data races:

```bash
go test -race -count=1 -v ./internal/proxy/
```

Resultado observado: os três testes (`TestSlowBackend_Returns504AndBackendSeesCancel`, `TestBackendDown_Returns502Fast` e `TestClientCancel_CancelsBackend`) passaram; o pacote terminou com `PASS` e não foi reportada nenhuma data race.

Também foi executado:

```bash
go test ./... -v
```

Resultado observado: os testes do pacote `internal/proxy` passaram e os pacotes `cmd/gateway` e `cmd/stub-backend` não têm testes próprios (`[no test files]`).

**Estado do Git:** o output de `git status` não foi incluído nas evidências partilhadas, pelo que o estado limpo do working tree ainda não está confirmado. Para o verificar, executar:

```bash
git status
```

Se ainda houver alterações legítimas por registar, não se deve afirmar que o working tree está limpo. Confirmar também que as mutações temporárias foram revertidas e que a configuração normal mantém `WriteTimeout: 15 * time.Second`, o timeout do pedido ativo e o tratamento `502` para falhas de ligação ao backend.

O registo descreve os resultados observados neste ambiente; não constitui uma garantia geral de desempenho ou de segurança para todas as máquinas e topologias de rede.
