//! cache-engine — cache de respostas com TTL em data/cache.
//! Sprint 1: placeholder. Implementação real na Sprint 2.

#[derive(Debug, PartialEq)]
pub struct CacheEngineError;

/// Placeholder: chave de cache determinística (hash FNV-1a simples).
pub fn cache_key(input: &str) -> Result<u64, CacheEngineError> {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in input.as_bytes() {
        h ^= *b as u64;
        h = h.wrapping_mul(0x0000_0100_0000_01b3);
    }
    Ok(h)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn placeholder_cache_key_is_deterministic() -> Result<(), CacheEngineError> {
        assert_eq!(cache_key("abc")?, cache_key("abc")?);
        assert_ne!(cache_key("abc")?, cache_key("abd")?);
        Ok(())
    }
}
