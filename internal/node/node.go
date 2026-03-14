package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	"github.com/nbd-wtf/go-nostr/nip19"

	"dnn-node/internal/bitcoin"
	"dnn-node/internal/config"
	"dnn-node/internal/constants"
	"dnn-node/internal/database"
	"dnn-node/internal/encoder"
	"dnn-node/internal/node/dashboard"
	"dnn-node/internal/policy"
	"dnn-node/internal/relay"
	dnnSync "dnn-node/internal/sync"
)

// Node represents a DNN node
type Node struct {
	config       *config.Config
	db           *database.Database
	relay        *relay.Relay
	syncer       *dnnSync.Syncer
	encoder      *encoder.Encoder
	queryBuilder *database.QueryBuilder
	policy       *policy.PolicyEnforcer
	server       *http.Server
	upgrader     websocket.Upgrader

	mu               sync.RWMutex
	clients          map[*websocket.Conn]bool
	dashboardClients map[*websocket.Conn]bool // Real-time dashboard connections
	shutdown         chan struct{}
}

// New creates a new DNN node
func New(cfg *config.Config, db *database.Database) (*Node, error) {
	// Create the relay component
	r, err := relay.New(cfg, db)
	if err != nil {
		return nil, fmt.Errorf("failed to create relay: %w", err)
	}

	// Create the syncer component
	s, err := dnnSync.New(cfg, db)
	if err != nil {
		return nil, fmt.Errorf("failed to create syncer: %w", err)
	}

	node := &Node{
		config:           cfg,
		db:               db,
		relay:            r,
		syncer:           s,
		encoder:          encoder.NewEncoderWithNetwork(cfg.Network),
		queryBuilder:     database.NewQueryBuilder(db),
		policy:           policy.NewPolicyEnforcer(cfg.Network),
		clients:          make(map[*websocket.Conn]bool),
		dashboardClients: make(map[*websocket.Conn]bool),
		shutdown:         make(chan struct{}),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for now
			},
		},
	}

	return node, nil
}

// Start starts the DNN node
func (n *Node) Start() error {
	// Set up syncer callback for real-time dashboard updates
	n.syncer.OnUpdate = n.BroadcastDashboardUpdate

	// Wire relay → syncer: when a client submits an event to our relay,
	// the syncer rebroadcasts it to all connected and discovered relays
	n.relay.OnEventStored = n.syncer.RebroadcastEvent

	// Run event cleanup on startup (retroactive validation and pruning)
	go n.runStartupCleanup()

	// Start the syncer
	go n.syncer.Start()

	// Start the relay
	go n.relay.Start()

	// Setup HTTP routes
	router := mux.NewRouter()

	// Serve dashboard on root for browsers
	router.HandleFunc("/", n.handleRoot).Methods("GET")

	// Nostr relay endpoints
	router.HandleFunc("/", n.handleNIP11).Methods("GET").Headers("Accept", "application/nostr+json")
	router.HandleFunc("/", n.handleWebSocket)

	// DNN-specific endpoints
	router.HandleFunc("/dnn/resolve/{name}", n.handleResolve).Methods("GET")
	router.HandleFunc("/dnn/verify/{name}/{npub}", n.handleVerify).Methods("GET")
	router.HandleFunc("/dnn/lookup/npub/{npub}", n.handleLookupByNpub).Methods("GET")
	router.HandleFunc("/dnn/debug/anchors", n.handleDebugAnchors).Methods("GET")
	router.HandleFunc("/dnn/debug/events", n.handleDebugEvents).Methods("GET")
	router.HandleFunc("/dnn/debug/all-events", n.handleDebugAllEvents).Methods("GET")
	router.HandleFunc("/dnn/block/{block}", n.handleGetBlock).Methods("GET")
	router.HandleFunc("/dnn/status", n.handleStatus).Methods("GET")
	router.HandleFunc("/dnn/peers", n.handlePeers).Methods("GET")
	router.HandleFunc("/dnn/search", n.handleSearch).Methods("GET")
	router.HandleFunc("/dnn/anchors", n.handleAnchors).Methods("GET")
	router.HandleFunc("/dnn/stats", n.handleStats).Methods("GET")
	router.HandleFunc("/dnn/history/{name}", n.handleNameHistory).Methods("GET")
	router.HandleFunc("/dnn/user/{pubkey}", n.handleUserNames).Methods("GET")
	router.HandleFunc("/dnn/recent-blocks", n.handleRecentBlocks).Methods("GET")
	router.HandleFunc("/dnn/block-stats", n.handleBlockStats).Methods("GET")
	router.HandleFunc("/dnn/export", n.handleExport).Methods("GET")
	router.HandleFunc("/dnn/sync-block", n.handleSyncBlock).Methods("POST")
	router.HandleFunc("/dnn/sync-transaction", n.handleSyncTransaction).Methods("POST")
	router.HandleFunc("/dnn/sync-pending", n.handleSyncPending).Methods("POST")
	router.HandleFunc("/dnn/recent-events", n.handleRecentEvents).Methods("GET")
	router.HandleFunc("/dnn/check-event", n.handleCheckEvent).Methods("POST")
	router.HandleFunc("/dnn/event-details/{event_id}", n.handleEventDetails).Methods("GET")
	router.HandleFunc("/dnn/event-details-by-naddr/{naddr}", n.handleEventDetailsByNaddr).Methods("GET")
	router.HandleFunc("/dnn/event/{event_id}", n.handleGetEvent).Methods("GET")
	router.HandleFunc("/dnn/store-local-event", n.handleStoreLocalEvent).Methods("POST")
	router.HandleFunc("/dnn/derive-address/{pubkey}", n.handleDeriveAddress).Methods("GET")
	router.HandleFunc("/dnn/node-info", n.handleNodeInfo).Methods("GET")

	// Admin and Awareness endpoints
	router.HandleFunc("/dnn/admin-check", n.handleAdminCheck).Methods("POST")
	router.HandleFunc("/dnn/awareness/local", n.handleGetLocalMarks).Methods("GET")
	router.HandleFunc("/dnn/awareness/local", n.handleAddLocalMark).Methods("POST")
	router.HandleFunc("/dnn/awareness/local/{block}/{position}", n.handleDeleteLocalMark).Methods("DELETE")
	router.HandleFunc("/dnn/awareness/peers", n.handleGetPeerMarks).Methods("GET")
	router.HandleFunc("/dnn/awareness/stats", n.handleAwarenessStats).Methods("GET")
	router.HandleFunc("/dnn/awareness/sync", n.handleAwarenessSync).Methods("POST")

	// Relay database info endpoints
	router.HandleFunc("/dnn/relay/stats", n.handleRelayStats).Methods("GET")

	// Discovered peer nodes endpoints (from Kind 64600)
	router.HandleFunc("/dnn/discovered-peers", n.handleDiscoveredPeers).Methods("GET")
	router.HandleFunc("/dnn/discovered-relays", n.handleDiscoveredRelays).Methods("GET")
	router.HandleFunc("/dnn/discovered-relays", n.handleAddDiscoveredRelays).Methods("POST")

	// Dashboard WebSocket for real-time updates
	router.HandleFunc("/dnn/ws/dashboard", n.handleDashboardWebSocket)

	// Serve static files from embedded dashboard
	staticFS, err := fs.Sub(dashboard.Content, "static")
	if err != nil {
		log.Printf("Warning: Could not load embedded static files: %v", err)
	} else {
		router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}

	// Health check
	router.HandleFunc("/health", n.handleHealth).Methods("GET")

	n.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", n.config.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := n.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the DNN node
func (n *Node) Stop() error {
	close(n.shutdown)

	// Stop the syncer
	n.syncer.Stop()

	// Stop the relay
	n.relay.Stop()

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := n.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown server: %w", err)
	}

	// Close all WebSocket connections
	n.mu.Lock()
	for conn := range n.clients {
		conn.Close()
	}
	n.mu.Unlock()

	return nil
}

// runStartupCleanup performs retroactive validation and pruning of events
func (n *Node) runStartupCleanup() {
	log.Println("[NODE] Running startup event cleanup...")

	// Create address checker that derives Bitcoin addresses from pubkey
	// and checks if any match transactions in the database
	isTestnet := n.config.Network == "testnet" || n.config.Network == "dev"
	deriver := bitcoin.NewAddressDeriver(isTestnet)

	addressChecker := func(pubkey string) (bool, error) {
		// Derive all possible addresses from pubkey
		derivedAddrs, err := deriver.DeriveAddresses(pubkey)
		if err != nil {
			return false, err
		}

		// Extract address strings
		var addressStrings []string
		for _, addr := range derivedAddrs {
			addressStrings = append(addressStrings, addr.Address)
		}

		// Check if any address has a valid transaction
		return n.db.HasValidTransactionForAddresses(addressStrings)
	}

	// Run cleanup
	if err := n.db.RunEventCleanup(addressChecker); err != nil {
		log.Printf("[NODE] Cleanup error: %v", err)
	}
}

// handleRoot serves the dashboard HTML for browser requests
func (n *Node) handleRoot(w http.ResponseWriter, r *http.Request) {
	// Check if this is a browser request (not WebSocket or Nostr)
	if r.Header.Get("Upgrade") == "websocket" {
		n.handleWebSocket(w, r)
		return
	}

	// Serve dashboard HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")

	// Serve the embedded dashboard or redirect to dashboard.html
	dashboardHTML := n.getDashboardHTML()
	w.Write([]byte(dashboardHTML))
}

// handleNIP11 handles NIP-11 relay information requests
func (n *Node) handleNIP11(w http.ResponseWriter, r *http.Request) {
	info := nip11.RelayInformationDocument{
		Name:        "DNN Node",
		Description: "Decentralized Naming Network Node",
		PubKey:      n.config.NodePubkey,
		Contact:     "",
		SupportedNIPs: []int{
			1,  // Basic protocol
			11, // Relay information
			// Add DNN-specific NIP number when assigned
		},
		Software: "dnn-node/0.1.0",
		Version:  "0.1.0",
	}

	w.Header().Set("Content-Type", "application/nostr+json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(info)
}

// handleWebSocket handles WebSocket connections for Nostr protocol
func (n *Node) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := n.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Add client
	n.mu.Lock()
	n.clients[conn] = true
	n.mu.Unlock()

	// Remove client on disconnect
	defer func() {
		n.mu.Lock()
		delete(n.clients, conn)
		n.mu.Unlock()
	}()

	// Handle WebSocket messages
	n.relay.HandleConnection(conn)
}

// resolveDelegation fetches and parses connection data from a delegated 62600 event
// Returns the delegated ParsedConnection or nil if delegation cannot be resolved
func (n *Node) resolveDelegation(delegationNaddr string) (*ParsedConnection, string, error) {
	if delegationNaddr == "" {
		return nil, "", nil
	}

	// Decode the naddr to extract pubkey and d-tag
	prefix, data, err := nip19.Decode(delegationNaddr)
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode delegation naddr: %w", err)
	}

	if prefix != "naddr" {
		return nil, "", fmt.Errorf("delegation must be an naddr, got: %s", prefix)
	}

	entityPointer, ok := data.(nostr.EntityPointer)
	if !ok {
		return nil, "", fmt.Errorf("failed to parse entity pointer from naddr")
	}

	if entityPointer.Kind != 62600 {
		return nil, "", fmt.Errorf("delegated event must be kind 62600, got: %d", entityPointer.Kind)
	}

	log.Printf("[DELEGATION] Resolving delegation to pubkey=%s, d=%s", entityPointer.PublicKey[:8], entityPointer.Identifier)

	// Try to fetch the delegated connection event from local database using pubkey AND d-tag
	// This is important because a pubkey can have multiple connection events with different d-tags
	content, err := n.db.GetConnectionContentByPubkeyAndDTag(entityPointer.PublicKey, entityPointer.Identifier)

	if err != nil {
		// Try fetching from relays if not found locally
		log.Printf("[DELEGATION] Not found locally, querying relays for delegated event...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for _, relayURL := range n.config.RelayURLs {
			relay, err := nostr.RelayConnect(ctx, relayURL)
			if err != nil {
				continue
			}
			defer relay.Close()

			filter := nostr.Filter{
				Kinds:   []int{62600},
				Authors: []string{entityPointer.PublicKey},
				Tags: nostr.TagMap{
					"d": []string{entityPointer.Identifier},
				},
				Limit: 1,
			}

			events, err := relay.QuerySync(ctx, filter)
			if err == nil && len(events) > 0 {
				content = events[0].Content
				log.Printf("[DELEGATION] Fetched delegated event from relay %s", relayURL)
				break
			}
		}

		if content == "" {
			return nil, "", fmt.Errorf("could not fetch delegated connection event")
		}
	}

	// Parse the delegated connection content
	delegatedConn, err := parseConnectionContent(content, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse delegated connection: %w", err)
	}

	log.Printf("[DELEGATION] Successfully resolved delegation, records=%d", len(delegatedConn.Records))
	return delegatedConn, entityPointer.PublicKey, nil
}

// handleResolve handles DNN name resolution
func (n *Node) handleResolve(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fullName := vars["name"]

	// Check for subdomain format (e.g., banana.nabceabsurd)
	// A subdomain request looks like: domain.dnnname[.blockspec]
	// We need to detect when the first part is a domain key in the connection JSON
	lowerFullName := strings.ToLower(fullName)
	var subdomain string
	var parentName string

	// Helper to check if a string looks like a block specifier (n50, b1m50, etc.)
	// Block specifiers start with n/b followed by digits/shorthand
	isBlockSpec := func(s string) bool {
		if len(s) < 2 {
			return false
		}
		if s[0] != 'n' && s[0] != 'b' {
			return false
		}
		// Check if second char is a digit or 'm'/'h'/'k' (shorthand)
		secondChar := s[1]
		return (secondChar >= '0' && secondChar <= '9') || secondChar == 'm' || secondChar == 'h' || secondChar == 'k'
	}

	// Check if we have a domain.dnnname pattern
	parts := strings.SplitN(lowerFullName, ".", 2)

	if len(parts) == 2 && len(parts[0]) > 0 && len(parts[1]) > 0 {
		firstPart := parts[0]
		secondPart := parts[1]

		// First part should NOT be a block specifier (it's a domain like "banana")
		// Second part should be a valid DNN name (could be "nabceabsurd" or "nabceabsurd.n5")
		if !isBlockSpec(firstPart) {
			// Extract the DNN name from secondPart (strip any block spec suffix)
			secondParts := strings.Split(secondPart, ".")
			potentialDNNName := secondParts[0]

			// The DNN name should NOT be a block specifier
			// It's a name like "nabceabsurd" even if it starts with 'n'
			if !isBlockSpec(potentialDNNName) {
				subdomain = firstPart
				parentName = secondPart // Keep the full second part for parsing
				log.Printf("[SUBDOMAIN] Detected domain request: domain='%s', parent='%s'", subdomain, parentName)
			}
		}
	}

	// Parse the name format (alice.n50, alice.n50.5, alice.b1000050.5, alice.abc-vintage)
	// If we detected a subdomain, parse the parent name instead
	nameToResolve := lowerFullName
	if subdomain != "" {
		nameToResolve = parentName
	}

	name, blockNum, position, err := n.parseDNNName(nameToResolve)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid name format: %v", err), http.StatusBadRequest)
		return
	}

	// Debug logging
	blockStr := "nil"
	posStr := "nil"
	if blockNum != nil {
		blockStr = fmt.Sprintf("%d", *blockNum)
	}
	if position != nil {
		posStr = fmt.Sprintf("%d", *position)
	}
	log.Printf("[DEBUG] Resolve request: fullName=%s, name='%s', blockNum=%s, position=%s",
		fullName, name, blockStr, posStr)

	// Check for invalid block numbers (before genesis)
	if blockNum != nil && *blockNum < 0 {
		http.Error(w, fmt.Sprintf("Invalid block number: %d (before DNN genesis block). DNN starts at Bitcoin block %d.",
			*blockNum, constants.GetGenesisBlock(n.config.Network)), http.StatusBadRequest)
		return
	}

	// Query local database
	record, err := n.db.GetAnchorByName(name, blockNum, position)
	if err != nil {
		log.Printf("[ERROR] Database query failed: %v", err)
		http.Error(w, fmt.Sprintf("Error resolving name: %v", err), http.StatusInternalServerError)
		return
	}

	// If not found locally OR if we want to check for newer versions, query relays
	if record == nil {
		log.Printf("[SYNC] Not found locally, querying relays for: name='%s', blockNum=%s, position=%s",
			name, blockStr, posStr)

		// Try to fetch and sync from relays
		syncedRecord, syncErr := n.fetchAndSyncFromRelays(name, blockNum, position)
		if syncErr != nil {
			log.Printf("[SYNC] Relay sync failed: %v", syncErr)
			http.Error(w, "Name not found", http.StatusNotFound)
			return
		}

		record = syncedRecord
		log.Printf("[SYNC] Successfully synced record from relays")
	}

	// Double-check record exists after sync
	if record == nil {
		log.Printf("[ERROR] Record is nil after sync - likely missing referenced events")
		http.Error(w, "Name found but incomplete data (missing referenced events)", http.StatusNotFound)
		return
	}

	// Check if this DNN ID is blocked by node operator
	isBlocked, _ := n.db.IsBlocked(record.DNNBlock, record.Position, "")
	if isBlocked {
		log.Printf("[AWARENESS] Blocked resolve for n%d.%d (%s) [operator block]", record.DNNBlock, record.Position, name)
		http.Error(w, "Name blocked by node operator", http.StatusNotFound)
		return
	}

	// Generate npub from pubkey
	npub, err := nip19.EncodePublicKey(record.Pubkey)
	if err != nil {
		npub = ""
	}

	// Generate event identifiers (naddr for addressable, nevent for anchor)
	nameNaddr, _ := nip19.EncodeEntity(record.Pubkey, 61600, record.Name, []string{})
	connectionNaddr, _ := nip19.EncodeEntity(record.Pubkey, 62600, record.Name, []string{})
	metadataNaddr, _ := nip19.EncodeEntity(record.Pubkey, 63600, record.Name, []string{})
	anchorNevent, _ := nip19.EncodeEvent(record.ID, []string{}, record.Pubkey)

	// Generate encoded format
	encoded, _ := n.encoder.Encode(record.DNNBlock, record.Position)

	// Parse connection and metadata JSON into structured format
	parsedConnection, connErr := parseConnectionContent(record.ConnectionContent, record.Name)
	if connErr != nil {
		parsedConnection = &ParsedConnection{Records: []DNSRecord{}}
	}

	// Handle delegation - if present, fetch delegated connection data
	var delegatedFrom string
	if parsedConnection.Delegation != "" {
		log.Printf("[RESOLVE] Connection has delegation: %s", parsedConnection.Delegation[:min(32, len(parsedConnection.Delegation))]+"...")
		delegatedConn, delegatedPubkey, err := n.resolveDelegation(parsedConnection.Delegation)
		if err != nil {
			log.Printf("[RESOLVE] Delegation resolution failed: %v", err)
			// Continue with local connection data as fallback
		} else if delegatedConn != nil {
			// Use delegated records and cert, but keep original meta
			log.Printf("[RESOLVE] Using delegated connection data from %s", delegatedPubkey[:8])
			originalMeta := parsedConnection.Meta
			parsedConnection = delegatedConn
			if originalMeta != nil {
				parsedConnection.Meta = originalMeta // Preserve original meta
			}
			delegatedFrom = delegatedPubkey
		}
	}

	// Handle subdomain - if this is a subdomain request, look up the subdomain's connection data
	// A subdomain is valid if it exists as a key in the 62600 connection content.
	// No 61600 o-tag validation needed — 62600 is the source of truth for domains.
	var subdomainUsed string
	var domainFound = true
	if subdomain != "" {
		// Check if subdomain matches the primary name
		// If so, skip the lookup - we already have the right connection data
		if strings.EqualFold(subdomain, record.Name) {
			log.Printf("[SUBDOMAIN] Domain '%s' matches primary name '%s', using its data", subdomain, record.Name)
			// No action needed - parsedConnection already has this domain's data
		} else {
			// Look for the domain key in connection content directly
			subdomainConn, subErr := parseSubdomainConnectionContent(record.ConnectionContent, subdomain)
			if subErr != nil {
				log.Printf("[SUBDOMAIN] Domain '%s' not found in 62600 connection: %v", subdomain, subErr)
				// Don't return 404 — instead, return 200 with empty records + raw event
				// so the daemon can compare created_at across nodes and pick the freshest answer
				parsedConnection = &ParsedConnection{Records: []DNSRecord{}}
				domainFound = false
			} else {
				// Use the domain's connection data
				log.Printf("[SUBDOMAIN] Using connection data for domain '%s' (records=%d)", subdomain, len(subdomainConn.Records))
				parsedConnection = subdomainConn
				subdomainUsed = subdomain
			}
		}
	}

	parsedMetadata, metaErr := parseMetadataContent(record.MetadataContent)
	if metaErr != nil {
		parsedMetadata = &ParsedMetadata{}
	}

	// Build comprehensive response with structured data
	response := map[string]interface{}{
		"name":             record.Name,
		"dnn_block":        record.DNNBlock,
		"bitcoin_block":    record.BitcoinBlock,
		"position":         record.Position,
		"encoded":          encoded,
		"pubkey":           record.Pubkey,
		"npub":             npub,
		"verified":         true, // If we found it, it's verified
		"connection":       parsedConnection,
		"metadata":         parsedMetadata,
		"anchor_event":     anchorNevent,
		"name_event":       nameNaddr,
		"connection_event": connectionNaddr,
		"metadata_event":   metadataNaddr,
		"created_at":       record.CreatedAt,
		"domain_found":    domainFound,
	}

	// Add raw signed connection event for daemon signature verification
	if record.ConnEventID != "" {
		// Reconstruct tags from JSON
		var tags [][]string
		if record.ConnEventTagsJSON != "" {
			json.Unmarshal([]byte(record.ConnEventTagsJSON), &tags)
		}
		response["connection_event_raw"] = map[string]interface{}{
			"id":         record.ConnEventID,
			"pubkey":     record.ConnEventPubkey,
			"sig":        record.ConnEventSig,
			"created_at": record.ConnEventCreatedAt,
			"kind":       62600,
			"content":    record.ConnectionContent,
			"tags":       tags,
		}
	}

	// Add delegation info if used
	if delegatedFrom != "" {
		response["delegated_from"] = delegatedFrom
	}

	// Add subdomain info if used
	if subdomainUsed != "" {
		response["subdomain"] = subdomainUsed
	}

	// Add awareness consensus data for client-side filtering
	// Clients (browsers/OS) use this to apply their own filter level (off/security/strict)
	// Check both TLD-level (name="") and name-specific marks
	awareness := map[string]interface{}{}

	// TLD-level marks (whole DNN ID)
	tldLocalMark, _ := n.queryBuilder.GetLocalMarkByID(record.DNNBlock, record.Position, "")
	if tldLocalMark != nil {
		tld := map[string]interface{}{"mark": tldLocalMark.Mark}
		if tldLocalMark.Category != "" {
			tld["category"] = tldLocalMark.Category
		}
		awareness["tld_local"] = tld
	}

	tldPeerConsensus, _ := n.queryBuilder.GetPeerConsensus(record.DNNBlock, record.Position, "")
	if tldPeerConsensus != nil && tldPeerConsensus.TotalPeers > 0 {
		awareness["tld_peers"] = tldPeerConsensus
	}

	// Name-specific marks (e.g., "banana" under n50.1)
	// The resolved domain name is in record.Name; for subdomains use subdomainUsed
	domainName := record.Name
	if subdomainUsed != "" {
		domainName = subdomainUsed
	}
	if domainName != "" {
		nameLocalMark, _ := n.queryBuilder.GetLocalMarkByID(record.DNNBlock, record.Position, domainName)
		if nameLocalMark != nil {
			nm := map[string]interface{}{"mark": nameLocalMark.Mark, "name": domainName}
			if nameLocalMark.Category != "" {
				nm["category"] = nameLocalMark.Category
			}
			awareness["name_local"] = nm
		}

		namePeerConsensus, _ := n.queryBuilder.GetPeerConsensus(record.DNNBlock, record.Position, domainName)
		if namePeerConsensus != nil && namePeerConsensus.TotalPeers > 0 {
			awareness["name_peers"] = namePeerConsensus
		}
	}

	if len(awareness) > 0 {
		response["awareness"] = awareness
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleVerify verifies if a given npub owns a DNN name
func (n *Node) handleVerify(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fullName := vars["name"]
	npubStr := vars["npub"]

	// Decode npub to pubkey
	pubkey, err := decodePubkey(npubStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid npub: %v", err), http.StatusBadRequest)
		return
	}

	// Parse the DNN name (case-insensitive)
	name, blockNum, position, err := n.parseDNNName(strings.ToLower(fullName))
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid name format: %v", err), http.StatusBadRequest)
		return
	}

	// Get the anchor record
	record, err := n.db.GetAnchorByName(name, blockNum, position)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error verifying name: %v", err), http.StatusInternalServerError)
		return
	}

	owns := false
	var proof map[string]interface{}

	if record != nil && record.Pubkey == pubkey {
		owns = true
		proof = map[string]interface{}{
			"anchor_event_id": record.ID,
			"bitcoin_block":   record.BitcoinBlock,
			"dnn_block":       record.DNNBlock,
			"position":        record.Position,
		}
	}

	response := map[string]interface{}{
		"name":        fullName,
		"npub":        npubStr,
		"owns":        owns,
		"verified_at": time.Now().Format(time.RFC3339),
	}

	if owns {
		response["dnn_block"] = record.DNNBlock
		response["bitcoin_block"] = record.BitcoinBlock
		response["position"] = record.Position
		response["proof"] = proof
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleLookupByNpub looks up all DNN names owned by an npub
func (n *Node) handleLookupByNpub(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	npubStr := vars["npub"]

	// Decode npub to pubkey
	pubkey, err := decodePubkey(npubStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid npub: %v", err), http.StatusBadRequest)
		return
	}

	// Query database for all names owned by this pubkey
	names, err := n.db.GetNamesByPubkey(pubkey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error looking up names: %v", err), http.StatusInternalServerError)
		return
	}

	// Build response
	nameList := make([]map[string]interface{}, 0, len(names))
	for _, record := range names {
		// Use DNNBlock (not BitcoinBlock) for encoding - encoder expects DNN block number
		// The encoded format is like "nabob-about" where:
		// - "nabob" is the block prefix
		// - "about" is the BIP39 word encoding the position
		// This IS the DNN ID - we use it directly from the encoder
		encoded, _ := n.encoder.Encode(record.DNNBlock, record.Position)

		nameList = append(nameList, map[string]interface{}{
			"name":          record.Name,                                              // User's custom name (for display separately if needed)
			"format":        fmt.Sprintf(".n%d.%d", record.DNNBlock, record.Position), // Numeric format
			"encoded":       encoded,                                                  // DNN ID like "nabob-about"
			"dnn_block":     record.DNNBlock,
			"bitcoin_block": record.BitcoinBlock,
			"position":      record.Position,
			"verified":      true,
		})
	}

	response := map[string]interface{}{
		"npub":  npubStr,
		"names": nameList,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleDebugEvents shows event counts in database
func (n *Node) handleDebugEvents(w http.ResponseWriter, r *http.Request) {
	var nameCount, connCount, metaCount, anchorCount int

	rows, _ := n.db.RawQuery("SELECT COUNT(*) FROM name_events")
	if rows != nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&nameCount)
		}
	}

	rows, _ = n.db.RawQuery("SELECT COUNT(*) FROM connection_events")
	if rows != nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&connCount)
		}
	}

	rows, _ = n.db.RawQuery("SELECT COUNT(*) FROM metadata_events")
	if rows != nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&metaCount)
		}
	}

	rows, _ = n.db.RawQuery("SELECT COUNT(*) FROM anchor_events")
	if rows != nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&anchorCount)
		}
	}

	response := map[string]interface{}{
		"name_events":       nameCount,
		"connection_events": connCount,
		"metadata_events":   metaCount,
		"anchor_events":     anchorCount,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleDebugAllEvents shows ALL events from all tables (for debugging orphaned events)
func (n *Node) handleDebugAllEvents(w http.ResponseWriter, r *http.Request) {
	result := map[string]interface{}{}

	// Get counts first
	counts := map[string]int{}
	var count int
	if rows, err := n.db.RawQuery("SELECT COUNT(*) FROM name_events"); err == nil && rows.Next() {
		rows.Scan(&count)
		rows.Close()
	}
	counts["name_events"] = count
	if rows, err := n.db.RawQuery("SELECT COUNT(*) FROM connection_events"); err == nil && rows.Next() {
		rows.Scan(&count)
		rows.Close()
	}
	counts["connection_events"] = count
	if rows, err := n.db.RawQuery("SELECT COUNT(*) FROM metadata_events"); err == nil && rows.Next() {
		rows.Scan(&count)
		rows.Close()
	}
	counts["metadata_events"] = count
	if rows, err := n.db.RawQuery("SELECT COUNT(*) FROM anchor_events"); err == nil && rows.Next() {
		rows.Scan(&count)
		rows.Close()
	}
	counts["anchor_events"] = count
	result["counts"] = counts

	// Query name events
	nameRows, _ := n.db.RawQuery("SELECT id, pubkey, d_tag FROM name_events LIMIT 10")
	if nameRows != nil {
		defer nameRows.Close()
		names := []map[string]string{}
		for nameRows.Next() {
			var id, pubkey, dTag string
			nameRows.Scan(&id, &pubkey, &dTag)
			names = append(names, map[string]string{"id": id[:8], "pubkey": pubkey[:8], "d_tag": dTag})
		}
		result["name_events"] = names
	}

	// Query connection events
	connRows, _ := n.db.RawQuery("SELECT id, pubkey, d_tag FROM connection_events LIMIT 10")
	if connRows != nil {
		defer connRows.Close()
		conns := []map[string]string{}
		for connRows.Next() {
			var id, pubkey, dTag string
			connRows.Scan(&id, &pubkey, &dTag)
			conns = append(conns, map[string]string{"id": id[:8], "pubkey": pubkey[:8], "d_tag": dTag})
		}
		result["connection_events"] = conns
	}

	// Query metadata events
	metaRows, _ := n.db.RawQuery("SELECT id, pubkey, d_tag FROM metadata_events LIMIT 10")
	if metaRows != nil {
		defer metaRows.Close()
		metas := []map[string]string{}
		for metaRows.Next() {
			var id, pubkey, dTag string
			metaRows.Scan(&id, &pubkey, &dTag)
			metas = append(metas, map[string]string{"id": id[:8], "pubkey": pubkey[:8], "d_tag": dTag})
		}
		result["metadata_events"] = metas
	}

	// Query anchor events
	anchorRows, _ := n.db.RawQuery("SELECT id, pubkey, name_event_id, connection_event_id, metadata_event_id, dnn_block_number, position FROM anchor_events LIMIT 10")
	if anchorRows != nil {
		defer anchorRows.Close()
		anchors := []map[string]interface{}{}
		for anchorRows.Next() {
			var id, pubkey, nameID, connID, metaID string
			var dnnBlock int64
			var position int
			anchorRows.Scan(&id, &pubkey, &nameID, &connID, &metaID, &dnnBlock, &position)
			anchors = append(anchors, map[string]interface{}{
				"id":                  id[:8],
				"pubkey":              pubkey[:8],
				"name_event_id":       nameID[:8],
				"connection_event_id": connID[:8],
				"metadata_event_id":   metaID[:8],
				"dnn_block":           dnnBlock,
				"position":            position,
			})
		}
		result["anchor_events"] = anchors
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// handleDebugAnchors lists all anchor events for debugging
func (n *Node) handleDebugAnchors(w http.ResponseWriter, r *http.Request) {

	query := `
		SELECT
			a.id, a.pubkey, a.dnn_block_number, a.bitcoin_block_number, a.position,
			n.d_tag as name
		FROM anchor_events a
		JOIN name_events n ON a.name_event_id = n.id
		ORDER BY a.dnn_block_number, a.position
		LIMIT 100
	`

	rows, err := n.db.RawQuery(query)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error querying anchors: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	anchors := []map[string]interface{}{}
	for rows.Next() {
		var id, pubkey, name string
		var dnnBlock, bitcoinBlock int64
		var position int

		if err := rows.Scan(&id, &pubkey, &dnnBlock, &bitcoinBlock, &position, &name); err != nil {
			continue
		}

		// Generate encoded format
		encoded, _ := n.encoder.Encode(bitcoinBlock, position)

		anchors = append(anchors, map[string]interface{}{
			"id":            id,
			"pubkey":        pubkey,
			"name":          name,
			"dnn_block":     dnnBlock,
			"bitcoin_block": bitcoinBlock,
			"position":      position,
			"encoded":       encoded,
			"n_format":      fmt.Sprintf("n%d.%d", dnnBlock, position),
			"b_format":      fmt.Sprintf("b%d.%d", bitcoinBlock, position),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"count":   len(anchors),
		"anchors": anchors,
	})
}

// fetchAndSyncFromRelays queries relays for missing DNN events and stores them locally
func (n *Node) fetchAndSyncFromRelays(name string, blockNum *int64, position *int) (*database.AnchorRecord, error) {
	// Query relays for anchor events matching the criteria
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	relays := n.config.RelayURLs
	if len(relays) == 0 {
		return nil, fmt.Errorf("no relays configured")
	}

	// Build filter for anchor events
	filter := nostr.Filter{
		Kinds: []int{60600, 61600, 62600, 63600}, // Query all DNN event types
		Tags: nostr.TagMap{
			"t": []string{"DNN"},
		},
		Limit: 500,
	}

	log.Printf("[SYNC] Querying %d relays...", len(relays))

	// Query all relays and collect events
	allEvents := make(map[string]*nostr.Event) // Deduplicate by ID

	for _, relayURL := range relays {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			log.Printf("[SYNC] Failed to connect to %s: %v", relayURL, err)
			continue
		}

		events, err := relay.QuerySync(ctx, filter)
		relay.Close()

		if err != nil {
			log.Printf("[SYNC] Query failed on %s: %v", relayURL, err)
			continue
		}

		for _, event := range events {
			allEvents[event.ID] = event
		}

		log.Printf("[SYNC] Got %d events from %s", len(events), relayURL)
	}

	if len(allEvents) == 0 {
		return nil, fmt.Errorf("no DNN events found on any relay")
	}

	log.Printf("[SYNC] Found %d total unique DNN events across all relays", len(allEvents))

	// Separate events by kind
	var anchors, names, connections, metadata []*nostr.Event
	for _, event := range allEvents {
		switch event.Kind {
		case 60600:
			anchors = append(anchors, event)
		case 61600:
			names = append(names, event)
		case 62600:
			connections = append(connections, event)
		case 63600:
			metadata = append(metadata, event)
		}
	}

	log.Printf("[SYNC] Events by kind: anchors=%d, names=%d, connections=%d, metadata=%d",
		len(anchors), len(names), len(connections), len(metadata))

	// Store all events locally (relay will handle validation and deduplication)
	storedCount := 0
	for _, event := range names {
		if err := n.db.StoreNameEvent(event); err == nil {
			storedCount++
		}
	}
	log.Printf("[SYNC] Stored %d name events", storedCount)

	storedCount = 0
	for _, event := range connections {
		if err := n.db.StoreConnectionEvent(event); err == nil {
			storedCount++
		}
	}
	log.Printf("[SYNC] Stored %d connection events", storedCount)

	storedCount = 0
	for _, event := range metadata {
		if err := n.db.StoreMetadataEvent(event); err == nil {
			storedCount++
		}
	}
	log.Printf("[SYNC] Stored %d metadata events", storedCount)

	// Now we need to find and update/store the anchor event with correct references
	for _, anchorEvent := range anchors {
		// Extract d tag (for addressable event) and transaction ID from tags
		var anchorDTag, txID string

		for _, tag := range anchorEvent.Tags {
			if len(tag) >= 2 {
				switch tag[0] {
				case "d":
					anchorDTag = tag[1]
				case "x":
					txID = tag[1]
				}
			}
		}

		// The anchor event contains naddr values in its tags (per NIP-DN spec)
		// We need to match those with stored events to get event IDs
		anchorPubkey := anchorEvent.PubKey
		var nameEventID, connEventID, metaEventID string

		// Match events by pubkey and find complete set with same d-tag
		// This handles both old events (direct IDs) and new events (naddr values)
		for _, nameEvent := range names {
			if nameEvent.PubKey != anchorPubkey {
				continue
			}

			// Get d-tag from this name event
			var eventDTag string
			for _, tag := range nameEvent.Tags {
				if len(tag) >= 2 && tag[0] == "d" {
					eventDTag = tag[1]
					break
				}
			}

			if eventDTag == "" {
				continue
			}

			// Try to find connection with same d-tag
			var tempConnID string
			for _, connEvent := range connections {
				if connEvent.PubKey == anchorPubkey {
					for _, tag := range connEvent.Tags {
						if len(tag) >= 2 && tag[0] == "d" && tag[1] == eventDTag {
							tempConnID = connEvent.ID
							break
						}
					}
					if tempConnID != "" {
						break
					}
				}
			}

			// Try to find metadata with same d-tag
			var tempMetaID string
			for _, metaEvent := range metadata {
				if metaEvent.PubKey == anchorPubkey {
					for _, tag := range metaEvent.Tags {
						if len(tag) >= 2 && tag[0] == "d" && tag[1] == eventDTag {
							tempMetaID = metaEvent.ID
							break
						}
					}
					if tempMetaID != "" {
						break
					}
				}
			}

			// If we found all three, this is our complete set!
			if tempConnID != "" && tempMetaID != "" {
				nameEventID = nameEvent.ID
				connEventID = tempConnID
				metaEventID = tempMetaID
				// Found complete set with this d-tag
				log.Printf("[SYNC] Found complete set! d-tag: %s", eventDTag)
				log.Printf("[SYNC] - Name event: %s", nameEventID[:8])
				log.Printf("[SYNC] - Connection event: %s", connEventID[:8])
				log.Printf("[SYNC] - Metadata event: %s", metaEventID[:8])
				break
			}
		}

		if nameEventID != "" && connEventID != "" && metaEventID != "" && txID != "" && anchorDTag != "" {
			log.Printf("[SYNC] All 4 events matched! Updating anchor with correct IDs")

			// Get the ACTUAL block and position from the Bitcoin transaction
			// Do NOT use query parameters - those are what the user searched for, not where the event actually is!
			tx, txErr := n.db.GetBitcoinTransaction(txID)
			if txErr != nil || tx == nil {
				log.Printf("[SYNC] Cannot find Bitcoin transaction %s in database, skipping anchor storage", txID[:8])
				continue
			}

			dnnBlock := tx.DNNBlock
			bitcoinBlock := tx.BitcoinBlock
			pos := tx.Position

			log.Printf("[SYNC] Using actual transaction location: DNN block=%d, Bitcoin block=%d, position=%d", dnnBlock, bitcoinBlock, pos)

			// Update the anchor event's tags with actual event IDs instead of naddr
			// Create a new event with correct references
			// Note: Anchor event has its own d tag for addressability, separate from the referenced events
			updatedAnchor := &nostr.Event{
				ID:        anchorEvent.ID,
				PubKey:    anchorEvent.PubKey,
				CreatedAt: anchorEvent.CreatedAt,
				Kind:      60600,
				Tags: nostr.Tags{
					{"d", anchorDTag},  // D tag for anchor's addressability (from original event)
					{"n", nameEventID}, // Name event ID (n = names)
					{"c", connEventID}, // Connection event ID (c = connection)
					{"m", metaEventID}, // Metadata event ID (m = metadata)
					{"x", txID},        // Transaction ID (x = transaction)
					{"t", "DNN"},
				},
				Content: anchorEvent.Content,
				Sig:     anchorEvent.Sig,
			}

			// Store anchor event with correct references
			err := n.db.StoreAnchorEvent(updatedAnchor, bitcoinBlock, dnnBlock, pos)
			if err != nil {
				log.Printf("[SYNC] Failed to store anchor event: %v", err)
			} else {
				log.Printf("[SYNC] Successfully stored anchor with correct event ID references")
			}
		} else {
			log.Printf("[SYNC] Incomplete match: name=%v, conn=%v, meta=%v, tx=%v",
				nameEventID != "", connEventID != "", metaEventID != "", txID != "")
		}
	}

	// Now query local database again
	log.Printf("[SYNC] Querying database: name='%s', blockNum=%v, position=%v", name, blockNum, position)
	record, queryErr := n.db.GetAnchorByName(name, blockNum, position)
	if queryErr != nil {
		log.Printf("[SYNC] Database query error: %v", queryErr)
		return nil, queryErr
	}
	if record == nil {
		log.Printf("[SYNC] No record found in database after sync")
	} else {
		log.Printf("[SYNC] Found record: name='%s', block=%d, pos=%d", record.Name, record.DNNBlock, record.Position)
	}
	return record, nil
}

// parseDNNName parses a DNN name in various formats and returns the components
func (n *Node) parseDNNName(fullName string) (name string, blockNum *int64, position *int, err error) {
	// Format examples:
	// - alice (just name, latest)
	// - alice.n50 or alice.n5h (DNN block with shorthand, position 1)
	// - alice.n50.5 or alice.n5h.5 (DNN block, position 5)
	// - alice.b1000050 or alice.b1m50 (Bitcoin block with shorthand, position 1)
	// - alice.b1000050.5 or alice.b1m50.5 (Bitcoin block, position 5)
	// - alice.nabcvintage (encoded format, no dashes)
	// - nabcvintage (just encoded ID, no name prefix)
	// - n50 or n5h (just DNN block, no name prefix)
	// - b1000050 or b1m50 (just Bitcoin block, no name prefix)

	// Handle formats without dots (just ID, no name)
	if !strings.Contains(fullName, ".") {
		// Could be: nabcvintage, n50, b1000050, or just a name
		if len(fullName) > 0 && (fullName[0] == 'n' || fullName[0] == 'b') {
			// Starts with n or b, treat as ID without name
			idPart := fullName

			// Check if it's encoded format (nXXX-word or nXXXword)
			if isEncodedFormat(idPart[1:]) {
				// Encoded format: nabcvintage
				block, pos, decodeErr := n.encoder.Decode(idPart)
				if decodeErr != nil {
					return "", nil, nil, fmt.Errorf("invalid encoded format: %v", decodeErr)
				}
				blockNum = &block
				position = &pos
				// For ID-only queries, we don't filter by name
				return "", blockNum, position, nil
			}

			// Numeric format: n50, n5h, b1000050, b1m50
			block, pos, parseErr := parseBlockWithShorthand(idPart[1:])
			if parseErr != nil {
				return "", nil, nil, parseErr
			}

			// Convert Bitcoin block to DNN block if needed
			if idPart[0] == 'b' {
				genesisBlock := constants.GetGenesisBlock(n.config.Network)
				dnnBlock := block - genesisBlock
				blockNum = &dnnBlock
			} else {
				blockNum = &block
			}

			position = &pos
			return "", blockNum, position, nil
		}

		// Just a name, return latest
		return fullName, nil, nil, nil
	}

	// Has dots: name.id or name.id.position OR just id.position (like n4.6)
	parts := strings.Split(fullName, ".")

	// Check if first part is an ID (starts with n or b)
	if len(parts[0]) > 0 && (parts[0][0] == 'n' || parts[0][0] == 'b') {
		// Format is: n4.6 or b1000050.5 (ID with position, no name)
		// Treat first part as the block part
		name = ""
		blockPart := parts[0]

		// Parse the block number
		block, pos, parseErr := parseBlockWithShorthand(blockPart[1:])
		if parseErr != nil {
			return "", nil, nil, parseErr
		}

		// Convert Bitcoin block to DNN block if needed
		if blockPart[0] == 'b' {
			genesisBlock := constants.GetGenesisBlock(n.config.Network)
			dnnBlock := block - genesisBlock
			blockNum = &dnnBlock
		} else {
			blockNum = &block
		}

		// Check for position in second part
		if len(parts) >= 2 {
			var explicitPos int
			_, scanErr := fmt.Sscanf(parts[1], "%d", &explicitPos)
			if scanErr == nil {
				position = &explicitPos
			} else {
				position = &pos
			}
		} else {
			position = &pos
		}

		return "", blockNum, position, nil
	}

	// Regular format: name.id or name.id.position
	name = parts[0]

	if len(parts) == 1 {
		return name, nil, nil, nil
	}

	blockPart := parts[1]

	// Check for encoded format (nabcvintage, nabc45rockettumble)
	if len(blockPart) > 1 && blockPart[0] == 'n' && isEncodedFormat(blockPart[1:]) {
		// Encoded format: alice.nabcvintage
		block, pos, decodeErr := n.encoder.Decode(blockPart)
		if decodeErr != nil {
			return "", nil, nil, fmt.Errorf("invalid encoded format: %v", decodeErr)
		}
		blockNum = &block
		position = &pos
		return name, blockNum, position, nil
	}

	// Check for n/b prefix with numeric format
	if blockPart[0] == 'n' || blockPart[0] == 'b' {
		// Parse block number with shorthand support
		block, pos, parseErr := parseBlockWithShorthand(blockPart[1:])
		if parseErr != nil {
			return "", nil, nil, parseErr
		}

		// Convert Bitcoin block to DNN block if needed
		if blockPart[0] == 'b' {
			genesisBlock := constants.GetGenesisBlock(n.config.Network)
			dnnBlock := block - genesisBlock
			blockNum = &dnnBlock
		} else {
			blockNum = &block
		}

		// Check for explicit position in third part
		if len(parts) == 3 {
			var explicitPos int
			_, scanErr := fmt.Sscanf(parts[2], "%d", &explicitPos)
			if scanErr != nil {
				return "", nil, nil, fmt.Errorf("invalid position: %v", scanErr)
			}
			position = &explicitPos
		} else {
			position = &pos
		}

		return name, blockNum, position, nil
	}

	return "", nil, nil, fmt.Errorf("invalid name format")
}

// parseBlockWithShorthand parses a block number with optional shorthand notation
// Examples: "50" → 50, "5h" → 500, "1m50" → 1000050, "2k5h" → 2500
func parseBlockWithShorthand(blockStr string) (block int64, position int, err error) {
	// Default position 1
	position = 1

	// Shorthand multipliers
	multipliers := map[string]int64{
		"h":  100,                 // hundred
		"k":  1000,                // thousand
		"m":  1000000,             // million
		"b":  1000000000,          // billion
		"t":  1000000000000,       // trillion
		"qd": 1000000000000000,    // quadrillion
		"qt": 1000000000000000000, // quintillion
		// Note: sp and o would overflow int64, handle separately if needed
	}

	var result int64
	currentNum := ""
	i := 0

	for i < len(blockStr) {
		c := blockStr[i]

		if c >= '0' && c <= '9' {
			currentNum += string(c)
			i++
		} else {
			// Hit a letter, check for multiplier
			num := int64(0)
			if currentNum != "" {
				fmt.Sscanf(currentNum, "%d", &num)
				currentNum = ""
			} else {
				num = 1 // No number before multiplier means 1x
			}

			// Try two-letter multipliers first
			if i+1 < len(blockStr) {
				twoLetter := blockStr[i : i+2]
				if mult, ok := multipliers[twoLetter]; ok {
					result += num * mult
					i += 2
					continue
				}
			}

			// Try single-letter multiplier
			oneLetter := string(blockStr[i])
			if mult, ok := multipliers[oneLetter]; ok {
				result += num * mult
				i++
			} else {
				return 0, 0, fmt.Errorf("invalid shorthand: %s", oneLetter)
			}
		}
	}

	// Add any remaining number
	if currentNum != "" {
		num := int64(0)
		fmt.Sscanf(currentNum, "%d", &num)
		result += num
	}

	if result == 0 {
		return 0, 0, fmt.Errorf("invalid block number")
	}

	return result, position, nil
}

// isEncodedFormat checks if a string is in the encoded format (has letters)
func isEncodedFormat(s string) bool {
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// decodePubkey decodes an npub or hex pubkey to hex pubkey
func decodePubkey(pubkeyStr string) (string, error) {
	// Check if it's an npub
	if strings.HasPrefix(pubkeyStr, "npub1") {
		_, pubkey, err := nip19.Decode(pubkeyStr)
		if err != nil {
			return "", err
		}
		return pubkey.(string), nil
	}

	// Assume it's hex
	if len(pubkeyStr) == 64 {
		return pubkeyStr, nil
	}

	return "", fmt.Errorf("invalid pubkey format")
}

// handleGetBlock handles DNN block retrieval
func (n *Node) handleGetBlock(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	block := vars["block"]

	// TODO: Implement block retrieval
	response := map[string]interface{}{
		"block":   block,
		"message": "Block retrieval not yet implemented",
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleStatus handles node status requests
func (n *Node) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Get stats from database for block heights
	stats, err := n.queryBuilder.GetStats()
	if err != nil {
		log.Printf("Failed to get stats for status: %v", err)
		stats = &database.Stats{} // Use empty stats on error
	}

	status := map[string]interface{}{
		"node_pubkey":          n.config.NodePubkey,
		"node_npub":            n.config.NodeNpub,
		"connected_peers":      len(n.clients),
		"syncing":              n.syncer.IsSyncing(),
		"last_sync":            n.syncer.LastSyncTime(),
		"dnn_version":          "0.1.0",
		"uptime":               time.Since(n.relay.StartTime()).Seconds(),
		"latest_dnn_block":     stats.LatestDNNBlock,
		"latest_bitcoin_block": stats.LatestBitcoinBlock,
		"total_names":          stats.TotalNames,
		"total_anchors":        stats.TotalAnchors,
		"total_bitcoin_txs":    stats.TotalBitcoinTxs,
		"total_pending_txs":    stats.TotalPendingTxs,
		"database_size_bytes":  stats.DatabaseSize,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(status)
}

// handlePeers handles peer list requests
func (n *Node) handlePeers(w http.ResponseWriter, r *http.Request) {
	peers := n.syncer.GetPeers()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(peers)
}

// handleNodeInfo handles node information requests for the dashboard
func (n *Node) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	// Get the current host for relay URL
	host := r.Host
	scheme := "ws"
	if r.TLS != nil {
		scheme = "wss"
	}
	relayURL := fmt.Sprintf("%s://%s", scheme, host)

	// Get peers from syncer
	peers := n.syncer.GetPeers()

	// Build response
	info := map[string]interface{}{
		"node_pubkey":       n.config.NodePubkey,
		"node_npub":         n.config.NodeNpub,
		"genesis_block":     constants.GetGenesisBlock(n.config.Network),
		"network":           n.config.Network,
		"relay_url":         relayURL,
		"configured_relays": n.config.RelayURLs,
		"configured_peers":  n.config.PeerNodes,
		"peers":             peers,
		"awareness": map[string]interface{}{
			"enabled": n.config.EnableAwareness,

			"total_marks": 0,
		},
		"admin_npub": n.config.AdminNpub,
	}

	// Get awareness stats if enabled
	if n.config.EnableAwareness {
		stats, err := n.queryBuilder.GetAwarenessStats()
		if err == nil && stats != nil {
			info["awareness"] = map[string]interface{}{
				"enabled": true,

				"local_total":   stats.LocalTotal,
				"local_allow":   stats.LocalAllow,
				"local_neutral": stats.LocalNeutral,
				"local_block":   stats.LocalBlock,
				"peer_total":    stats.PeerTotal,
				"peer_allow":    stats.PeerAllow,
				"peer_neutral":  stats.PeerNeutral,
				"peer_block":    stats.PeerBlock,
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(info)
}

// handleAdminCheck checks if the provided pubkey is the admin
func (n *Node) handleAdminCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pubkey string `json:"pubkey"`
		Npub   string `json:"npub"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Check both pubkey and npub
	isAdmin := false
	if req.Npub != "" && req.Npub == n.config.AdminNpub {
		isAdmin = true
	}
	// Also check pubkey if provided (convert admin npub to hex for comparison)
	if req.Pubkey != "" && n.config.AdminNpub != "" {
		// Simple npub comparison (npub contains the pubkey)
		adminHex := n.config.AdminNpub // In production, decode npub to hex
		if req.Pubkey == adminHex {
			isAdmin = true
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]bool{"is_admin": isAdmin})
}

// handleGetLocalMarks returns all local awareness marks
func (n *Node) handleGetLocalMarks(w http.ResponseWriter, r *http.Request) {
	marks, err := n.queryBuilder.GetLocalMarks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(marks)
}

// handleAddLocalMark adds a new local awareness mark
func (n *Node) handleAddLocalMark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DNNBlock int64  `json:"dnn_block"`
		Position int    `json:"position"`
		Name     string `json:"name"`
		Mark     string `json:"mark"`
		Category string `json:"category"`
		Reason   string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := n.queryBuilder.AddLocalMark(req.DNNBlock, req.Position, req.Name, req.Mark, req.Category, req.Reason); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDeleteLocalMark removes a local awareness mark
func (n *Node) handleDeleteLocalMark(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	blockStr := vars["block"]
	posStr := vars["position"]
	nameStr := r.URL.Query().Get("name") // optional query param: ?name=banana

	var block int64
	var pos int
	fmt.Sscanf(blockStr, "%d", &block)
	fmt.Sscanf(posStr, "%d", &pos)

	if err := n.queryBuilder.DeleteLocalMark(block, pos, nameStr); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleGetPeerMarks returns aggregated peer awareness marks
func (n *Node) handleGetPeerMarks(w http.ResponseWriter, r *http.Request) {
	aggregates, err := n.queryBuilder.GetAllPeerMarks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(aggregates)
}

// handleAwarenessStats returns awareness database statistics
func (n *Node) handleAwarenessStats(w http.ResponseWriter, r *http.Request) {
	stats, err := n.queryBuilder.GetAwarenessStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build response with filter_level from config
	response := map[string]interface{}{
		"local_total":   stats.LocalTotal,
		"local_allow":   stats.LocalAllow,
		"local_neutral": stats.LocalNeutral,
		"local_block":   stats.LocalBlock,
		"peer_total":    stats.PeerTotal,
		"peer_allow":    stats.PeerAllow,
		"peer_neutral":  stats.PeerNeutral,
		"peer_block":    stats.PeerBlock,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleAwarenessSync fetches admin's NIP-51 awareness list from relays and syncs to local
func (n *Node) handleAwarenessSync(w http.ResponseWriter, r *http.Request) {
	if n.config.AdminNpub == "" {
		http.Error(w, "No admin npub configured", http.StatusBadRequest)
		return
	}

	// Decode admin npub to hex pubkey
	prefix, pubkeyData, err := nip19.Decode(n.config.AdminNpub)
	if err != nil || prefix != "npub" {
		http.Error(w, "Invalid admin npub", http.StatusBadRequest)
		return
	}
	adminPubkey := pubkeyData.(string)

	// Query relays for admin's awareness list (kind:30000 d:dnn-awareness)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var foundEvent *nostr.Event
	for _, relayURL := range n.config.RelayURLs {
		relay := nostr.NewRelay(ctx, relayURL)
		if err := relay.Connect(ctx); err != nil {
			continue
		}

		filter := nostr.Filter{
			Authors: []string{adminPubkey},
			Kinds:   []int{30000},
			Tags:    nostr.TagMap{"d": []string{"dnn-awareness"}},
			Limit:   1,
		}

		events, err := relay.QuerySync(ctx, filter)
		relay.Close()

		if err == nil && len(events) > 0 {
			if foundEvent == nil || events[0].CreatedAt > foundEvent.CreatedAt {
				foundEvent = events[0]
			}
		}
	}

	if foundEvent == nil {
		http.Error(w, "No awareness list found on relays", http.StatusNotFound)
		return
	}

	// Parse event tags and sync to local DB
	syncCount := 0

	// First, clear existing local marks (full sync)
	if err := n.queryBuilder.ClearLocalMarks(); err != nil {
		log.Printf("[AWARENESS] Failed to clear local marks: %v", err)
	}

	// Parse ["dnn", "{name.}n{block}.{pos}", "mark", "category", "reason"] tags
	for _, tag := range foundEvent.Tags {
		if len(tag) >= 3 && tag[0] == "dnn" {
			dnnID := tag[1]
			mark := tag[2]
			category := ""
			reason := ""
			if len(tag) >= 4 {
				category = tag[3]
			}
			if len(tag) >= 5 {
				reason = tag[4]
			}

			// Parse DNN ID: "n{block}.{pos}" or "name.n{block}.{pos}"
			var block int64
			var pos int
			var name string

			// Try name.n{block}.{pos} first
			parts := strings.SplitN(dnnID, ".", 2)
			if len(parts) == 2 {
				if _, err := fmt.Sscanf(parts[1], "n%d.%d", &block, &pos); err == nil {
					name = parts[0] // Has a name prefix
				} else if _, err := fmt.Sscanf(dnnID, "n%d.%d", &block, &pos); err != nil {
					continue // Neither format matched
				}
			} else {
				continue // Needs at least one dot
			}

			// Add to local marks
			if err := n.queryBuilder.AddLocalMark(block, pos, name, mark, category, reason); err == nil {
				syncCount++
			}
		}
	}

	result := map[string]interface{}{
		"synced_count":     syncCount,
		"event_id":         foundEvent.ID,
		"event_created_at": foundEvent.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// handleHealth handles health check requests
func (n *Node) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := map[string]string{
		"status": "healthy",
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(health)
}

// handleSearch handles name search requests
func (n *Node) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 20
	offset := 0

	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
		if limit > 100 {
			limit = 100
		}
	}

	if offsetStr != "" {
		fmt.Sscanf(offsetStr, "%d", &offset)
	}

	results, err := n.queryBuilder.SearchNames(query, limit, offset)
	if err != nil {
		http.Error(w, fmt.Sprintf("Search failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(results)
}

// handleAnchors handles all Bitcoin transactions with anchor event status (paginated)
func (n *Node) handleAnchors(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	status := r.URL.Query().Get("status")                 // complete, pending, all
	search := r.URL.Query().Get("search")                 // search term
	address := r.URL.Query().Get("address")               // legacy filter
	bitcoinBlockStr := r.URL.Query().Get("bitcoin_block") // filter by bitcoin block

	limit := 50 // Default to 50 for paginated requests
	offset := 0
	bitcoinBlock := 0

	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
		if limit > 500 {
			limit = 500 // Cap at 500 for performance
		}
	}

	if offsetStr != "" {
		fmt.Sscanf(offsetStr, "%d", &offset)
	}

	if bitcoinBlockStr != "" {
		fmt.Sscanf(bitcoinBlockStr, "%d", &bitcoinBlock)
	}

	// Default status to "all" if not specified
	if status == "" {
		status = "all"
	}

	// Legacy address filter support
	if address != "" {
		results, err := n.queryBuilder.GetBitcoinTransactionsByAddress(address, limit)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get transactions: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(results)
		return
	}

	// Use new paginated query
	result, err := n.queryBuilder.GetBitcoinTransactionsPaginated(limit, offset, status, search, bitcoinBlock)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get transactions: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// handleStats handles statistics requests
func (n *Node) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := n.queryBuilder.GetStats()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get stats: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(stats)
}

// handleBlockStats handles block anchor counts requests for the visualizer
func (n *Node) handleBlockStats(w http.ResponseWriter, r *http.Request) {
	startBlockStr := r.URL.Query().Get("startBlock")
	endBlockStr := r.URL.Query().Get("endBlock")

	var startBlock, endBlock int
	if startBlockStr != "" {
		fmt.Sscanf(startBlockStr, "%d", &startBlock)
	}
	if endBlockStr != "" {
		fmt.Sscanf(endBlockStr, "%d", &endBlock)
	}

	// Validate range
	if startBlock <= 0 || endBlock <= 0 || startBlock > endBlock {
		http.Error(w, "Invalid block range. Provide startBlock and endBlock query params.", http.StatusBadRequest)
		return
	}

	// Limit range to prevent abuse
	if endBlock-startBlock > 100 {
		endBlock = startBlock + 100
	}

	counts, err := n.queryBuilder.GetBlockAnchorCounts(startBlock, endBlock)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get block stats: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(counts)
}

// handleNameHistory handles name history requests
func (n *Node) handleNameHistory(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	history, err := n.queryBuilder.GetNameHistory(name)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get history: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(history)
}

// handleUserNames handles user names requests
func (n *Node) handleUserNames(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	pubkey := vars["pubkey"]

	names, err := n.queryBuilder.GetUserNames(pubkey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get user names: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(names)
}

// handleRecentBlocks handles recent blocks requests
func (n *Node) handleRecentBlocks(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 10

	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
		if limit > 100 {
			limit = 100
		}
	}

	blocks, err := n.queryBuilder.GetRecentBlocks(limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get recent blocks: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(blocks)
}

// handleExport handles data export requests
func (n *Node) handleExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	if format != "json" {
		format = "json" // Default to JSON
	}

	data, err := n.queryBuilder.ExportNames()
	if err != nil {
		http.Error(w, fmt.Sprintf("Export failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=dnn-names.json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(data)
}

// handleSyncBlock handles manual block sync requests
func (n *Node) handleSyncBlock(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BlockNumber int64 `json:"block_number"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if request.BlockNumber < 930130 {
		http.Error(w, "Block number must be >= 930130 (DNN genesis for testing)", http.StatusBadRequest)
		return
	}

	// Trigger manual sync in a goroutine
	go func() {
		if err := n.syncer.SyncSpecificBlock(request.BlockNumber); err != nil {
			log.Printf("Manual sync of block %d failed: %v", request.BlockNumber, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Syncing block %d...", request.BlockNumber),
		"block":   request.BlockNumber,
	})
}

// handleSyncTransaction handles manual transaction sync requests
func (n *Node) handleSyncTransaction(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TransactionID string `json:"transaction_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if len(request.TransactionID) != 64 {
		http.Error(w, "Transaction ID must be 64 characters (hex)", http.StatusBadRequest)
		return
	}

	// Trigger manual sync in a goroutine
	go func() {
		if err := n.syncer.SyncSpecificTransaction(request.TransactionID); err != nil {
			log.Printf("Manual sync of transaction %s failed: %v", request.TransactionID, err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"message":        fmt.Sprintf("Syncing transaction %s...", request.TransactionID[:16]+"..."),
		"transaction_id": request.TransactionID,
	})
}

// handleRecentEvents handles recent events requests
func (n *Node) handleRecentEvents(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50

	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
		if limit > 200 {
			limit = 200
		}
	}

	// Query recent events from database
	filter := nostr.Filter{
		Kinds: []int{61600, 62600, 63600, 60600},
		Limit: limit,
	}

	events, err := n.db.QueryEvents(filter)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get recent events: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(events)
}

// handleCheckEvent handles event lookup requests across relays
func (n *Node) handleCheckEvent(w http.ResponseWriter, r *http.Request) {
	var request struct {
		EventID string `json:"event_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if len(request.EventID) != 64 {
		http.Error(w, "Event ID must be 64 hex characters", http.StatusBadRequest)
		return
	}

	// Query relays for this event
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type RelayResult struct {
		RelayURL string       `json:"relay_url"`
		Found    bool         `json:"found"`
		Event    *nostr.Event `json:"event,omitempty"`
		Error    string       `json:"error,omitempty"`
	}

	results := make([]RelayResult, 0)

	// Check local database first
	localEvents, _ := n.db.QueryEvents(nostr.Filter{
		IDs:   []string{request.EventID},
		Limit: 1,
	})

	localResult := RelayResult{
		RelayURL: "LOCAL DATABASE",
		Found:    len(localEvents) > 0,
	}
	if len(localEvents) > 0 {
		localResult.Event = localEvents[0]
	}
	results = append(results, localResult)

	// Check each configured relay
	for _, relayURL := range n.config.RelayURLs {
		result := RelayResult{
			RelayURL: relayURL,
			Found:    false,
		}

		relay := nostr.NewRelay(ctx, relayURL)
		if err := relay.Connect(ctx); err != nil {
			result.Error = fmt.Sprintf("Connection failed: %v", err)
			results = append(results, result)
			continue
		}

		filter := nostr.Filter{
			IDs:   []string{request.EventID},
			Limit: 1,
		}

		events, err := relay.QuerySync(ctx, filter)
		relay.Close()

		if err != nil {
			result.Error = fmt.Sprintf("Query failed: %v", err)
		} else if len(events) > 0 {
			result.Found = true
			result.Event = events[0]
		}

		results = append(results, result)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"event_id": request.EventID,
		"results":  results,
	})
}

// handleSyncPending handles manual pending transaction sync trigger
func (n *Node) handleSyncPending(w http.ResponseWriter, r *http.Request) {
	// Trigger sync in a goroutine
	go func() {
		log.Println("Manual pending transaction sync triggered from UI")
		if err := n.syncer.SyncPendingTransactions(); err != nil {
			log.Printf("Manual pending sync failed: %v", err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Checking pending transactions for anchor events...",
	})
}

// fetchEventFromDBOrRelays attempts to fetch an event from local DB first, then from relays
func (n *Node) fetchEventFromDBOrRelays(eventID string, kind int) *nostr.Event {
	// Try local DB first
	events, err := n.db.QueryEvents(nostr.Filter{
		IDs:   []string{eventID},
		Kinds: []int{kind},
		Limit: 1,
	})
	if err == nil && len(events) > 0 {
		return events[0]
	}

	// If not in DB, try fetching from configured relays IN PARALLEL
	log.Printf("Event %s not in local DB, querying %d relays in parallel...", eventID[:16], len(n.config.RelayURLs))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second) // Reduced timeout
	defer cancel()

	type result struct {
		event *nostr.Event
		relay string
	}

	resultChan := make(chan result, len(n.config.RelayURLs))

	// Query all relays in parallel
	for _, relayURL := range n.config.RelayURLs {
		go func(url string) {
			relay := nostr.NewRelay(ctx, url)
			if err := relay.Connect(ctx); err != nil {
				resultChan <- result{nil, url}
				return
			}

			filter := nostr.Filter{
				IDs:   []string{eventID},
				Kinds: []int{kind},
				Limit: 1,
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
	for i := 0; i < len(n.config.RelayURLs); i++ {
		select {
		case res := <-resultChan:
			if res.event != nil {
				log.Printf("Found event %s on relay %s", eventID[:16], res.relay)
				return res.event
			}
		case <-ctx.Done():
			log.Printf("Timeout querying relays for event %s", eventID[:16])
			return nil
		}
	}

	log.Printf("Event %s not found in DB or relays", eventID[:16])
	return nil
}

// fetchEventByDTag fetches an addressable replaceable event by d-tag, pubkey, and kind
// This function checks BOTH local DB and external relays, returns the NEWEST version,
// and syncs newer events to local DB
func (n *Node) fetchEventByDTag(dTag string, pubkey string, kind int) *nostr.Event {
	log.Printf("[fetchEventByDTag] Looking for kind=%d pubkey=%s d-tag=%s", kind, pubkey[:min(len(pubkey), 16)], dTag[:min(len(dTag), 16)])

	// Try local DB first
	var localEvent *nostr.Event
	events, err := n.db.QueryEvents(nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{kind},
		Tags:    nostr.TagMap{"d": []string{dTag}},
		Limit:   1,
	})
	if err == nil && len(events) > 0 {
		localEvent = events[0]
		log.Printf("[fetchEventByDTag] Found in local DB: id=%s created_at=%d", events[0].ID[:16], events[0].CreatedAt)
	} else {
		log.Printf("[fetchEventByDTag] Not in local DB (err=%v, count=%d)", err, len(events))
	}

	// Also query relays for potentially newer version
	var newestEvent *nostr.Event = localEvent

	if len(n.config.RelayURLs) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		type result struct {
			event *nostr.Event
			relay string
		}

		resultChan := make(chan result, len(n.config.RelayURLs))

		// Query all relays in parallel
		for _, relayURL := range n.config.RelayURLs {
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

		// Collect all results and find the newest
		for i := 0; i < len(n.config.RelayURLs); i++ {
			select {
			case res := <-resultChan:
				if res.event != nil {
					// Update newestEvent if relay event is newer, or if same timestamp but different ID (potentially newer content)
					isNewer := newestEvent == nil || res.event.CreatedAt > newestEvent.CreatedAt
					isSameTimeDifferentEvent := newestEvent != nil && res.event.CreatedAt == newestEvent.CreatedAt && res.event.ID != newestEvent.ID

					if isNewer || isSameTimeDifferentEvent {
						log.Printf("[fetchEventByDTag] Found event on %s (id=%s, created_at=%d)", res.relay, res.event.ID[:16], res.event.CreatedAt)
						newestEvent = res.event
					}
				}
			case <-ctx.Done():
				log.Printf("[fetchEventByDTag] Timeout querying relays for d-tag %s", dTag[:min(len(dTag), 16)])
				break
			}
		}

		// If we found a newer event on relays (or same timestamp but different event), sync it to local DB
		shouldSync := newestEvent != nil && (localEvent == nil ||
			newestEvent.CreatedAt > localEvent.CreatedAt ||
			(newestEvent.CreatedAt == localEvent.CreatedAt && newestEvent.ID != localEvent.ID))

		if shouldSync {
			log.Printf("[fetchEventByDTag] Syncing event to local DB (kind=%d, id=%s, created_at=%d)",
				kind, newestEvent.ID[:16], newestEvent.CreatedAt)
			switch kind {
			case 61600:
				if err := n.db.StoreNameEvent(newestEvent); err != nil {
					log.Printf("[fetchEventByDTag] Failed to sync name event: %v", err)
				}
			case 62600:
				if err := n.db.StoreConnectionEvent(newestEvent); err != nil {
					log.Printf("[fetchEventByDTag] Failed to sync connection event: %v", err)
				}
			case 63600:
				if err := n.db.StoreMetadataEvent(newestEvent); err != nil {
					log.Printf("[fetchEventByDTag] Failed to sync metadata event: %v", err)
				}
			}
		}
	}

	if newestEvent == nil {
		log.Printf("[fetchEventByDTag] Event with d-tag %s (kind %d) not found in DB or relays", dTag[:min(len(dTag), 16)], kind)
	}

	return newestEvent
}

// Helper function for min
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// handleEventDetails fetches complete event details including all referenced events
func (n *Node) handleEventDetails(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	eventID := vars["event_id"]

	if len(eventID) != 64 {
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	// Query for the anchor event (kind 60600)
	events, err := n.db.QueryEvents(nostr.Filter{
		IDs:   []string{eventID},
		Kinds: []int{60600},
		Limit: 1,
	})

	if err != nil || len(events) == 0 {
		http.Error(w, "Event not found", http.StatusNotFound)
		return
	}

	anchorEvent := events[0]

	// Extract referenced naddr/event IDs from anchor event tags
	var nameRef, connectionRef, metadataRef string

	for _, tag := range anchorEvent.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "n":
				nameRef = tag[1]
			case "c":
				connectionRef = tag[1]
			case "m":
				metadataRef = tag[1]
			}
		}
	}

	// Get anchor's created_at for validation
	anchorCreatedAt := int64(anchorEvent.CreatedAt)

	// Helper function to decode naddr/event ID and fetch event
	// Only returns events that existed at or before anchor creation
	fetchEventFromRef := func(ref string, expectedKind int) *nostr.Event {
		if ref == "" {
			return nil
		}

		// Check if it's an naddr (starts with "naddr1")
		if strings.HasPrefix(ref, "naddr1") {
			// Decode the naddr
			prefix, data, err := nip19.Decode(ref)
			if err != nil {
				log.Printf("Failed to decode naddr: %v", err)
				return nil
			}

			if prefix != "naddr" {
				log.Printf("Expected naddr prefix, got %s", prefix)
				return nil
			}

			// Extract data through JSON marshaling
			jsonData, err := json.Marshal(data)
			if err != nil {
				log.Printf("Failed to marshal naddr data: %v", err)
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
				log.Printf("Failed to unmarshal naddr data: %v", err)
				return nil
			}

			// Fetch using extracted data
			event := n.fetchEventByDTag(decoded.Identifier, decoded.Pubkey, decoded.Kind)

			// NOTE: If the fetched event is newer than the anchor, it means the event
			// was updated after anchoring. We log a warning but still return it
			// to allow users to view/edit their current events.
			// The anchor's naddr reference points to a specific version, but for
			// editing purposes, we show the current version.
			if event != nil && int64(event.CreatedAt) > anchorCreatedAt {
				log.Printf("INFO: Event kind %d for d-tag %s was updated after anchoring (event created_at=%d > anchor created_at=%d). Showing current version.",
					decoded.Kind, decoded.Identifier[:min(len(decoded.Identifier), 8)], event.CreatedAt, anchorCreatedAt)
				// Still return the event - user should see their current version
			}

			return event
		}

		// Fallback: treat as raw event ID and fetch by ID
		return n.fetchEventFromDBOrRelays(ref, expectedKind)
	}

	// Fetch referenced events
	var nameEvent, connectionEvent, metadataEvent *nostr.Event

	log.Printf("[EventDetails] Anchor refs - name: %q, connection: %q, metadata: %q",
		nameRef, connectionRef, metadataRef)

	nameEvent = fetchEventFromRef(nameRef, 61600)
	log.Printf("[EventDetails] Fetched name event: %v", nameEvent != nil)

	connectionEvent = fetchEventFromRef(connectionRef, 62600)
	log.Printf("[EventDetails] Fetched connection event: %v", connectionEvent != nil)

	metadataEvent = fetchEventFromRef(metadataRef, 63600)
	log.Printf("[EventDetails] Fetched metadata event: %v", metadataEvent != nil)

	// Helper function to encode event as naddr (for addressable events only)
	encodeEvent := func(event *nostr.Event) map[string]string {
		if event == nil {
			return nil
		}

		result := make(map[string]string)

		// Addressable events (with 'd' tag) get naddr encoding
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "d" {
				naddr, err := nip19.EncodeEntity(event.PubKey, event.Kind, tag[1], []string{})
				if err == nil {
					result["naddr"] = naddr
				}
				break
			}
		}

		return result
	}

	// Build response with encoded addresses
	response := map[string]interface{}{
		"anchor_event":        anchorEvent,
		"anchor_encoding":     encodeEvent(anchorEvent),
		"name_event":          nameEvent,
		"name_encoding":       encodeEvent(nameEvent),
		"connection_event":    connectionEvent,
		"connection_encoding": encodeEvent(connectionEvent),
		"metadata_event":      metadataEvent,
		"metadata_encoding":   encodeEvent(metadataEvent),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleEventDetailsByNaddr fetches anchor event details by naddr (instead of event ID)
// This ensures we always get the LATEST version of the anchor, avoiding stale cache issues
func (n *Node) handleEventDetailsByNaddr(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	naddrStr := vars["naddr"]

	if naddrStr == "" || !strings.HasPrefix(naddrStr, "naddr1") {
		http.Error(w, "Invalid naddr", http.StatusBadRequest)
		return
	}

	// Decode the naddr to get kind, pubkey, d-tag
	prefix, data, err := nip19.Decode(naddrStr)
	if err != nil {
		http.Error(w, "Failed to decode naddr: "+err.Error(), http.StatusBadRequest)
		return
	}

	if prefix != "naddr" {
		http.Error(w, "Expected naddr prefix", http.StatusBadRequest)
		return
	}

	// Extract data through JSON marshaling (same pattern as handleEventDetails)
	jsonData, err := json.Marshal(data)
	if err != nil {
		http.Error(w, "Failed to marshal naddr data", http.StatusInternalServerError)
		return
	}

	type naddrData struct {
		Kind       int      `json:"kind"`
		Pubkey     string   `json:"pubkey"`
		Identifier string   `json:"identifier"`
		Relays     []string `json:"relays"`
	}

	var decoded naddrData
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		http.Error(w, "Failed to unmarshal naddr data", http.StatusInternalServerError)
		return
	}

	// Query local database first
	localEvents, _ := n.db.QueryEvents(nostr.Filter{
		Authors: []string{decoded.Pubkey},
		Kinds:   []int{decoded.Kind},
		Tags:    map[string][]string{"d": {decoded.Identifier}},
		Limit:   1,
	})

	var localEvent *nostr.Event
	if len(localEvents) > 0 {
		localEvent = localEvents[0]
	}

	// Also query external relays for potentially newer versions
	var newestEvent *nostr.Event = localEvent

	if len(n.config.RelayURLs) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for _, relayURL := range n.config.RelayURLs {
			relay, err := nostr.RelayConnect(ctx, relayURL)
			if err != nil {
				continue
			}

			events, err := relay.QuerySync(ctx, nostr.Filter{
				Authors: []string{decoded.Pubkey},
				Kinds:   []int{decoded.Kind},
				Tags:    nostr.TagMap{"d": []string{decoded.Identifier}},
				Limit:   1,
			})
			relay.Close()

			if err != nil || len(events) == 0 {
				continue
			}

			relayEvent := events[0]

			// Validate that relay event has new-format tags (n, c, m, x)
			// Reject old-format events (names, connection, metadata, transaction)
			hasNewFormatTags := false
			hasOldFormatTags := false
			for _, tag := range relayEvent.Tags {
				if len(tag) >= 2 {
					switch tag[0] {
					case "n", "c", "m", "x":
						hasNewFormatTags = true
					case "names", "connection", "metadata", "transaction":
						hasOldFormatTags = true
					}
				}
			}

			if hasOldFormatTags || !hasNewFormatTags {
				log.Printf("[EventDetailsByNaddr] Skipping relay %s event - has old-format tags", relayURL)
				continue
			}

			// Check if relay event is newer
			if newestEvent == nil || relayEvent.CreatedAt > newestEvent.CreatedAt {
				newestEvent = relayEvent
				log.Printf("[EventDetailsByNaddr] Found newer event on %s (created_at=%d vs local=%d)",
					relayURL, relayEvent.CreatedAt, func() int64 {
						if localEvent != nil {
							return int64(localEvent.CreatedAt)
						}
						return 0
					}())

				// Sync to local database
				if decoded.Kind == 60600 {
					// For anchors, we need block/position from existing record
					bitcoinBlock, dnnBlock, position, _ := n.db.GetAnchorBlockPosition(decoded.Pubkey, decoded.Identifier)
					if err := n.db.StoreAnchorEvent(relayEvent, bitcoinBlock, dnnBlock, position); err == nil {
						log.Printf("[EventDetailsByNaddr] Synced newer anchor to local DB")
					}
				}
			}
		}

	}

	if newestEvent == nil {
		http.Error(w, "Anchor event not found", http.StatusNotFound)
		return
	}

	anchorEvent := newestEvent

	// Extract referenced naddr/event IDs from anchor event tags
	var nameRef, connectionRef, metadataRef string

	for _, tag := range anchorEvent.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "n":
				nameRef = tag[1]
			case "c":
				connectionRef = tag[1]
			case "m":
				metadataRef = tag[1]
			}
		}
	}

	// Get anchor's created_at for validation
	anchorCreatedAt := int64(anchorEvent.CreatedAt)

	// Helper function to decode naddr/event ID and fetch event
	fetchEventFromRef := func(ref string, expectedKind int) *nostr.Event {
		if ref == "" {
			return nil
		}

		// Check if it's an naddr (starts with "naddr1")
		if strings.HasPrefix(ref, "naddr1") {
			// Decode the naddr
			prefix, data, err := nip19.Decode(ref)
			if err != nil {
				log.Printf("Failed to decode naddr: %v", err)
				return nil
			}

			if prefix != "naddr" {
				log.Printf("Expected naddr prefix, got %s", prefix)
				return nil
			}

			// Extract data through JSON marshaling
			jsonData, err := json.Marshal(data)
			if err != nil {
				log.Printf("Failed to marshal naddr data: %v", err)
				return nil
			}

			var decoded naddrData
			if err := json.Unmarshal(jsonData, &decoded); err != nil {
				log.Printf("Failed to unmarshal naddr data: %v", err)
				return nil
			}

			// Fetch using extracted data
			event := n.fetchEventByDTag(decoded.Identifier, decoded.Pubkey, decoded.Kind)

			if event != nil && int64(event.CreatedAt) > anchorCreatedAt {
				log.Printf("INFO: Event kind %d for d-tag %s was updated after anchoring. Showing current version.",
					decoded.Kind, decoded.Identifier[:min(len(decoded.Identifier), 8)])
			}

			return event
		}

		// Fallback: treat as raw event ID and fetch by ID
		return n.fetchEventFromDBOrRelays(ref, expectedKind)
	}

	// Fetch referenced events
	var nameEvent, connectionEvent, metadataEvent *nostr.Event

	log.Printf("[EventDetailsByNaddr] Anchor refs - name: %q, connection: %q, metadata: %q",
		nameRef, connectionRef, metadataRef)

	nameEvent = fetchEventFromRef(nameRef, 61600)
	connectionEvent = fetchEventFromRef(connectionRef, 62600)
	metadataEvent = fetchEventFromRef(metadataRef, 63600)

	// Helper function to encode event as naddr
	encodeEvent := func(event *nostr.Event) map[string]string {
		if event == nil {
			return nil
		}

		result := make(map[string]string)

		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "d" {
				naddr, err := nip19.EncodeEntity(event.PubKey, event.Kind, tag[1], []string{})
				if err == nil {
					result["naddr"] = naddr
				}
				break
			}
		}

		return result
	}

	// Query anchor_events table for dnn_block and position
	var dnnBlock, position int64
	if anchorEvent != nil {
		rows, err := n.db.RawQuery(`SELECT dnn_block_number, position FROM anchor_events WHERE id = ?`, anchorEvent.ID)
		if err == nil && rows != nil {
			defer rows.Close()
			if rows.Next() {
				rows.Scan(&dnnBlock, &position)
			}
		}
	}

	// Build response - include both original anchor refs and re-encoded naddrs
	response := map[string]interface{}{
		"anchor_event":    anchorEvent,
		"anchor_encoding": encodeEvent(anchorEvent),
		// DNN identification
		"dnn_block": dnnBlock,
		"position":  position,
		"dnn_id":    fmt.Sprintf("n%d.%d", dnnBlock, position),
		// Original naddr references from anchor tags (these are what the anchor actually references)
		"anchor_name_ref":       nameRef,
		"anchor_connection_ref": connectionRef,
		"anchor_metadata_ref":   metadataRef,
		// Fetched events and their re-encoded naddrs
		"name_event":          nameEvent,
		"name_encoding":       encodeEvent(nameEvent),
		"connection_event":    connectionEvent,
		"connection_encoding": encodeEvent(connectionEvent),
		"metadata_event":      metadataEvent,
		"metadata_encoding":   encodeEvent(metadataEvent),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleStoreLocalEvent stores a signed DNN event directly to local database
// This is used when the local relay WebSocket is unavailable but events were published to external relays
func (n *Node) handleStoreLocalEvent(w http.ResponseWriter, r *http.Request) {
	// Parse the signed event from request body
	var event nostr.Event
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, "Invalid event JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate basic event structure
	if event.ID == "" || event.PubKey == "" || event.Sig == "" {
		http.Error(w, "Event missing required fields (id, pubkey, sig)", http.StatusBadRequest)
		return
	}

	// Verify signature
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		http.Error(w, "Invalid event signature", http.StatusBadRequest)
		return
	}

	log.Printf("[StoreLocalEvent] Received event kind=%d id=%s pubkey=%s",
		event.Kind, event.ID[:8], event.PubKey[:8])

	// Store based on event kind
	var storeErr error
	switch event.Kind {
	case 60600:
		// For anchor events, we need block/position info
		// Try to find existing anchor with same d-tag to get block/position
		var dTag string
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "d" {
				dTag = tag[1]
				break
			}
		}

		// Query for existing anchor's block/position
		existingEvents, _ := n.db.QueryEvents(nostr.Filter{
			Authors: []string{event.PubKey},
			Kinds:   []int{60600},
			Tags:    map[string][]string{"d": {dTag}},
			Limit:   1,
		})

		if len(existingEvents) > 0 {
			// Get block/position from existing anchor using database helper
			bitcoinBlock, dnnBlock, position, err := n.db.GetAnchorBlockPosition(event.PubKey, dTag)

			if err == nil {
				storeErr = n.db.StoreAnchorEvent(&event, bitcoinBlock, dnnBlock, position)
				log.Printf("[StoreLocalEvent] Updated anchor with block=%d pos=%d", dnnBlock, position)
			} else {
				log.Printf("[StoreLocalEvent] Could not find existing anchor position, storing anyway: %v", err)
				storeErr = n.db.StoreAnchorEvent(&event, 0, 0, 0)
			}
		} else {
			log.Printf("[StoreLocalEvent] No existing anchor found, cannot determine block/position")
			http.Error(w, "Cannot store new anchor event - no existing anchor record", http.StatusBadRequest)
			return
		}

	case 61600:
		// Apply rate limits before storing
		if limitResult, err := n.policy.EnforceRateLimits(&event, n.db); err == nil {
			for _, evtID := range limitResult.EventsToDelete {
				n.db.DeleteEventByID(event.Kind, evtID)
			}
			for _, dTagToDelete := range limitResult.DTagsToDelete {
				n.db.DeleteEventsByDTag(event.PubKey, event.Kind, dTagToDelete)
			}
			if limitResult.VersionExceeded {
				log.Printf("[RateLimit] Deleted oldest version for d-tag (kind=%d)", event.Kind)
			}
			if limitResult.DTagExceeded {
				log.Printf("[RateLimit] Deleted oldest d-tag (kind=%d)", event.Kind)
			}
		}
		storeErr = n.db.StoreNameEvent(&event)
	case 62600:
		// Apply rate limits before storing
		if limitResult, err := n.policy.EnforceRateLimits(&event, n.db); err == nil {
			for _, evtID := range limitResult.EventsToDelete {
				n.db.DeleteEventByID(event.Kind, evtID)
			}
			for _, dTagToDelete := range limitResult.DTagsToDelete {
				n.db.DeleteEventsByDTag(event.PubKey, event.Kind, dTagToDelete)
			}
		}
		storeErr = n.db.StoreConnectionEvent(&event)
	case 63600:
		// Apply rate limits before storing
		if limitResult, err := n.policy.EnforceRateLimits(&event, n.db); err == nil {
			for _, evtID := range limitResult.EventsToDelete {
				n.db.DeleteEventByID(event.Kind, evtID)
			}
			for _, dTagToDelete := range limitResult.DTagsToDelete {
				n.db.DeleteEventsByDTag(event.PubKey, event.Kind, dTagToDelete)
			}
		}
		storeErr = n.db.StoreMetadataEvent(&event)
	case 64600:
		// Node discovery event - delegate to syncer's handler which does verification
		log.Printf("[StoreLocalEvent] Processing 64600 event from %s", event.PubKey[:8])
		go n.syncer.HandleNodeDiscoveryFromLocal(&event)
		// No storeErr - the syncer handles storage asynchronously
	default:
		http.Error(w, fmt.Sprintf("Unsupported event kind: %d", event.Kind), http.StatusBadRequest)
		return
	}

	if storeErr != nil {
		log.Printf("[StoreLocalEvent] Failed to store event: %v", storeErr)
		http.Error(w, "Failed to store event: "+storeErr.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[StoreLocalEvent] Successfully stored event kind=%d id=%s", event.Kind, event.ID[:8])

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"message":  "Event stored locally",
		"event_id": event.ID,
	})
}

// handleGetEvent fetches a single event of any kind (for My Published Events section)
func (n *Node) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	eventID := vars["event_id"]

	if len(eventID) != 64 {
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	// Try to fetch the event from DB or relays (any kind)
	// Try common DNN kinds first
	kinds := []int{60600, 61600, 62600, 63600}
	var foundEvent *nostr.Event

	for _, kind := range kinds {
		event := n.fetchEventFromDBOrRelays(eventID, kind)
		if event != nil {
			foundEvent = event
			break
		}
	}

	if foundEvent == nil {
		http.Error(w, "Event not found", http.StatusNotFound)
		return
	}

	// Generate encoding (only naddr for addressable events)
	encoding := make(map[string]string)

	// Addressable events (with 'd' tag) get naddr encoding
	for _, tag := range foundEvent.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			naddr, err := nip19.EncodeEntity(foundEvent.PubKey, foundEvent.Kind, tag[1], []string{})
			if err == nil {
				encoding["naddr"] = naddr
			}
			break
		}
	}

	// Build response
	response := map[string]interface{}{
		"event":    foundEvent,
		"encoding": encoding,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleDeriveAddress derives Bitcoin addresses from a Nostr pubkey
func (n *Node) handleDeriveAddress(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	pubkey := vars["pubkey"]

	if len(pubkey) != 64 {
		http.Error(w, "Invalid pubkey", http.StatusBadRequest)
		return
	}

	// Use address deriver
	deriver := bitcoin.NewAddressDeriver(false) // mainnet for now
	addresses, err := deriver.DeriveAddresses(pubkey)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to derive addresses: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"pubkey":    pubkey,
		"addresses": addresses,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// getDashboardHTML returns the dashboard HTML
// getDashboardHTML returns the dashboard HTML from embedded files
func (n *Node) getDashboardHTML() string {
	html, err := dashboard.GetHTML()
	if err != nil {
		log.Printf("[Dashboard] Error loading embedded HTML: %v", err)
		return "<html><body><h1>Dashboard Error</h1><p>Failed to load dashboard: " + err.Error() + "</p></body></html>"
	}
	return html
}

// BroadcastEvent broadcasts an event to all connected clients
func (n *Node) BroadcastEvent(event *nostr.Event) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	message := []interface{}{"EVENT", event}
	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Failed to marshal event: %v", err)
		return
	}

	for conn := range n.clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("Failed to send event to client: %v", err)
		}
	}
}

// DashboardUpdate represents a real-time update for the dashboard
type DashboardUpdate struct {
	Type      string      `json:"type"`      // "stats", "block_synced", "anchor_found", "transaction_stored"
	Data      interface{} `json:"data"`      // The update payload
	Timestamp int64       `json:"timestamp"` // Unix timestamp
}

// handleDashboardWebSocket handles WebSocket connections for real-time dashboard updates
func (n *Node) handleDashboardWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := n.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Dashboard WebSocket upgrade error: %v", err)
		return
	}

	// Register this client
	n.mu.Lock()
	n.dashboardClients[conn] = true
	clientCount := len(n.dashboardClients)
	n.mu.Unlock()

	log.Printf("[DASHBOARD-WS] Client connected (total: %d)", clientCount)

	// Send initial stats on connect
	go func() {
		stats, err := n.queryBuilder.GetStats()
		if err == nil {
			n.sendDashboardUpdate(conn, &DashboardUpdate{
				Type:      "stats",
				Data:      stats,
				Timestamp: time.Now().Unix(),
			})
		}
	}()

	// Keep connection alive and handle disconnection
	defer func() {
		n.mu.Lock()
		delete(n.dashboardClients, conn)
		remaining := len(n.dashboardClients)
		n.mu.Unlock()
		conn.Close()
		log.Printf("[DASHBOARD-WS] Client disconnected (remaining: %d)", remaining)
	}()

	// Read messages (for ping/pong and keepalive)
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break // Client disconnected
		}
	}
}

// sendDashboardUpdate sends an update to a single dashboard client
func (n *Node) sendDashboardUpdate(conn *websocket.Conn, update *DashboardUpdate) error {
	data, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// BroadcastDashboardUpdate sends an update to all connected dashboard clients
func (n *Node) BroadcastDashboardUpdate(updateType string, data interface{}) {
	n.mu.RLock()
	clientCount := len(n.dashboardClients)
	n.mu.RUnlock()

	if clientCount == 0 {
		return // No dashboard clients connected
	}

	update := &DashboardUpdate{
		Type:      updateType,
		Data:      data,
		Timestamp: time.Now().Unix(),
	}

	updateData, err := json.Marshal(update)
	if err != nil {
		log.Printf("[DASHBOARD-WS] Failed to marshal update: %v", err)
		return
	}

	n.mu.RLock()
	defer n.mu.RUnlock()

	for conn := range n.dashboardClients {
		if err := conn.WriteMessage(websocket.TextMessage, updateData); err != nil {
			// Don't log every error, just silently continue (client might have disconnected)
			continue
		}
	}
}

// handleRelayStats returns relay database statistics
func (n *Node) handleRelayStats(w http.ResponseWriter, r *http.Request) {
	// Helper function to query counts
	countTable := func(table string) int {
		var count int
		rows, err := n.db.RawQuery("SELECT COUNT(*) FROM " + table)
		if err != nil {
			return 0
		}
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&count)
		}
		return count
	}

	// Get all table counts
	tables := map[string]int{
		"dnn_blocks":            countTable("dnn_blocks"),
		"name_events":           countTable("name_events"),
		"connection_events":     countTable("connection_events"),
		"metadata_events":       countTable("metadata_events"),
		"anchor_events":         countTable("anchor_events"),
		"peer_nodes":            countTable("peer_nodes"),
		"awareness_marks_local": countTable("awareness_marks_local"),
		"awareness_marks_peers": countTable("awareness_marks_peers"),
		"bitcoin_transactions":  countTable("bitcoin_transactions"),
		"sync_state":            countTable("sync_state"),
		"metrics":               countTable("metrics"),
		"event_cache":           countTable("event_cache"),
	}

	// Get sync state values
	syncState := map[string]string{}
	rows, err := n.db.RawQuery("SELECT key, value FROM sync_state")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key, value string
			rows.Scan(&key, &value)
			syncState[key] = value
		}
	}

	// Get recent blocks summary
	var latestDNNBlock, latestBitcoinBlock int64
	rows, _ = n.db.RawQuery("SELECT block_number, bitcoin_block_number FROM dnn_blocks ORDER BY block_number DESC LIMIT 1")
	if rows != nil {
		defer rows.Close()
		if rows.Next() {
			rows.Scan(&latestDNNBlock, &latestBitcoinBlock)
		}
	}

	// Calculate total unique DNN IDs (anchor events)
	totalDNNIDs := countTable("anchor_events")

	response := map[string]interface{}{
		"tables":               tables,
		"sync_state":           syncState,
		"latest_dnn_block":     latestDNNBlock,
		"latest_bitcoin_block": latestBitcoinBlock,
		"total_dnn_ids":        totalDNNIDs,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(response)
}

// handleDiscoveredPeers returns peer nodes discovered from Kind 64600 events
func (n *Node) handleDiscoveredPeers(w http.ResponseWriter, r *http.Request) {
	// Get query parameters for pagination and search
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	search := r.URL.Query().Get("search")

	limit := 10 // Default
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
		limit = l
	}

	offset := 0
	if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
		offset = o
	}

	result, err := n.queryBuilder.GetPeerNodesPaginated(limit, offset, search)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get discovered peers: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// handleDiscoveredRelays returns unique relays discovered from peer nodes + user NIP-65
func (n *Node) handleDiscoveredRelays(w http.ResponseWriter, r *http.Request) {
	// Get query parameters for pagination and search
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	search := r.URL.Query().Get("search")

	limit := 10 // Default
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
		limit = l
	}

	offset := 0
	if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
		offset = o
	}

	result, err := n.queryBuilder.GetDiscoveredRelaysPaginated(limit, offset, search)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get discovered relays: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(result)
}

// handleAddDiscoveredRelays stores user-discovered relays to the database
func (n *Node) handleAddDiscoveredRelays(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Relays       []string `json:"relays"`
		Source       string   `json:"source"`
		DiscoveredBy string   `json:"discovered_by"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(request.Relays) == 0 {
		http.Error(w, "No relays provided", http.StatusBadRequest)
		return
	}

	// Default source to "nip65" if not specified
	source := request.Source
	if source == "" {
		source = "nip65"
	}

	if err := n.queryBuilder.StoreDiscoveredRelays(request.Relays, source, request.DiscoveredBy); err != nil {
		http.Error(w, fmt.Sprintf("Failed to store relays: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("[API] Stored %d discovered relays from %s (source: %s)", len(request.Relays), request.DiscoveredBy, source)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"count":   len(request.Relays),
	})
}
