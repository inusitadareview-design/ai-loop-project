# SPEC — Arquitetura do AI Loop Project

## 1. Visão Geral

O AI Loop Project executa loops de trabalho autônomos
(**planejar → executar → revisar → publicar**) roteando chamadas de IA via
OmniRoute, com otimização de tokens e memória comprimida em Rust, e
orquestração/agendamento/CLI/API em Go. Dashboard React para observabilidade.

Princípios:

- **Go** = orquestração e I/O; **Rust** = hot paths determinísticos;
  fronteira entre eles via FFI/CGO estável (ver GUARDRAILS G5.3).
- Toda ação externa é governada por `config/settings.yaml` + guardrails.
- Falhas seguem as árvores de decisão de `docs/DECISIONS.md`.

## 2. Estrutura de Pastas (obrigatória)

```
ai-loop-project/
├── cmd/ailoop/              # Entry point da CLI (cobra)
├── internal/
│   ├── engine/              # Pipeline engine (loop planejar→executar→revisar→publicar)
│   ├── router/              # Roteamento de provedores (fallback chain)
│   ├── omniroute/           # Integração OmniRoute + bridge FFI (CGO)
│   ├── memory/              # Memória persistente (append-only + compressão)
│   ├── scheduler/           # Agendador (cron-like, lock por pipeline)
│   ├── pipeline/            # Definições/templates de pipelines YAML
│   │   └── templates/
│   ├── api/                 # HTTP handlers (chi)
│   │   ├── routes/
│   │   └── middleware/
│   └── observability/       # Logger estruturado zerolog + métricas
├── pkg/
│   ├── config/              # Carregamento/validação de settings.yaml
│   └── ffi/                 # Biblioteca estática Rust compilada (.a) + headers
├── rust/                    # Workspace Rust (crates)
│   ├── token-budget/        # Validação de orçamento/custos por chamada
│   ├── payload-optimizer/   # Compressão/normalização de payloads
│   ├── prompt-builder/      # Montagem determinística de prompts
│   ├── memory-compressor/   # Compressão semântica de memória
│   ├── cache-engine/        # Cache de respostas (data/cache)
│   └── ffi/                 # Camada C-ABI consumida pelo Go
├── web/                     # Dashboard React (TypeScript)
│   └── src/
│       ├── api/
│       └── components/
├── config/
│   ├── settings.yaml        # Configuração padrão global
│   └── pipelines/           # Pipelines nomeados (ex.: news_blog.yaml)
├── data/                    # Runtime (gitignored)
│   ├── memory/  outputs/  logs/  cache/
├── docs/                    # SPEC.md, GUARDRAILS.md, DECISIONS.md
├── scripts/                 # build.sh, helpers
├── tests/
│   ├── go/  rust/  e2e/
├── Makefile
└── go.mod
```

## 3. Componentes

### 3.1 CLI (`cmd/ailoop`)

Comandos (cobra): `run`, `discover`, `models`, `combo`, `pipelines`,
`memory`, `schedule`, `budget`, `dashboard`, `status`.
Em Sprint 1 os subcomandos são placeholders; `discover` já exercita a FFI.

### 3.2 Observability (`internal/observability`)

- Logger zerolog JSON escrevendo em stdout **e** `data/logs/ai_loop.log`.
- Rotação: arquivo até 10MB, 5 backups (implementação própria, sem deps).
- API: `NewLogger()`, `WithFields(map[string]any)`, `Debug/Info/Warn/Error`.

### 3.3 Config (`pkg/config`)

- `Load(path string) (*Config, error)` lê YAML, aplica defaults e valida.
- Structs tipadas por seção: `providers`, `routing`, `budget`, `logging`,
  `paths`, `scheduler`, `pipeline`.

### 3.4 Rust Core (Sprint 2+) e FFI

Cada crate expõe funções puras `Result<T, E>`. O crate `ffi` expõe C-ABI:

```rust
#[no_mangle]
pub extern "C" fn ffi_hello() -> *const std::os::raw::c_char; // "Hello from Rust FFI"
```

O Go chama via CGO (`internal/omniroute/ffi_bridge.go`), linkando
`pkg/ffi/libailoop_ffi.a` produzido por `scripts/build.sh`.

### 3.5 Engine / Router / Scheduler / Memory / API

Especificados nas Sprints 3–8. Contratos mínimos:

- **Router**: cadeia de fallback de provedores declarada no pipeline YAML;
  nunca ultrapassar `budget.*` (GUARDRAILS G1).
- **Scheduler**: dispara pipelines por cron; respeita lock por pipeline (G4.3).
- **Memory**: eventos append-only em `data/memory/`; compressão via crate Rust.
- **API**: chi, rotas REST sob `/api/v1/*`, healthcheck `/healthz`.

## 4. Pipelines YAML

Um pipeline define: nome, stages (plan/exec/review/publish), modelo preferido,
fallbacks, `require_review`, limites. Exemplo: `config/pipelines/news_blog.yaml`.

## 5. Sprints

| Sprint | Entrega |
|---|---|
| 1 | Fundação: estrutura, go.mod, cargo workspace, logger, config, CLI, FFI hello, build |
| 2 | Rust Core: token-budget, payload-optimizer, prompt-builder, memory-compressor, cache-engine |
| 3 | Router + OmniRoute providers |
| 4 | Engine de pipelines |
| 5 | Memória persistente |
| 6 | Scheduler |
| 7 | API REST |
| 8 | Dashboard web + E2E |

## 6. Build & Run

```bash
./scripts/build.sh   # cargo build -r → copia .a/.h p/ pkg/ffi → go build (CGO)
make build           # idem, gera bin/ailoop
make test            # go test ./... && cargo test
./bin/ailoop --help
./bin/ailoop discover
```
