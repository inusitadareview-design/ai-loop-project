//! prompt-builder — montagem determinística de prompts.
//! Sprint 1: placeholder. Implementação real na Sprint 2.

#[derive(Debug, PartialEq)]
pub struct PromptBuilderError;

/// Placeholder: concatena system + user com separador fixo.
pub fn build(system: &str, user: &str) -> Result<String, PromptBuilderError> {
    Ok(format!("[system]\n{system}\n[user]\n{user}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn placeholder_builds_prompt() -> Result<(), PromptBuilderError> {
        let p = build("sys", "usr")?;
        assert!(p.contains("[system]") && p.contains("usr"));
        Ok(())
    }
}
