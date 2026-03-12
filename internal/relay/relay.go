package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"dnn-node/internal/bitcoin"
	"dnn-node/internal/config"
	"dnn-node/internal/database"
	"dnn-node/internal/policy"
	"dnn-node/internal/validation"
)

// Relay handles Nostr relay functionality
type Relay struct {
	config    *config.Config
	db        *database.Database
	policy    *policy.PolicyEnforcer
	validator *validation.Validator

	startTime time.Time

	mu            sync.RWMutex
	subscriptions map[string]*Subscription

	// OnEventStored is called after a valid event is stored.
	// The syncer hooks into this to rebroadcast events to other relays.
	OnEventStored func(*nostr.Event)
}

// Subscription represents a client subscription
type Subscription struct {
	ID      string
	Filters []nostr.Filter
	Conn    *websocket.Conn
}

// New creates a new relay
func New(cfg *config.Config, db *database.Database) (*Relay, error) {
	return &Relay{
		config:    cfg,
		db:        db,
		policy:    policy.NewPolicyEnforcer(cfg.Network), // Use network from config
		validator: validation.NewValidator(),

		startTime:     time.Now(),
		subscriptions: make(map[string]*Subscription),
	}, nil
}

// Start starts the relay
func (r *Relay) Start() {
	log.Println("Relay started")
}

// Stop stops the relay
func (r *Relay) Stop() {
	log.Println("Relay stopped")
}

// StartTime returns when the relay was started
func (r *Relay) StartTime() time.Time {
	return r.startTime
}

// HandleConnection handles a WebSocket connection
func (r *Relay) HandleConnection(conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Read messages
	for {
		select {
		case <-ctx.Done():
			return
		default:
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket error: %v", err)
				}
				return
			}

			if messageType != websocket.TextMessage {
				continue
			}

			// Parse Nostr message
			var msg []json.RawMessage
			if err := json.Unmarshal(message, &msg); err != nil {
				r.sendError(conn, "NOTICE", "Invalid message format")
				continue
			}

			if len(msg) < 1 {
				r.sendError(conn, "NOTICE", "Empty message")
				continue
			}

			var msgType string
			if err := json.Unmarshal(msg[0], &msgType); err != nil {
				r.sendError(conn, "NOTICE", "Invalid message type")
				continue
			}

			switch msgType {
			case "EVENT":
				r.handleEvent(conn, msg)
			case "REQ":
				r.handleRequest(conn, msg)
			case "CLOSE":
				r.handleClose(conn, msg)
			default:
				r.sendError(conn, "NOTICE", fmt.Sprintf("Unknown message type: %s", msgType))
			}
		}
	}
}

// handleEvent handles EVENT messages
func (r *Relay) handleEvent(conn *websocket.Conn, msg []json.RawMessage) {
	if len(msg) < 2 {
		r.sendError(conn, "NOTICE", "EVENT message missing event data")
		return
	}

	var event nostr.Event
	if err := json.Unmarshal(msg[1], &event); err != nil {
		r.sendError(conn, "NOTICE", "Invalid event format")
		return
	}

	log.Printf("[RELAY] Received EVENT kind=%d, id=%s, pubkey=%s", event.Kind, event.ID[:8], event.PubKey[:8])

	// Verify event signature
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		log.Printf("[RELAY REJECT] Signature verification failed for event %s: %v", event.ID[:8], err)
		r.sendOK(conn, event.ID, false, "invalid: signature verification failed")
		return
	}
	log.Printf("[RELAY] Signature verified for event %s", event.ID[:8])

	// Check if this is a DNN event
	isDNN := false
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "DNN" {
			isDNN = true
			break
		}
	}

	if !isDNN {
		log.Printf("[RELAY REJECT] Event %s missing DNN tag", event.ID[:8])
		r.sendOK(conn, event.ID, false, "invalid: not a DNN event")
		return
	}
	log.Printf("[RELAY] DNN tag verified for event %s", event.ID[:8])

	// Validate event size according to policy
	if err := r.policy.ValidateEventSize(&event); err != nil {
		log.Printf("[RELAY REJECT] Size validation failed for event %s: %v", event.ID[:8], err)
		r.sendOK(conn, event.ID, false, fmt.Sprintf("invalid: %v", err))
		return
	}
	log.Printf("[RELAY] Size validated for event %s", event.ID[:8])

	// Validate event structure based on kind
	if err := r.validator.ValidateDNNEvent(&event); err != nil {
		log.Printf("[RELAY REJECT] Structure validation failed for event %s: %v", event.ID[:8], err)
		r.sendOK(conn, event.ID, false, fmt.Sprintf("invalid: %v", err))
		return
	}
	log.Printf("[RELAY] Structure validated for event %s", event.ID[:8])

	// Additional validation for metadata content
	if event.Kind == 63600 {
		if err := r.policy.ValidateMetadataContent(&event); err != nil {
			log.Printf("[RELAY REJECT] Metadata content validation failed for event %s: %v", event.ID[:8], err)
			r.sendOK(conn, event.ID, false, fmt.Sprintf("invalid: %v", err))
			return
		}
		log.Printf("[RELAY] Metadata content validated for event %s", event.ID[:8])
	}

	// Additional validation for anchor events
	if event.Kind == 60600 {
		if err := r.policy.ValidateTagReferences(&event); err != nil {
			log.Printf("[RELAY REJECT] Tag references validation failed for event %s: %v", event.ID[:8], err)
			r.sendOK(conn, event.ID, false, fmt.Sprintf("invalid: %v", err))
			return
		}
		log.Printf("[RELAY] Tag references validated for event %s", event.ID[:8])
	}

	// For kinds 61600, 62600, 63600: validate that author has a valid Bitcoin transaction
	if event.Kind == 61600 || event.Kind == 62600 || event.Kind == 63600 {
		// Derive Bitcoin addresses from author's npub
		isTestnet := r.config.Network == "testnet" || r.config.Network == "dev"
		deriver := bitcoin.NewAddressDeriver(isTestnet)
		derivedAddrs, err := deriver.DeriveAddresses(event.PubKey)
		if err != nil {
			log.Printf("[RELAY REJECT] Failed to derive addresses for %s: %v", event.PubKey[:8], err)
			r.sendOK(conn, event.ID, false, "invalid: failed to derive bitcoin address")
			return
		}

		// Extract address strings for lookup
		var addressStrings []string
		for _, addr := range derivedAddrs {
			addressStrings = append(addressStrings, addr.Address)
		}

		// Check if any derived address has a valid transaction
		hasValidTx, err := r.db.HasValidTransactionForAddresses(addressStrings)
		if err != nil {
			log.Printf("[RELAY] Error checking transactions for %s: %v", event.PubKey[:8], err)
			r.sendOK(conn, event.ID, false, "error: failed to validate transaction")
			return
		}

		if !hasValidTx {
			log.Printf("[RELAY REJECT] No valid transaction for author %s - cannot publish DNN events", event.PubKey[:8])
			r.sendOK(conn, event.ID, false, "invalid: author has no valid Bitcoin self-transfer transaction")
			return
		}
		log.Printf("[RELAY] Author %s has valid transaction, accepting event", event.PubKey[:8])
	}

	// Store event based on kind
	var storeErr error
	switch event.Kind {
	case 61600: // Name event
		log.Printf("[RELAY DEBUG] Storing Name event %s from %s", event.ID[:8], event.PubKey[:8])
		storeErr = r.db.StoreNameEvent(&event)
	case 62600: // Connection event
		log.Printf("[RELAY DEBUG] Storing Connection event %s from %s", event.ID[:8], event.PubKey[:8])
		storeErr = r.db.StoreConnectionEvent(&event)
	case 63600: // Metadata event
		log.Printf("[RELAY DEBUG] Storing Metadata event %s from %s", event.ID[:8], event.PubKey[:8])
		storeErr = r.db.StoreMetadataEvent(&event)
	case 60600: // Anchor event
		log.Printf("[RELAY DEBUG] Storing Anchor event %s from %s", event.ID[:8], event.PubKey[:8])
		// For anchor events, we need to validate the Bitcoin transaction
		// and determine the block number and position
		// This is a simplified version - full implementation would verify with Bitcoin node
		storeErr = r.handleAnchorEvent(&event)
	default:
		r.sendOK(conn, event.ID, false, fmt.Sprintf("invalid: unsupported event kind %d", event.Kind))
		return
	}

	if storeErr != nil {
		log.Printf("[RELAY ERROR] Failed to store event %s (kind %d): %v", event.ID[:8], event.Kind, storeErr)
		r.sendOK(conn, event.ID, false, fmt.Sprintf("error: %v", storeErr))
		return
	}

	log.Printf("[RELAY SUCCESS] Stored event %s (kind %d)", event.ID[:8], event.Kind)

	r.sendOK(conn, event.ID, true, "")

	// Broadcast event to all subscribers
	r.broadcastEvent(&event)

	// Trigger rebroadcast to other relays via syncer callback
	if r.OnEventStored != nil {
		r.OnEventStored(&event)
	}
}

// handleAnchorEvent processes and validates anchor events
func (r *Relay) handleAnchorEvent(event *nostr.Event) error {
	// Extract transaction ID (tag 'x' = transaction)
	var txID string
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "x" {
			txID = tag[1]
			break
		}
	}

	if txID == "" {
		return fmt.Errorf("anchor event missing x tag (transaction)")
	}

	log.Printf("========================================")
	log.Printf("🎯 REACTIVE ANCHOR MATCHING TRIGGERED")
	log.Printf("========================================")
	log.Printf("[RELAY] Anchor event received for transaction %s", txID)
	log.Printf("[RELAY] Checking if transaction exists in database...")

	// Look up the transaction in the database
	tx, err := r.db.GetBitcoinTransaction(txID)
	if err != nil {
		log.Printf("[RELAY ERROR] Database error looking up transaction %s: %v", txID[:16], err)
		return fmt.Errorf("database error: %v", err)
	}

	if tx == nil {
		// Transaction not found in database
		log.Printf("[RELAY] Transaction %s not found in database yet - anchor will be stored when transaction is discovered", txID[:16])
		return fmt.Errorf("transaction not yet in database (will be stored when transaction is discovered)")
	}

	log.Printf("[RELAY] ✓ Found transaction %s at Bitcoin block %d, DNN block %d, position %d",
		txID[:16], tx.BitcoinBlock, tx.DNNBlock, tx.Position)

	// Store the anchor event with the correct block and position data
	err = r.db.StoreAnchorEvent(event, tx.BitcoinBlock, tx.DNNBlock, tx.Position)
	if err != nil {
		log.Printf("[RELAY ERROR] Failed to store anchor event: %v", err)
		return err
	}

	// Fetch and store referenced events (name, connection, metadata)
	log.Printf("[RELAY] Fetching referenced events for anchor %s...", event.ID[:8])
	if err := r.fetchAndStoreReferencedEvents(event); err != nil {
		log.Printf("[RELAY] Failed to fetch referenced events: %v", err)
	}

	log.Printf("[RELAY SUCCESS] ✓ Anchor event stored and linked to transaction %s", txID[:16])
	log.Printf("========================================")
	log.Printf("✅ REACTIVE MATCHING COMPLETE")
	log.Printf("========================================")
	return nil
}

// handleRequest handles REQ messages
func (r *Relay) handleRequest(conn *websocket.Conn, msg []json.RawMessage) {
	if len(msg) < 3 {
		r.sendError(conn, "NOTICE", "REQ message missing subscription ID or filters")
		return
	}

	var subID string
	if err := json.Unmarshal(msg[1], &subID); err != nil {
		r.sendError(conn, "NOTICE", "Invalid subscription ID")
		return
	}

	// Parse filters
	var filters []nostr.Filter
	for i := 2; i < len(msg); i++ {
		var filter nostr.Filter
		if err := json.Unmarshal(msg[i], &filter); err != nil {
			r.sendError(conn, "NOTICE", "Invalid filter format")
			return
		}
		filters = append(filters, filter)
	}

	// Store subscription
	r.mu.Lock()
	r.subscriptions[subID] = &Subscription{
		ID:      subID,
		Filters: filters,
		Conn:    conn,
	}
	r.mu.Unlock()

	// Query and send matching events from database
	for _, filter := range filters {
		events, err := r.db.QueryEvents(filter)
		if err != nil {
			log.Printf("Failed to query events: %v", err)
			continue
		}

		log.Printf("[RELAY] Sending %d events to client for subscription %s", len(events), subID)
		sentCount := 0
		// Send each matching event to the client (with awareness filtering)
		for _, event := range events {
			// Apply awareness filtering for kind 62600 and 63600
			if r.config.EnableAwareness && (event.Kind == 62600 || event.Kind == 63600) {
				if r.isEventBlocked(event) {
					log.Printf("[RELAY] Filtered blocked event %s (kind %d)", event.ID[:8], event.Kind)
					continue // Skip sending this event
				}
			}
			r.sendEvent(conn, subID, event)
			sentCount++
		}
		log.Printf("[RELAY] Sent %d events to client (after filtering)", sentCount)
	}

	// Send EOSE (End of Stored Events)
	r.sendEOSE(conn, subID)
}

// handleClose handles CLOSE messages
func (r *Relay) handleClose(conn *websocket.Conn, msg []json.RawMessage) {
	if len(msg) < 2 {
		r.sendError(conn, "NOTICE", "CLOSE message missing subscription ID")
		return
	}

	var subID string
	if err := json.Unmarshal(msg[1], &subID); err != nil {
		r.sendError(conn, "NOTICE", "Invalid subscription ID")
		return
	}

	r.mu.Lock()
	delete(r.subscriptions, subID)
	r.mu.Unlock()
}

// broadcastEvent broadcasts an event to all matching subscriptions
func (r *Relay) broadcastEvent(event *nostr.Event) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, sub := range r.subscriptions {
		// Check if event matches any filter
		for _, filter := range sub.Filters {
			if r.eventMatchesFilter(event, filter) {
				r.sendEvent(sub.Conn, sub.ID, event)
				break
			}
		}
	}
}

// eventMatchesFilter checks if an event matches a filter
func (r *Relay) eventMatchesFilter(event *nostr.Event, filter nostr.Filter) bool {
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

	// Check tags
	for tagName, tagValues := range filter.Tags {
		if len(tagValues) == 0 {
			continue
		}

		found := false
		for _, eventTag := range event.Tags {
			if len(eventTag) >= 2 && eventTag[0] == tagName {
				for _, filterValue := range tagValues {
					if eventTag[1] == filterValue {
						found = true
						break
					}
				}
			}
			if found {
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

// sendError sends an error message
func (r *Relay) sendError(conn *websocket.Conn, msgType string, message string) {
	msg := []interface{}{msgType, message}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// sendOK sends an OK message
func (r *Relay) sendOK(conn *websocket.Conn, eventID string, accepted bool, message string) {
	msg := []interface{}{"OK", eventID, accepted, message}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// sendEvent sends an EVENT message
func (r *Relay) sendEvent(conn *websocket.Conn, subID string, event *nostr.Event) {
	msg := []interface{}{"EVENT", subID, event}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// sendEOSE sends an EOSE message
func (r *Relay) sendEOSE(conn *websocket.Conn, subID string) {
	msg := []interface{}{"EOSE", subID}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// isEventBlocked checks if an event belongs to a blocked DNN ID
// It looks up the anchor that references this event to find the DNN block/position
func (r *Relay) isEventBlocked(event *nostr.Event) bool {
	// Find which anchor references this event by looking up the pubkey in anchor references
	var dnnBlock int64
	var position int

	// Query anchor_events to find which DNN ID this event belongs to via pubkey match
	rows, err := r.db.RawQuery(`
		SELECT dnn_block_number, position FROM anchor_events
		WHERE connection_event_ref LIKE '%' || ? || '%'
		   OR metadata_event_ref LIKE '%' || ? || '%'
		LIMIT 1
	`, event.PubKey, event.PubKey)
	if err != nil {
		return false
	}
	defer rows.Close()

	if rows.Next() {
		if err := rows.Scan(&dnnBlock, &position); err == nil && dnnBlock > 0 {
			// Check if this DNN ID is blocked
			isBlocked, err := r.db.IsBlocked(dnnBlock, position, "")
			if err == nil && isBlocked {
				return true
			}
		}
	}

	return false
}

// fetchAndStoreReferencedEvents fetches name, connection, and metadata events referenced by an anchor event
func (r *Relay) fetchAndStoreReferencedEvents(anchorEvent *nostr.Event) error {
	// Extract naddr references from anchor event tags
	// Single-character tags: n=names, c=connection, m=metadata
	var nameRef, connectionRef, metadataRef string

	for _, tag := range anchorEvent.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "n":
			nameRef = tag[1]
		case "c":
			connectionRef = tag[1]
		case "m":
			metadataRef = tag[1]
		}
	}

	// Helper function to decode naddr and fetch event
	fetchEventFromNaddr := func(naddr string, expectedKind int) *nostr.Event {
		if naddr == "" || !strings.HasPrefix(naddr, "naddr1") {
			return nil
		}

		// Decode the naddr
		prefix, data, err := nip19.Decode(naddr)
		if err != nil {
			log.Printf("[RELAY] Failed to decode naddr: %v", err)
			return nil
		}

		if prefix != "naddr" {
			log.Printf("[RELAY] Expected naddr prefix, got %s", prefix)
			return nil
		}

		// Extract data through JSON marshaling
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("[RELAY] Failed to marshal naddr data: %v", err)
			return nil
		}

		type naddrData struct {
			Kind       int      `json:"kind"`
			Pubkey     string   `json:"pubkey"`
			Identifier string   `json:"identifier"`
			Relays     []string `json:"relays"`
		}

		var decoded naddrData
		if err := json.Unmarshal(jsonData, &decoded); err != nil {
			log.Printf("[RELAY] Failed to unmarshal naddr data: %v", err)
			return nil
		}

		// Fetch using extracted data
		return r.fetchEventByDTag(decoded.Identifier, decoded.Pubkey, decoded.Kind)
	}

	// Fetch referenced events
	nameEvent := fetchEventFromNaddr(nameRef, 61600)
	connectionEvent := fetchEventFromNaddr(connectionRef, 62600)
	metadataEvent := fetchEventFromNaddr(metadataRef, 63600)

	// Store events if found
	if nameEvent != nil {
		if err := r.db.StoreNameEvent(nameEvent); err != nil {
			log.Printf("[RELAY] Failed to store name event: %v", err)
		} else {
			log.Printf("[RELAY] ✓ Stored name event %s", nameEvent.ID[:8])
		}
	}

	if connectionEvent != nil {
		if err := r.db.StoreConnectionEvent(connectionEvent); err != nil {
			log.Printf("[RELAY] Failed to store connection event: %v", err)
		} else {
			log.Printf("[RELAY] ✓ Stored connection event %s", connectionEvent.ID[:8])
		}
	}

	if metadataEvent != nil {
		if err := r.db.StoreMetadataEvent(metadataEvent); err != nil {
			log.Printf("[RELAY] Failed to store metadata event: %v", err)
		} else {
			log.Printf("[RELAY] ✓ Stored metadata event %s", metadataEvent.ID[:8])
		}
	}

	return nil
}

// fetchEventByDTag fetches an event by its d-tag from relays
func (r *Relay) fetchEventByDTag(dTag string, pubkey string, kind int) *nostr.Event {
	// Try local DB first
	events, err := r.db.QueryEvents(nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{kind},
		Tags:    nostr.TagMap{"d": []string{dTag}},
		Limit:   1,
	})
	if err == nil && len(events) > 0 {
		return events[0]
	}

	// If not in DB, try fetching from configured relays IN PARALLEL
	dTagDisplay := dTag
	if len(dTagDisplay) > 16 {
		dTagDisplay = dTagDisplay[:16]
	}
	log.Printf("[RELAY] Event with d-tag %s (kind %d) not in local DB, querying %d relays in parallel...", dTagDisplay, kind, len(r.config.RelayURLs))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		event *nostr.Event
		relay string
	}

	resultChan := make(chan result, len(r.config.RelayURLs))

	// Query all relays in parallel
	for _, relayURL := range r.config.RelayURLs {
		go func(url string) {
			relay := nostr.NewRelay(ctx, url)
			if err := relay.Connect(ctx); err != nil {
				resultChan <- result{nil, url}
				return
			}

			filter := nostr.Filter{
				Authors: []string{pubkey},
				Kinds:   []int{kind},
				Tags:    nostr.TagMap{"d": []string{dTag}},
				Limit:   1,
			}

			events, err := relay.QuerySync(ctx, filter)
			relay.Close()

			if err == nil && len(events) > 0 {
				resultChan <- result{events[0], url}
			} else {
				resultChan <- result{nil, url}
			}
		}(relayURL)
	}

	// Wait for first successful result or all to complete
	for i := 0; i < len(r.config.RelayURLs); i++ {
		select {
		case res := <-resultChan:
			if res.event != nil {
				log.Printf("[RELAY] Found event with d-tag %s on relay %s", dTagDisplay, res.relay)
				return res.event
			}
		case <-ctx.Done():
			log.Printf("[RELAY] Timeout querying relays for event with d-tag %s", dTagDisplay)
			return nil
		}
	}

	log.Printf("[RELAY] Event with d-tag %s (kind %d) not found in DB or relays", dTagDisplay, kind)
	return nil
}
