# DESIGN.md

# Algoritmos

## Comparação de Métodos

| Algoritmo | Estado Guardado | Consumo de Memória | Comportamento no Cenário (100 req às 12:00:59 e 100 req às 12:01:01) | Principal Defeito |
| :--- | :--- | :--- | :--- | :--- |
| **Fixed Window** | 1 número (contador) + timestamp do minuto | Baixo e estático | **Permite todas as 200 requisições.** Ocorre o reset na viragem do minuto (12:01:00). | Permite o dobro da quota contratada em janelas de transição (picos de tráfego na borda). |
| **Sliding Window Log** | Lista dinâmica com o timestamp exato de cada pedido | Alto (cresce linearmente com o tráfego) | **Bloqueia os 100 pedidos das 12:01:01.** O intervalo dinâmico deteta as 100 requisições feitas 2 segundos antes. | Inviável para alto volume de tráfego devido ao consumo massivo de memória RAM. |
| **Sliding Window Counter** | 2 números (contador da janela atual e da janela anterior) | Baixo e estático | **Bloqueia a maioria das requisições das 12:01:01**, mas o cálculo é uma estimativa matemática baseada em média ponderada. | **Imprecisão matemática:** Assume distribuição uniforme. Se o tráfego anterior ocorreu todo nos últimos 5 segundos do minuto, o cálculo falha e pode deixar passar até 50% mais pedidos do que a quota. |
| **Token Bucket** | 2 números (saldo de fichas + timestamp da última recarga) | Baixo e estático | **Bloqueia as 100 requisições das 12:01:01.** Em 2 segundos, o balde só regenera uma fração irrelevante de fichas. | Permite a passagem de picos isolados (*bursts*) equivalentes à capacidade máxima do balde, o que pode sobrecarregar o backend imediatamente se o balde for muito grande. |

## Escolha de Arquitetura e Estratégia

*   **Para limite por IP: Token Bucket**
    *   **Justificação:** Protege a infraestrutura contra abusos mantendo uma pegada de memória mínima e estática por IP. Tolera pequenos picos legítimos causados por renderização de páginas web sem penalizar o utilizador, bloqueando apenas o comportamento sistemático de bots.
*   **Para limite por Tenant: Sliding Window Log**
    *   **Justificação:** Se o contrato de serviço (*SLA*) com o tenant exige de forma estrita que a quota nunca seja ultrapassada em qualquer intervalo de 60 segundos, o *Sliding Window Counter* não serve devido à sua margem de erro por aproximação. Como o número de tenants corporativos é exponencialmente menor do que o número de IPs públicos, o custo de memória do *Sliding Window Log* torna-se sustentável e justifica a precisão necessária para o faturamento.

## O Desafio: Manter dois algoritmos vs. Token Bucket para tudo
Manter dois algoritmos aumenta a complexidade de manutenção, testes unitários e probabilidade de bugs no Gateway. No entanto, a decisão justifica-se pela **assimetria de requisitos**: o limite por IP exige escala massiva e eficiência máxima de memória sacrificando a precisão absoluta; o limite por tenant exige precisão contratual absoluta onde a volumetria permite um custo de memória superior. Usar *Token Bucket* para o tenant permitiria *bursts* que violam a barreira estrita de tráfego por minuto contratada.

# Atomicidade

## Justificativa e Falhas de Concorrência

A execução através de comandos isolados gera condições de corrida (*race conditions*) e inconsistências de estado que invalidam o limite de taxa.

### Falha 1: Queda do Gateway entre INCR e EXPIRE
No modelo `INCR` seguido de `EXPIRE`, se o processo do Gateway sofrer um *crash* ou perder a ligação à rede exatamente entre os dois comandos, a chave do Redis é incrementada mas o tempo de vida (*TTL*) nunca é configurado.
*   **Impacto:** A chave torna-se persistente no Redis. O contador acumulado nunca mais sofrerá *reset*, resultando no bloqueio permanente e definitivo desse IP, mesmo após hours ou dias de inatividade.

### Falha 2: Operações não-atómicas (Ler -> Decidir -> Incrementar)
Quando múltiplos Gateways executam estes passos de forma separada, ocorre uma violação de concorrência. Se dois Gateways leem em simultâneo o valor "99" para um limite de 100:
*    Ambos avaliam a condição como verdadeira (99 < 100) e autorizam o pedido.
*    Ambos enviam o incremento de seguida, elevando o contador para 101.
*    **Impacto:** O limite do sistema é violado e o tráfego acima da quota é processado pelo backend.

### Limitações do MULTI/EXEC
O comando `MULTI/EXEC` apenas enfileira operações no Redis para execução sequencial, agindo em modo de pipeline cego.
*   **O problema para o Limiter:** Não é possível ler o valor do contador e usar o resultado no meio do bloco para tomar a decisão lógica de incrementar ou bloquear (`if contador < limite`). O Redis só devolve as respostas após o comando `EXEC`. Sem o uso de `WATCH` (que gera rejeições por concorrência otimista sob alta carga), o `MULTI/EXEC` é incapaz de resolver o problema do limiter.

### Solução com Script Lua
Os scripts Lua eliminam estes cenários porque o Redis os executa de forma síncrona, isolada e atómica **ao nível do nó**. Nenhum outro comando de nenhum outro Gateway consegue intervir durante a execução do script. O bloco de lógica passa a ser executado inteiramente no servidor Redis: se o Gateway falhar na rede, a operação ou não acontece ou é executada até ao fim (garantindo o `EXPIRE`).

# Caso de falha

## Estratégia de Fallback por Rota

*   **Rota de Login: Fail-Closed**
    *   **Justificação:** Privilegia a segurança. Em caso de indisponibilidade do Redis, a rota de autenticação bloqueia requisições automatizadas não controladas ou exige verificação complementar (ex: ativação forçada de CAPTCHA no Gateway). Permitir *fail-open* exporia o sistema a ataques de força bruta (*credential stuffing*) massivos precisamente no momento de maior vulnerabilidade da infraestrutura. O custo de rejeitar temporariamente alguns acessos legítimos é inferior ao risco de comprometimento de contas.
*   **Rota de Leitura Pública (Listagem de Produtos): Fail-Open**
    *   **Justificação:** Privilegia a disponibilidade do negócio. Bloquear esta rota devido a uma falha no sistema de monitorização transformaria o rate limiter no ponto único de falha (*SPOF*) de toda a plataforma, interrompendo a faturação da empresa.

## Mecanismo de Meio-Termo: Local In-Memory Fallback
Para evitar o impacto total do *fail-open* ou do *fail-closed*, o Gateway implementa um mecanismo de contingência local. Ao detetar a queda do Redis, o Gateway ativa um **Rate Limiter em memória RAM local** (usando uma estrutura simples e estática de *Token Bucket* por instância do Gateway).

Embora os contadores deixem de ser globais e partilhados entre as réplicas da infraestrutura, cada Gateway passa a defender-se de forma autónoma, impedindo que um único IP ou bot monopolize os recursos do backend enquanto o Redis recupera.

# p99 < 2ms

## Definição do Acordo de Nível de Serviço (SLO)

Para que o indicador de latência tenha validade técnica e reprodutibilidade, o SLO fica formalmente definido através dos seguintes parâmetros de teste:

> **"p99 < 2ms, medido desde a receção do primeiro byte do pedido no socket do Gateway até ao envio do último byte da resposta ao cliente, em topologia de rede local de baixa latência com instâncias EC2 c6i.xlarge dedicadas e isoladas para o Gerador de Carga, Gateway (Go) e Redis (nós separados), a 50.000 req/s constantes durante 10 minutos, com 10.000 IPs e 1.000 tenants distintos, usando k6."**

A medição isola estritamente o Gateway e o Redis, simulando a concorrência real através de chaves distribuídas. Para garantir a segurança total da infraestrutura sob stress extremo, definem-se também os limites de cauda: **p99.9 < 5ms** e **Máximo absoluto < 15ms**.

## Coordinated Omission (Omissão Coordenada)
A omissão coordenada ocorre quando a ferramenta de testes aguarda o fecho ou processamento de um pedido lento antes de enviar o próximo na mesma linha de execução.
*   **Impacto no resultado:** Ao abrandar o ritmo de envio precisamente quando o sistema está congestionado, a ferramenta deixa de emitir e registar os centenas de pedidos que deveriam ter sofrido com o atraso, gerando relatórios com métricas de p99 falsamente otimistas.

# Teste Rápido de Conceito: Chaves sem Chavetas `{}`

Se as chaves de um tenant fossem configuradas exatamente como `tenant:acme:contador` e `tenant:acme:ultimo_pedido`, sem a utilização de chavetas:

*   **O que acontecia:** A execução do script Lua que tenta aceder a ambas as chaves em simultâneo **falhava imediatamente, devolvendo o erro `CROSSSLOT Keys in request don't hash to the same slot`**.
*   **O Motivo:** Sem as chavetas, o algoritmo de hashing do Redis Cluster (*CRC16*) processa o nome completo de cada chave de forma independente. Como as strings completas são ligeiramente diferentes no final (`...contador` vs `...ultimo_pedido`), os resultados do cálculo matemático apontam para *hash slots* distintos. O Redis Cluster proíbe terminantemente a execução de scripts Lua que envolvam chaves localizadas em slots diferentes, impossibilitando a garantia de atomicidade se os slots residirem em nós/servidores físicos distintos.