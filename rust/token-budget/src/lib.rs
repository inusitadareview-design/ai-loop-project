//! token-budget — validação de orçamento/custo projetado por chamada.
//! Sprint 1: placeholder. Implementação real na Sprint 2.

/// Erros do crate (guardrail G5.2: Result<T, E> em vez de unwrap).
#[derive(Debug, PartialEq)]
pub enum TokenBudgetError {
    InvalidInput(String),
}

/// Placeholder: estima tokens de um texto (~4 chars/token).
pub fn estimate_tokens(text: &str) -> Result<usize, TokenBudgetError> {
    let n = text.len();
    Ok((n / 4).max(if n == 0 { 0 } else { 1 }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn placeholder_estimates_tokens() -> Result<(), TokenBudgetError> {
        assert_eq!(estimate_tokens("")?, 0);
        assert!(estimate_tokens("hello world")? >= 2);
        Ok(())
    }
}
