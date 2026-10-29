// ws.go — WebSocket hub for real-time dashboard updates.
// Broadcasts pipeline events to all connected clients.
//
// Architecture:
//   - Each client gets its own context (NOT derived from the HTTP request
//     context, which is canceled as soon as HandleUpgrade returns).
//   - Two goroutines per client: readPump and writePump.
//   - Both pumps share the client's context; either pump failing
//     cancels the context and tears down the other.
//   - Unregistration is coordinated via sync.Once to avoid races.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

// WSEvent is a message broadcast to all connected clients.
type WSEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload,omitempty"`
}

// wsClient represents a single WebSocket connection.
type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	ctx    context.Context
	cancel context.CancelFunc

	// Ensures unregistration and close happen exactly once,
	// regardless of which pump fails first.
	unregOnce sync.Once
	closeOnce sync.Once
}

// close tears down the client's connection. Safe to call multiple times.
func (c *wsClient) close(reason string) {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.conn.Close(websocket.StatusNormalClosure, reason)
	})
}

// unregister signals the hub to remove this client. Safe to call
// multiple times; only the first call has effect.
func (c *wsClient) unregister(hub *WSHub) {
	c.unregOnce.Do(func() {
		hub.unregister <- c
	})
}

// WSHub manages WebSocket connections and broadcasts.
type WSHub struct {
	clients    map[*wsClient]bool
	broadcast  chan WSEvent
	register   chan *wsClient
	unregister chan *wsClient
	mu         sync.RWMutex
}

// NewWSHub creates a new hub. Start it with Start().
func NewWSHub() *WSHub {
	return &WSHub{
		clients:    make(map[*wsClient]bool),
		broadcast:  make(chan WSEvent, 256),
		register:   make(chan *wsClient),
		unregister: make(chan *wsClient),
	}
}

// Start runs the hub's event loop. Blocks until ctx is canceled.
func (h *WSHub) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.shutdown()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("ws: client connected, total=%d", count)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			count := len(h.clients)
			h.mu.Unlock()
			log.Printf("ws: client disconnected, total=%d", count)

		case event := <-h.broadcast:
			h.fanout(event)
		}
	}
}

// Broadcast sends an event to all connected clients. Non-blocking:
// if the broadcast queue is full, the event is dropped.
func (h *WSHub) Broadcast(event WSEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	select {
	case h.broadcast <- event:
	default:
		log.Printf("ws: broadcast queue full, dropping event type=%s", event.Type)
	}
}

// BroadcastEvent is a convenience method that constructs a WSEvent from
// primitive types and broadcasts it. Used by scheduler to emit lifecycle
// events without importing the api package.
func (h *WSHub) BroadcastEvent(eventType string, payload map[string]interface{}) {
	h.Broadcast(WSEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Payload:   payload,
	})
}

// ClientCount returns the current number of connected clients.
func (h *WSHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// HandleUpgrade upgrades an HTTP request to a WebSocket connection.
// IMPORTANT: each client gets its OWN context (not r.Context()), because
// the HTTP request context is canceled as soon as HandleUpgrade returns,
// which would immediately tear down the WebSocket pumps.
func (h *WSHub) HandleUpgrade(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("ws: upgrade failed: %v", err)
		return
	}

	// Independent context for this client. Canceled only when the
	// connection closes (either side).
	clientCtx, cancel := context.WithCancel(context.Background())

	client := &wsClient{
		conn:   conn,
		send:   make(chan []byte, 64),
		ctx:    clientCtx,
		cancel: cancel,
	}

	// Register first, then start pumps. If registration somehow
	// fails (hub shut down), cancel the client immediately.
	select {
	case h.register <- client:
		// ok
	default:
		cancel()
		_ = conn.Close(websocket.StatusGoingAway, "hub busy")
		return
	}

	// Start pumps. Either pump failing tears down the other via
	// client.close() → cancel → both pumps see ctx.Done().
	go h.writePump(client)
	go h.readPump(client)
}

// writePump sends queued messages to the client.
func (h *WSHub) writePump(client *wsClient) {
	defer func() {
		client.unregister(h)
		client.close("write pump done")
	}()

	for {
		select {
		case <-client.ctx.Done():
			return
		case msg, ok := <-client.send:
			if !ok {
				// Channel closed by hub (unregister path).
				return
			}
			writeCtx, cancel := context.WithTimeout(client.ctx, 10*time.Second)
			err := client.conn.Write(writeCtx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				log.Printf("ws: write failed: %v", err)
				return
			}
		}
	}
}

// readPump reads messages from the client. For now we don't process
// client→server messages, but we need this pump to detect client
// disconnection (close frame, network error, timeout).
func (h *WSHub) readPump(client *wsClient) {
	defer func() {
		client.unregister(h)
		client.close("read pump done")
	}()

	client.conn.SetReadLimit(4096)

	// Spawn a goroutine to do blocking reads so we can respect
	// context cancellation (which the blocking Read API does not).
	type readResult struct {
		err error
	}
	resultCh := make(chan readResult, 1)

	go func() {
		for {
			// 60s idle timeout: if the client sends nothing for 60s,
			// we consider it dead. Real production code would use
			// ping/pong frames here.
			readCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			_, _, err := client.conn.Read(readCtx)
			cancel()
			if err != nil {
				resultCh <- readResult{err: err}
				return
			}
			// Message received but ignored in this increment.
			// Future increments may handle client→server commands here.
		}
	}()

	select {
	case <-client.ctx.Done():
		return
	case res := <-resultCh:
		err := res.err
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("ws: client read timeout")
		}
		return
	}
}

// fanout sends an event to all connected clients.
func (h *WSHub) fanout(event WSEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("ws: marshal failed: %v", err)
		return
	}

	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- data:
		default:
			log.Printf("ws: slow client, disconnecting")
			c.close("slow client")
		}
	}
}

// shutdown closes all active client connections.
func (h *WSHub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for client := range h.clients {
		client.close("server shutting down")
		close(client.send)
		delete(h.clients, client)
	}
	log.Printf("ws: hub shut down, all clients disconnected")
}

// handleWS is an HTTP handler that upgrades to WebSocket.
// It wraps WSHub.HandleUpgrade to match chi's handler signature.
func handleWS(hub *WSHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hub.HandleUpgrade(w, r)
	}
}
