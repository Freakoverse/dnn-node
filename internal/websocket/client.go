package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
)

// Client represents a WebSocket client connection
type Client struct {
	ID            string
	Conn          *websocket.Conn
	Send          chan []byte
	Subscriptions map[string]*Subscription
	mu            sync.RWMutex
	hub           *Hub
	pingTicker    *time.Ticker
}

// Subscription represents a client's subscription to events
type Subscription struct {
	ID      string
	Filters []nostr.Filter
	Client  *Client
}

// Hub manages all WebSocket clients
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// NewHub creates a new WebSocket hub
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's main loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Printf("Client %s connected", client.ID)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)
				h.mu.Unlock()
				log.Printf("Client %s disconnected", client.ID)
			} else {
				h.mu.Unlock()
			}

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.Send <- message:
				default:
					// Client's send channel is full, close it
					h.mu.RUnlock()
					h.mu.Lock()
					delete(h.clients, client)
					close(client.Send)
					h.mu.Unlock()
					h.mu.RLock()
				}
			}
			h.mu.RUnlock()
		}
	}
}

// BroadcastEvent broadcasts an event to all connected clients
func (h *Hub) BroadcastEvent(event *nostr.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		// Check if event matches any of the client's subscriptions
		for _, sub := range client.Subscriptions {
			for _, filter := range sub.Filters {
				if matchesFilter(event, filter) {
					message := []interface{}{"EVENT", sub.ID, event}
					data, _ := json.Marshal(message)
					select {
					case client.Send <- data:
					default:
						// Channel full, skip
					}
					break
				}
			}
		}
	}
}

// GetClientCount returns the number of connected clients
func (h *Hub) GetClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// NewClient creates a new WebSocket client
func NewClient(conn *websocket.Conn, hub *Hub) *Client {
	return &Client{
		ID:            generateClientID(),
		Conn:          conn,
		Send:          make(chan []byte, 256),
		Subscriptions: make(map[string]*Subscription),
		hub:           hub,
		pingTicker:    time.NewTicker(30 * time.Second),
	}
}

// ReadPump pumps messages from the WebSocket connection to the hub
func (c *Client) ReadPump(ctx context.Context) {
	defer func() {
		c.pingTicker.Stop()
		c.hub.unregister <- c
		c.Conn.Close()
	}()

	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := c.Conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket error: %v", err)
				}
				return
			}

			// Parse and handle the message
			if err := c.handleMessage(message); err != nil {
				c.sendError("NOTICE", err.Error())
			}
		}
	}
}

// WritePump pumps messages from the hub to the WebSocket connection
func (c *Client) WritePump(ctx context.Context) {
	defer func() {
		c.Conn.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			c.Conn.WriteMessage(websocket.TextMessage, message)

		case <-c.pingTicker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage handles incoming WebSocket messages
func (c *Client) handleMessage(message []byte) error {
	var msg []json.RawMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		return fmt.Errorf("invalid message format")
	}

	if len(msg) < 1 {
		return fmt.Errorf("empty message")
	}

	var msgType string
	if err := json.Unmarshal(msg[0], &msgType); err != nil {
		return fmt.Errorf("invalid message type")
	}

	switch msgType {
	case "REQ":
		return c.handleReq(msg)
	case "CLOSE":
		return c.handleClose(msg)
	case "EVENT":
		return c.handleEvent(msg)
	default:
		return fmt.Errorf("unknown message type: %s", msgType)
	}
}

// handleReq handles REQ messages
func (c *Client) handleReq(msg []json.RawMessage) error {
	if len(msg) < 3 {
		return fmt.Errorf("REQ message missing subscription ID or filters")
	}

	var subID string
	if err := json.Unmarshal(msg[1], &subID); err != nil {
		return fmt.Errorf("invalid subscription ID")
	}

	// Parse filters
	var filters []nostr.Filter
	for i := 2; i < len(msg); i++ {
		var filter nostr.Filter
		if err := json.Unmarshal(msg[i], &filter); err != nil {
			return fmt.Errorf("invalid filter format")
		}
		filters = append(filters, filter)
	}

	// Store subscription
	c.mu.Lock()
	c.Subscriptions[subID] = &Subscription{
		ID:      subID,
		Filters: filters,
		Client:  c,
	}
	c.mu.Unlock()

	// Send EOSE (End of Stored Events)
	c.sendEOSE(subID)

	return nil
}

// handleClose handles CLOSE messages
func (c *Client) handleClose(msg []json.RawMessage) error {
	if len(msg) < 2 {
		return fmt.Errorf("CLOSE message missing subscription ID")
	}

	var subID string
	if err := json.Unmarshal(msg[1], &subID); err != nil {
		return fmt.Errorf("invalid subscription ID")
	}

	c.mu.Lock()
	delete(c.Subscriptions, subID)
	c.mu.Unlock()

	return nil
}

// handleEvent handles EVENT messages
func (c *Client) handleEvent(msg []json.RawMessage) error {
	if len(msg) < 2 {
		return fmt.Errorf("EVENT message missing event data")
	}

	var event nostr.Event
	if err := json.Unmarshal(msg[1], &event); err != nil {
		return fmt.Errorf("invalid event format")
	}

	// Verify signature
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		c.sendOK(event.ID, false, "invalid: signature verification failed")
		return nil
	}

	// Process the event (this would typically store it and broadcast to subscribers)
	// For now, just send OK
	c.sendOK(event.ID, true, "")

	return nil
}

// sendError sends an error message to the client
func (c *Client) sendError(msgType string, message string) {
	msg := []interface{}{msgType, message}
	data, _ := json.Marshal(msg)
	c.Send <- data
}

// sendOK sends an OK message to the client
func (c *Client) sendOK(eventID string, accepted bool, message string) {
	msg := []interface{}{"OK", eventID, accepted, message}
	data, _ := json.Marshal(msg)
	c.Send <- data
}

// sendEOSE sends an EOSE message to the client
func (c *Client) sendEOSE(subID string) {
	msg := []interface{}{"EOSE", subID}
	data, _ := json.Marshal(msg)
	c.Send <- data
}

// SendEvent sends an event to the client for a specific subscription
func (c *Client) SendEvent(subID string, event *nostr.Event) {
	msg := []interface{}{"EVENT", subID, event}
	data, _ := json.Marshal(msg)
	c.Send <- data
}

// matchesFilter checks if an event matches a filter
func matchesFilter(event *nostr.Event, filter nostr.Filter) bool {
	// Check event ID
	if len(filter.IDs) > 0 {
		found := false
		for _, id := range filter.IDs {
			if event.ID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check kinds
	if len(filter.Kinds) > 0 {
		found := false
		for _, kind := range filter.Kinds {
			if event.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check authors
	if len(filter.Authors) > 0 {
		found := false
		for _, author := range filter.Authors {
			if event.PubKey == author {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check time constraints
	if filter.Since != nil && event.CreatedAt < *filter.Since {
		return false
	}
	if filter.Until != nil && event.CreatedAt > *filter.Until {
		return false
	}

	return true
}

// generateClientID generates a unique client ID
func generateClientID() string {
	return fmt.Sprintf("client-%d", time.Now().UnixNano())
}