package omniroute

import (
	"strings"
	"testing"
)

func TestHello(t *testing.T) {
	msg, err := Hello()
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if msg != "Hello from Rust FFI" {
		t.Errorf("got %q, want 'Hello from Rust FFI'", msg)
	}
}

func TestValidateTokenBudget_Fits(t *testing.T) {
	result, err := ValidateTokenBudget("hello world", 10.0, 0.0, 0.002, 100)
	if err != nil {
		t.Fatalf("ValidateTokenBudget: %v", err)
	}
	if !result.Fits {
		t.Errorf("expected Fits=true, got false (fits_budget=%v, fits_token_limit=%v)",
			result.FitsBudget, result.FitsTokenLimit)
	}
	if result.EstimatedTokens == 0 {
		t.Error("expected non-zero EstimatedTokens")
	}
}

func TestValidateTokenBudget_ExceedsTokenLimit(t *testing.T) {
	longPrompt := strings.Repeat("x", 1000) // ~250 tokens
	result, err := ValidateTokenBudget(longPrompt, 10.0, 0.0, 0.002, 100)
	if err != nil {
		t.Fatalf("ValidateTokenBudget: %v", err)
	}
	if result.Fits {
		t.Error("expected Fits=false for prompt exceeding token limit")
	}
	if result.FitsTokenLimit {
		t.Error("expected FitsTokenLimit=false")
	}
}

func TestValidateTokenBudget_InvalidInput(t *testing.T) {
	// Negative daily_limit_usd is invalid input (BudgetError)
	result, err := ValidateTokenBudget("prompt", -1.0, 0.0, 0.002, 100)
	if err != nil {
		t.Fatalf("ValidateTokenBudget: %v", err)
	}
	if result.Fits {
		t.Error("expected Fits=false for invalid input")
	}
	if result.Error == "" {
		t.Error("expected non-empty Error field for invalid input")
	}
}
