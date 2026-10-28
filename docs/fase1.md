# Previsão

## 1. MAX_IDLE_PER_HOST=0
### R.: 2

## 2. MAX_IDLE_PER_HOST=256
### R.: 10

# TESTE


## (ANTES) 1. MAX_IDLE_PER_HOST=0
### R.: 1

## TESTE DE 200 PEDIDOS:  hey -z 10s -c 200 http://localhost:8080/fast 

## (DEPOIS) 1. MAX_IDLE_PER_HOST=0
### R.: 21252

## Aparece o 1.2.3.4? O que lá está? (Não)

# Imagina que na Fase 4 o limite por IP lê o primeiro valor desse cabeçalho. O que faz um atacante para ter quotas infinitas?
## Um atacante poderia enviar pedidos com valores diferentes
## Se o Gateway aceitar esses valores sem verificar a sua origem, o atacante pode fingir ser muitos IPs diferentes.
## O sistema poderá interpretar cada valor como um cliente novo, atribuindo-lhe uma quota independente. Assim, o atacante consegue contornar o limite.
## Isto chama-se falsificação de cabeçalhos (header spoofing).

## Em que situação seria correto confiar num X-Forwarded-For recebido?
## É correto confiar no X-Forwarded-For quando o pedido chega através de um proxy de confiança, como o NGINX, e o Gateway está configurado para aceitar esse cabeçalho apenas desse proxy.
