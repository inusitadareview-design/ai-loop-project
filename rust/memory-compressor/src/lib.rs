//! memory-compressor — compressão semântica de memória persistente.
//! Sprint 1: placeholder. Implementação real na Sprint 2.

#[derive(Debug, PartialEq)]
pub struct MemoryCompressorError;

/// Placeholder: tamanho comprimido estimado (nunca apaga originais — G3.1).
pub fn compressed_size(original_bytes: usize) -> Result<usize, MemoryCompressorError> {
    Ok(original_bytes / 2)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn placeholder_estimates_compressed_size() -> Result<(), MemoryCompressorError> {
        assert_eq!(compressed_size(100)?, 50);
        Ok(())
    }
}
