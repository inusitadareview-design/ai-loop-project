//! payload-optimizer — compressão/normalização de payloads.
//! Sprint 1: placeholder. Implementação real na Sprint 2.

#[derive(Debug, PartialEq)]
pub enum PayloadOptimizerError {
    InvalidInput(String),
}

/// Placeholder: normaliza espaços em branco do payload.
pub fn normalize(payload: &str) -> Result<String, PayloadOptimizerError> {
    Ok(payload.split_whitespace().collect::<Vec<_>>().join(" "))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn placeholder_normalizes_whitespace() -> Result<(), PayloadOptimizerError> {
        assert_eq!(normalize(" a   b \n c ")?, "a b c");
        Ok(())
    }
}
