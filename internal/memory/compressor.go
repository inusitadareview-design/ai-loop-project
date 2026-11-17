// compressor.go wraps the Rust memory-compressor via FFI.
// Go converts []Event to JSON, calls Rust, parses response, and frees memory.
//
// Guardrails enforced:
// - I5: Rust is the ONLY entity that produces compressed snapshots
// - G3.1: Compression creates new files, never deletes originals
// - G5.3: Go copies/frees memory immediately after FFI call
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"unsafe"
)

/*
#cgo LDFLAGS: -L${SRCDIR}/../../pkg/ffi -lailoop_ffi
#include <stdlib.h>

extern char* ffi_memory_compress(const char* json_events);
extern void ffi_free_string(char* ptr);
*/
import "C"

// CompressionResult holds the output of a memory compression.
type CompressionResult struct {
	Snapshot       string  `json:"snapshot"`
	EventCount     int     `json:"event_count"`
	OriginalSize   int     `json:"original_size"`
	CompressedSize int     `json:"compressed_size"`
	ReductionPct   float64 `json:"reduction_pct"`
}

// CompressEvents compresses a batch of memory events into a snapshot.
// This function:
// 1. Converts []Event to JSON
// 2. Calls Rust FFI (ffi_memory_compress)
// 3. Parses the JSON response
// 4. Frees the Rust-allocated memory immediately
//
// Returns an error if:
// - Events slice is empty
// - JSON serialization fails
// - FFI returns NULL (Rust error: empty/duplicate/parse failure)
// - Response JSON is invalid
func CompressEvents(ctx context.Context, events []Event) (*CompressionResult, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("compress: events slice is empty")
	}

	// Check context before expensive operation
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("compress: %w", err)
	}

	// Convert Go events to Rust-compatible JSON format
	rustEvents := make([]rustEvent, len(events))
	for i, ev := range events {
		rustEvents[i] = rustEvent{
			ID:            ev.ID,
			Content:       eventContent(ev),
			TimestampSecs: ev.Timestamp,
		}
	}

	jsonBytes, err := json.Marshal(rustEvents)
	if err != nil {
		return nil, fmt.Errorf("compress: marshal events: %w", err)
	}

	// Call Rust FFI
	cJSON := C.CString(string(jsonBytes))
	defer C.free(unsafe.Pointer(cJSON))

	resultPtr := C.ffi_memory_compress(cJSON)
	if resultPtr == nil {
		return nil, fmt.Errorf("compress: FFI returned NULL (Rust error: empty/duplicate/parse failure)")
	}

	// Copy the result string immediately (G5.3: don't hold Rust pointers)
	resultStr := C.GoString(resultPtr)

	// Free the Rust-allocated memory immediately
	C.ffi_free_string(resultPtr)

	// Parse the JSON response
	var result CompressionResult
	if err := json.Unmarshal([]byte(resultStr), &result); err != nil {
		return nil, fmt.Errorf("compress: unmarshal response: %w", err)
	}

	return &result, nil
}

// rustEvent is the JSON format expected by Rust's memory-compressor.
type rustEvent struct {
	ID            string `json:"id"`
	Content       string `json:"content"`
	TimestampSecs int64  `json:"timestamp_secs"`
}

// eventContent extracts the content from an Event.
// For now, it serializes the Data field as JSON.
// In the future, this could be smarter (e.g., extract specific fields).
func eventContent(ev Event) string {
	if ev.Data == nil {
		return ""
	}
	bytes, err := json.Marshal(ev.Data)
	if err != nil {
		return ""
	}
	return string(bytes)
}
