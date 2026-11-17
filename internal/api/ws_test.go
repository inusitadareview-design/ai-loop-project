package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

// startTestHub creates a hub, starts it in a goroutine, and returns
// a cleanup function that cancels the context.
func startTestHub(t *testing.T) (*WSHub, context.Context) {
	t.Helper()
	hub := NewWSHub()
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Start(ctx)
	t.Cleanup(cancel)
	return hub, ctx
}

// connectClient opens a WebSocket client to the test server.
func connectClient(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return conn
}

func TestWSHub_BroadcastToSingleClient(t *testing.T) {
	hub, _ := startTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(handleWS(hub)))
	defer srv.Close()

	conn := connectClient(t, srv)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the hub a moment to register the client.
	time.Sleep(50 * time.Millisecond)

	if got := hub.ClientCount(); got != 1 {
		t.Fatalf("ClientCount = %d, want 1", got)
	}

	// Broadcast an event.
	hub.Broadcast("test_event", map[string]any{})

	// Read the message from the client side.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got WSEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "test_event" {
		t.Errorf("Type = %q, want test_event", got.Type)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp should be auto-set")
	}
}

func TestWSHub_BroadcastToMultipleClients(t *testing.T) {
	hub, _ := startTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(handleWS(hub)))
	defer srv.Close()

	const numClients = 3
	conns := make([]*websocket.Conn, numClients)
	for i := 0; i < numClients; i++ {
		conns[i] = connectClient(t, srv)
		defer conns[i].Close(websocket.StatusNormalClosure, "")
	}

	// Wait for all to register.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if hub.ClientCount() == numClients {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := hub.ClientCount(); got != numClients {
		t.Fatalf("ClientCount = %d, want %d", got, numClients)
	}

	hub.Broadcast("pipeline_started", map[string]any{"name": "test"})

	// Every client should receive the message.
	var wg sync.WaitGroup
	for i, conn := range conns {
		wg.Add(1)
		go func(i int, c *websocket.Conn) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, data, err := c.Read(ctx)
			if err != nil {
				t.Errorf("client %d read: %v", i, err)
				return
			}
			var got WSEvent
			_ = json.Unmarshal(data, &got)
			if got.Type != "pipeline_started" {
				t.Errorf("client %d: Type = %q, want pipeline_started", i, got.Type)
			}
		}(i, conn)
	}
	wg.Wait()
}

func TestWSHub_ClientDisconnectReducesCount(t *testing.T) {
	hub, _ := startTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(handleWS(hub)))
	defer srv.Close()

	conn := connectClient(t, srv)

	time.Sleep(50 * time.Millisecond)
	if got := hub.ClientCount(); got != 1 {
		t.Fatalf("before close: ClientCount = %d, want 1", got)
	}

	// Client closes the connection.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Hub should detect the disconnection within pongTimeout.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ClientCount() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := hub.ClientCount(); got != 0 {
		t.Errorf("after close: ClientCount = %d, want 0", got)
	}
}

func TestWSHub_BroadcastSetsTimestamp(t *testing.T) {
	hub, _ := startTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(handleWS(hub)))
	defer srv.Close()

	conn := connectClient(t, srv)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	before := time.Now()
	hub.Broadcast("draft_created", map[string]any{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got WSEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Timestamp.Before(before) {
		t.Errorf("Timestamp %v is before broadcast call %v", got.Timestamp, before)
	}
}

func TestWSHub_ShutdownClosesClients(t *testing.T) {
	hub, _ := startTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(handleWS(hub)))
	defer srv.Close()

	conn := connectClient(t, srv)
	defer conn.Close(websocket.StatusGoingAway, "")
	time.Sleep(50 * time.Millisecond)

	// Cancel the hub's context to trigger shutdown.
	// We can't cancel ctx directly (it's the one we passed), so we
	// simulate by calling shutdown manually via a new cancel.
	// Re-create scenario: start with own cancel.
	hub2 := NewWSHub()
	ctx2, cancel2 := context.WithCancel(context.Background())
	go hub2.Start(ctx2)

	srv2 := httptest.NewServer(http.HandlerFunc(handleWS(hub2)))
	defer srv2.Close()

	conn2 := connectClient(t, srv2)
	time.Sleep(50 * time.Millisecond)

	if got := hub2.ClientCount(); got != 1 {
		t.Fatalf("before shutdown: ClientCount = %d, want 1", got)
	}

	cancel2()
	time.Sleep(100 * time.Millisecond)

	if got := hub2.ClientCount(); got != 0 {
		t.Errorf("after shutdown: ClientCount = %d, want 0", got)
	}

	// Client side should see closure.
	readCtx, cancelRead := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelRead()
	_, _, err := conn2.Read(readCtx)
	if err == nil {
		t.Error("expected read error after server shutdown")
	}
	_ = conn // silence unused
}

func TestWSHub_NilPayloadAllowed(t *testing.T) {
	hub, _ := startTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(handleWS(hub)))
	defer srv.Close()

	conn := connectClient(t, srv)
	defer conn.Close(websocket.StatusNormalClosure, "")
	time.Sleep(50 * time.Millisecond)

	// Pass nil payload so omitempty actually omits it from JSON.
	hub.Broadcast("shutdown_test", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "shutdown_test" {
		t.Errorf("type = %v, want shutdown_test", got["type"])
	}
	// Payload should be absent (omitempty works on nil, not empty map).
	if _, hasPayload := got["payload"]; hasPayload {
		t.Errorf("payload should be omitted when nil, got %v", got["payload"])
	}
}
