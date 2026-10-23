# AI Loop Project

Plataforma de automação de pipelines de IA com roteamento inteligente
via OmniRoute, otimização de tokens e memória comprimida.

## 🎯 Objetivo

Executar loops de trabalho (planejar → executar → revisar → publicar)
com o mínimo de overhead possível, respeitando limites de APIs gratuitas.

## 🏗️ Arquitetura

- **Go**: Orquestração, engine de pipelines, agendador, API REST, CLI
- **Rust**: Hot paths (token budget, cache, prompt builder, compressor)
- **TypeScript/React**: Dashboard web

## 📁 Estrutura
ai-loop-project/
├── cmd/ailoop/ # Entry point Go
├── internal/ # Código Go privado
│ ├── engine/ # Pipeline engine
│ ├── router/ # Roteamento de provedores
│ ├── omniroute/ # Integração OmniRoute
│ ├── memory/ # Memória persistente
│ ├── scheduler/ # Agendador
│ ├── pipeline/ # Definições de pipeline
│ ├── api/ # HTTP handlers
│ └── observability/ # Logs e métricas
├── pkg/config/ # Config pública
├── rust/ # Workspaces Rust
│ ├── token-budget/
│ ├── payload-optimizer/
│ ├── prompt-builder/
│ ├── memory-compressor/
│ ├── cache-engine/
│ └── ffi/ # Bindings Go↔Rust
├── web/ # Dashboard React
├── config/ # YAMLs de configuração
├── data/ # Runtime (memória, outputs, logs)
├── docs/ # Especificações e decisões
└── tests/ # Testes Go, Rust, E2E

## 🚀 Começando

Veja a especificação completa em [docs/SPEC.md](docs/SPEC.md).

## 📜 Licença

MIT

