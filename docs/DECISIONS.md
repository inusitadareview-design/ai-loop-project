# DECISIONS — Árvores de Decisão para Falhas

> Formato: **gatilho → checagens → ação**. Toda decisão preserva o
> guardrail correspondente (GUARDRAILS G1–G6). Nunca "engolir" falha:
> se nenhuma ramificação resolver, escalar para `paused + logged error`.

## D1 — Falha de chamada a provedor de IA (router/omniroute)

```
Erro em chamada ao modelo preferido?
├─ Timeout (> routing.timeout_seconds)?
│   ├─ 1ª vez: retry 1x com mesmo provedor
│   └─ persiste: fallback → próximo da cadeia routing.fallback_chain
├─ HTTP 429 (rate limit)?
│   ├─ tentativas < max_requests retries: backoff exponencial (2s base)
│   └─ esgotado: fallback chain; se esgotada → pausar task (G1.2, G4.1)
├─ HTTP 4xx (payload inválido)?
│   ├─ token budget excedido? → payload-optimizer comprime e tenta 1x
│   └─ ainda inválido → falhar task com erro explícito (sem retry cego)
└─ HTTP 5xx / rede?
    └─ até 3 retries com backoff; depois próximo provedor da fallback chain
Se TODOS os provedores falharem → status=blocked, logar, notificar dashboard
```

## D2 — Orçamento estourada (budget)

```
Custo projetado da chamada > restante do dia (budget.daily_limit_usd)?
├─ restante > 0: reduzir max_tokens via token-budget e revalidar
├─ restante == 0: colocar fila em espera até janela UTC seguinte
└─ NUNCA migrar para plano pago automaticamente (G1.3)
```

## D3 — Cache (cache-engine)

```
Resposta solicitada existe em data/cache?
├─ hit válido (TTL ok): servir do cache, custo = 0
├─ hit expirado: revalidar com provedor barato primeiro
└─ miss: chamar provedor; gravar resposta; se disco cheio → log warn, seguir sem cache
Falha de escrita de cache JAMAIS derruba a pipeline.
```

## D4 — Memória (memory-compressor)

```
Arquivo de memória corrompido/incompleto na leitura?
├─ último registro truncado? → descartar só o último registro, manter resto
├─ arquivo ilegível? → usar último backup (.bak) disponível
└─ nenhum backup? → recriar vazio e logar ERROR com hash do arquivo perdido
Compressão produz novo arquivo + mantém original por 7 dias (G3.1)
```

## D5 — Scheduler

```
Job dispara e já existe execução da mesma pipeline?
├─ lock com < max_runtime: ignorar disparo (log info) (G4.3)
└─ lock órfão (processo morto): remover lock com log WARN e assumir
Pipeline falhou 3x seguidas com mesmo erro? → paused + alerta (G4.1)
```

## D6 — Publicação (publish stage)

```
require_review: true no pipeline YAML?
├─ sim: gerar draft em data/outputs/, aguardar aprovação humana (G3.2)
└─ não: publicar; se publicar falhar:
    ├─ erro transitório (rede/5xx): 3 retries com backoff
    └─ erro permanente (auth/moderação): parar, salvar rascunho, escalar
Nunca republicar conteúdo parcialmente publicado sem verificação de estado.
```

## D7 — FFI Go↔Rust

```
Chamada FFI retorna NULL/erro?
├─ logar ERROR com nome da função e contexto
├─ tratar como falha da hot path → usar caminho Go degradado quando existir
└─ nunca continuar com valor indefinido/memória não validada (G5.3)
```
