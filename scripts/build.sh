#!/usr/bin/env bash
# scripts/build.sh — The Bridge
# Compiles Rust FFI library (if present), copies artifacts to pkg/ffi/,
# then compiles Go binary with CGO linking the Rust static library.
#
# This script is TOLERANT: if Rust is not installed or rust/ffi/ does not
# exist, it compiles Go WITHOUT CGO (degraded mode). This allows the
# project to build and run even before Sprint 2 (Rust Core) is complete.
#
# Guardrails enforced:
#   G6.2 — Reproducible builds: runs from clean clone without manual steps
#   G5.3 — FFI boundary: only simple C-ABI types cross the boundary
#
# Usage:
#   ./scripts/build.sh            # Full build (Rust + Go with CGO)
#   ./scripts/build.sh --go-only  # Skip Rust, build Go only (no CGO)
set -euo pipefail

# --- Configuration ---
RUST_DIR="rust/ffi"
FFI_LIB_NAME="libailoop_ffi"
FFI_HEADER="ailoop_ffi.h"
PKG_FFI_DIR="pkg/ffi"
BIN_DIR="bin"
BINARY_NAME="ailoop"
CMD_PATH="./cmd/ailoop"

# --- Color output helpers ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }

# --- Parse arguments ---
GO_ONLY=false
if [[ "${1:-}" == "--go-only" ]]; then
    GO_ONLY=true
fi

# --- Step 1: Rust compilation (unless --go-only or Rust missing) ---
RUST_AVAILABLE=false
if [[ "$GO_ONLY" == false ]]; then
    if command -v cargo &> /dev/null; then
        if [[ -d "$RUST_DIR" ]] && [[ -f "$RUST_DIR/Cargo.toml" ]]; then
            RUST_AVAILABLE=true
            log_info "Building Rust FFI library..."
            (
                cd "$RUST_DIR"
                cargo build --release
            )
            
            # Copy artifacts to pkg/ffi/
            mkdir -p "$PKG_FFI_DIR"
            
            # Find the static library (location varies by target)
            FFI_LIB_PATH=$(find "$RUST_DIR/target/release" -name "${FFI_LIB_NAME}.a" 2>/dev/null | head -n1)
            if [[ -z "$FFI_LIB_PATH" ]]; then
                log_error "Rust build succeeded but ${FFI_LIB_NAME}.a not found"
                exit 1
            fi
            
            cp "$FFI_LIB_PATH" "$PKG_FFI_DIR/"
            log_info "Copied $(basename "$FFI_LIB_PATH") to $PKG_FFI_DIR/"
            
            # Copy header file if it exists
            if [[ -f "$RUST_DIR/$FFI_HEADER" ]]; then
                cp "$RUST_DIR/$FFI_HEADER" "$PKG_FFI_DIR/"
                log_info "Copied $FFI_HEADER to $PKG_FFI_DIR/"
            else
                log_warn "Header file $FFI_HEADER not found in $RUST_DIR (skipping)"
            fi
        else
            log_warn "Rust directory $RUST_DIR not found — building Go without CGO"
        fi
    else
        log_warn "cargo not installed — building Go without CGO (degraded mode)"
    fi
else
    log_info "Skipping Rust build (--go-only flag)"
fi

# --- Step 2: Go compilation ---
log_info "Building Go binary..."
mkdir -p "$BIN_DIR"

if [[ "$RUST_AVAILABLE" == true ]]; then
    # Build with CGO linking the Rust static library
    export CGO_ENABLED=1
    export CGO_LDFLAGS="-L$(pwd)/$PKG_FFI_DIR -l:$(basename "$FFI_LIB_PATH")"
    export CGO_CFLAGS="-I$(pwd)/$PKG_FFI_DIR"
    
    log_info "CGO enabled — linking Rust FFI library"
    go build -trimpath -ldflags="-s -w" -o "$BIN_DIR/$BINARY_NAME" "$CMD_PATH"
else
    # Build without CGO (degraded mode — no Rust features)
    export CGO_ENABLED=0
    log_info "CGO disabled — building without Rust FFI (degraded mode)"
    go build -trimpath -ldflags="-s -w" -o "$BIN_DIR/$BINARY_NAME" "$CMD_PATH"
fi

log_info "Build complete: $BIN_DIR/$BINARY_NAME"