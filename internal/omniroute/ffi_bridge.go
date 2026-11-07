package omniroute

/*
#cgo LDFLAGS: -L${SRCDIR}/../../pkg/ffi -l:libailoop_ffi.a -lm -ldl -lpthread
#cgo CFLAGS: -I${SRCDIR}/../../pkg/ffi
#include <stdlib.h>
#include "ailoop_ffi.h"
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"unsafe"
)

// Hello returns the handshake string from Rust (Sprint 1).
// The returned string is from static memory — do NOT modify it.
func Hello() (string, error) {
	ptr := C.ffi_hello()
	if ptr == nil {
		return "", fmt.Errorf("ffi_hello returned NULL")
	}
	// Static memory — DO NOT free
	return C.GoString(ptr), nil
}

// MemoryCompress compresses memory events JSON via Rust (Sprint 5).
// Caller receives owned Go string; Rust memory is freed immediately.
func MemoryCompress(jsonEvents string) (string, error) {
	cStr := C.CString(jsonEvents)
	defer C.free(unsafe.Pointer(cStr))

	resultPtr := C.ffi_memory_compress(cStr)
	if resultPtr == nil {
		return "", fmt.Errorf("ffi_memory_compress returned NULL (parse/compress error)")
	}
	defer C.ffi_free_string(resultPtr)

	return C.GoString(resultPtr), nil
}

// TokenBudgetResult is the response from ValidateTokenBudget.
type TokenBudgetResult struct {
	Fits             bool    `json:"fits"`
	FitsBudget       bool    `json:"fits_budget"`
	FitsTokenLimit   bool    `json:"fits_token_limit"`
	EstimatedTokens  int     `json:"estimated_tokens"`
	ProjectedCostUSD float64 `json:"projected_cost_usd"`
	RemainingUSD     float64 `json:"remaining_budget_usd"`
	Error            string  `json:"error,omitempty"`
}

// ValidateTokenBudget validates that a prompt fits within budget constraints
// via the Rust token-budget crate. Enforces invariant I1 (Token Pre-Validation).
//
// Returns error ONLY on FFI failure (NULL pointer, invalid UTF-8, serialization).
// A call that simply exceeds the budget is NOT an error — result.Fits will be false.
func ValidateTokenBudget(
	prompt string,
	dailyLimitUSD float64,
	spentUSD float64,
	costPer1kInputTokens float64,
	maxTokensPerRequest int,
) (TokenBudgetResult, error) {
	// Build JSON input for Rust FFI
	input := map[string]interface{}{
		"prompt":                   prompt,
		"daily_limit_usd":          dailyLimitUSD,
		"spent_usd":                spentUSD,
		"cost_per_1k_input_tokens": costPer1kInputTokens,
		"max_tokens_per_request":   maxTokensPerRequest,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return TokenBudgetResult{}, fmt.Errorf("marshal input: %w", err)
	}

	cInput := C.CString(string(inputJSON))
	defer C.free(unsafe.Pointer(cInput))

	resultPtr := C.ffi_token_budget_validate(cInput)
	if resultPtr == nil {
		return TokenBudgetResult{}, fmt.Errorf("ffi_token_budget_validate returned NULL")
	}
	defer C.ffi_free_string(resultPtr)

	resultJSON := C.GoString(resultPtr)

	var result TokenBudgetResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return TokenBudgetResult{}, fmt.Errorf("unmarshal FFI result: %w", err)
	}

	return result, nil
}
