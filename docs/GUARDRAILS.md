# GUARDRAILS — Regras de Proteção Obrigatórias

> Estas regras são **invioláveis**. Todo código, configuração e pipeline
> deve respeitá-las. Violações devem falhar de forma explícita (erro),
> nunca silenciosamente.

## G1 — Custos e Orçamento

- **G1.1** Nunca exceder o orçamento diário definido em
  `config/settings.yaml` (`budget.daily_limit_usd`). Antes de cada chamada
  externa, o token-budget engine (Rust) deve validar o custo projetado.
- **G1.2** APIs gratuitas: respeitar rate limits declarados
  (`budget.max_requests_per_minute`). Em caso de 429, aplicar backoff
  exponencial (base 2s, máx 5 tentativas) e então pausar a task.
- **G1.3** Proibido trocar para plano pago ou aumentar limites de conta
  automaticamente. Qualquer mudança de billing é decisão humana.

## G2 — Segurança e Segredos

- **G2.1** Nunca commitar segredos. Chaves vivem apenas em
  `config/secrets.yaml` (gitignored) ou variáveis de ambiente
  (`AILOOP_*`). Logs jamais devem conter chaves de API (mascarar como
  `sk-***`).
- **G2.2** Nenhum arquivo fora de `data/` pode ser escrito pelo runtime.
- **G2.3** Proibido executar comandos shell arbitrários vindos de output
  de LLM. Ações permitidas são as declaradas no YAML do pipeline.

## G3 — Proteção de Dados e Memória

- **G3.1** Memória persistente (`data/memory/`) é append-only por evento;
  compressão gera novos arquivos, nunca apaga os originais sem backup.
- **G3.2** Outputs publicados passam por revisão humana quando o pipeline
  define `require_review: true`. Sem aprovação, NÃO publicar.
- **G3.3** Nunca deletar logs ativos; rotação mantém no máximo 5 backups
  conforme política de observabilidade.

## G4 — Comportamento do Loop

- **G4.1** Dead-loop protection: se uma task falhar 3 vezes consecutivas
  com o mesmo erro, ela é colocada em `paused` e sinalizada no dashboard.
- **G4.2** Cada iteração do loop deve terminar com um estado persistido
  (planejar → executar → revisar → publicar / falhar).
- **G4.3** O scheduler nunca inicia nova execução se a anterior da mesma
  pipeline ainda estiver rodando (lock por pipeline).

## G5 — Qualidade de Código

- **G5.1** Go: todo error retornado deve ser tratado explicitamente.
  Proibido `_ = err`. Erros devem ser wrapados com `fmt.Errorf("...: %w", err)`.
- **G5.2** Rust: proibido `unwrap()`/`expect()` em código de produção;
  usar `Result<T, E>`. `unwrap()` permitido apenas em testes.
- **G5.3** FFI: toda fronteira Go↔Rust usa tipos C-ABI simples
  (`*const c_char`, buffers + tamanho) e o chamador Go sempre libera/recopia
  a memória imediatamente após a chamada.
- **G5.4** Nenhuma feature fora do escopo da sprint em andamento.

## G6 — Reversibilidade

- **G6.1** Toda migration de dados/config deve ter caminho de rollback.
- **G6.2** Builds são reprodutíveis: `make build` a partir de um clone limpo
  deve funcionar sem passos manuais além de `scripts/build.sh`.
