package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"dnn-node/internal/bitcoin"
	"dnn-node/internal/config"
	"dnn-node/internal/constants"
	"dnn-node/internal/database"
	"dnn-node/internal/policy"
	"dnn-node/internal/reorg"
)

// Syncer handles synchronization with Bitcoin and peer nodes
type Syncer struct {
	config       *config.Config
	db           *database.Database
	bitcoinP2P     *bitcoin.P2PClient
	restValidator  *bitcoin.RestClient  // Independent REST API for cross-validation
	policy         *policy.PolicyEnforcer
	reorgHandler   *reorg.ReorgHandler // Handles blockchain reorganizations

	mu         sync.RWMutex
	peers      map[string]*PeerNode
	syncing    bool
	lastSync   time.Time
	shutdown   chan struct{}
	syncTicker *time.Ticker

	// Relay subscriptions for real-time event updates
	relayConns    map[string]*nostr.Relay // Active relay connections
	relayConnsMu  sync.RWMutex            // Protects relayConns
	subCancelFunc context.CancelFunc      // Cancel function for all subscriptions

	// Rebroadcast dedup cache — tracks event IDs we've already rebroadcasted
	rebroadcastCache   map[string]time.Time // eventID → when rebroadcasted
	rebroadcastCacheMu sync.Mutex

	// Dashboard update callback (optional)
	OnUpdate func(updateType string, data interface{})
}

// PeerNode represents a peer DNN node
type PeerNode struct {
	Pubkey   string    `json:"pubkey"`
	RelayURL string    `json:"relay_url"`
	LastSeen time.Time `json:"last_seen"`
	LastSync time.Time `json:"last_sync"`
	IsActive bool      `json:"is_active"`
}

// rebroadcastFreshness is the maximum age of an event's created_at timestamp
// for it to be eligible for rebroadcasting. Events older than this are assumed
// to already exist on other relays (e.g., during initial sync).
const rebroadcastFreshness = 10 * time.Minute

// rebroadcastCacheTTL is how long we remember that we've rebroadcasted an event.
// This prevents duplicate rebroadcasts within this window.
const rebroadcastCacheTTL = 15 * time.Minute

// New creates a new syncer
func New(cfg *config.Config, db *database.Database) (*Syncer, error) {
	// Create Bitcoin client if configured
	var btcP2P *bitcoin.P2PClient

	if cfg.BitcoinRPC.UseP2P {
		log.Println("Using Bitcoin P2P protocol with REST cross-validation")

		// Create REST client for independent cross-validation of block hashes
		restValidator := bitcoin.NewMempoolSpaceClient()

		// Create P2P client with REST fallback for cross-checks
		btcP2P = bitcoin.NewP2PClient(false, cfg.BitcoinRPC.P2PPeers, restValidator)

		if err := btcP2P.Start(); err != nil {
			log.Printf("Warning: Failed to start Bitcoin P2P client: %v", err)
			btcP2P = nil
		} else {
			btcP2P.SetTxIndexStore(db)
		}

		syncer := &Syncer{
			config:        cfg,
			db:            db,
			bitcoinP2P:    btcP2P,
			restValidator: restValidator,
			policy:        policy.NewPolicyEnforcer(cfg.Network),
			peers:         make(map[string]*PeerNode),
			shutdown:      make(chan struct{}),
			relayConns:    make(map[string]*nostr.Relay),
		}

		// Initialize reorg handler if Bitcoin P2P is available
		if btcP2P != nil {
			syncer.reorgHandler = reorg.NewReorgHandler(db, btcP2P, cfg.Network)
			log.Println("Reorg handler initialized - will check every 144 DNN blocks")
		}

		// Register the anchor fetch hook
		hookManager := database.NewHookManager()
		hookManager.Register(database.NewAnchorFetchHook(syncer.fetchAndStoreReferencedEvents))
		db.SetHookManager(hookManager)

		return syncer, nil
	}

	// Bitcoin sync disabled
	log.Println("Bitcoin sync disabled - enable P2P mode in config.json")
	syncer := &Syncer{
		config:     cfg,
		db:         db,
		policy:     policy.NewPolicyEnforcer(cfg.Network),
		peers:      make(map[string]*PeerNode),
		shutdown:   make(chan struct{}),
		relayConns: make(map[string]*nostr.Relay),
	}

	hookManager := database.NewHookManager()
	hookManager.Register(database.NewAnchorFetchHook(syncer.fetchAndStoreReferencedEvents))
	db.SetHookManager(hookManager)

	return syncer, nil
}

// Start starts the syncer
func (s *Syncer) Start() {
	log.Println("Syncer started")

	// Load peer nodes from config
	s.loadPeers()

	// Start sync ticker
	s.syncTicker = time.NewTicker(time.Duration(s.config.SyncInterval) * time.Second)

	// Initial sync
	go s.performSync()

	// Start persistent relay subscriptions for real-time event updates
	go s.startRelaySubscriptions()

	// Start peer health checks (every 6 hours)
	go s.startPeerHealthChecks()

	// Start auto-announce (monthly)
	go s.startAutoAnnounce()

	// Sync loop
	go func() {
		for {
			select {
			case <-s.syncTicker.C:
				s.performSync()
			case <-s.shutdown:
				return
			}
		}
	}()
}

// Stop stops the syncer
func (s *Syncer) Stop() {
	// Stop relay subscriptions first
	s.stopRelaySubscriptions()

	close(s.shutdown)
	if s.syncTicker != nil {
		s.syncTicker.Stop()
	}
	if s.bitcoinP2P != nil {
		s.bitcoinP2P.Stop()
	}
	log.Println("Syncer stopped")
}

// broadcastUpdate safely calls the OnUpdate callback if set
func (s *Syncer) broadcastUpdate(updateType string, data interface{}) {
	if s.OnUpdate != nil {
		s.OnUpdate(updateType, data)
	}
}

// loadPeers loads peer nodes from configuration and database
func (s *Syncer) loadPeers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load from config
	for _, peerURL := range s.config.PeerNodes {
		// Extract pubkey from peer URL if formatted as pubkey@relay
		// For now, just use the URL as-is
		s.peers[peerURL] = &PeerNode{
			RelayURL: peerURL,
			IsActive: true,
		}
	}
}

// performSync performs synchronization with Bitcoin and peer nodes
func (s *Syncer) performSync() {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return
	}
	s.syncing = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.lastSync = time.Now()
		s.mu.Unlock()
	}()

	log.Println("Starting sync...")

	// Sync with Bitcoin if P2P client is available
	if s.bitcoinP2P != nil {
		if err := s.syncBitcoin(); err != nil {
			log.Printf("Bitcoin sync error: %v", err)
		}
	}

	// Check for pending Bitcoin transactions and query relays for anchor events
	if err := s.SyncPendingTransactions(); err != nil {
		log.Printf("Pending transactions sync error: %v", err)
	}

	// Sync with peer nodes
	if err := s.syncPeers(); err != nil {
		log.Printf("Peer sync error: %v", err)
	}

	// Sync peer awareness marks (fetch each peer's NIP-51 awareness list)
	if s.config.EnableAwareness {
		if err := s.syncPeerAwareness(); err != nil {
			log.Printf("Peer awareness sync error: %v", err)
		}
	}

	log.Println("Sync completed")

	// Broadcast stats update to connected dashboard clients
	qb := database.NewQueryBuilder(s.db)
	if stats, err := qb.GetStats(); err == nil {
		s.broadcastUpdate("stats", stats)
	}
}

// syncBitcoin synchronizes with the Bitcoin blockchain
func (s *Syncer) syncBitcoin() error {
	// Get current block height from Bitcoin P2P
	if s.bitcoinP2P == nil {
		return fmt.Errorf("no Bitcoin P2P client available")
	}

	blockHeight, err := s.bitcoinP2P.GetBlockCount()
	if err != nil {
		return fmt.Errorf("failed to get block count: %w", err)
	}

	log.Printf("Current Bitcoin block height: %d", blockHeight)

	// Get last synced block from database
	qb := database.NewQueryBuilder(s.db)
	lastSyncedStr, err := qb.GetSyncState("last_synced_block")
	if err != nil {
		log.Printf("Failed to get last synced block: %v", err)
	}

	genesisBlock := constants.GetGenesisBlock(s.config.Network)
	lastSyncedBlock := genesisBlock - 1 // Start just before genesis block
	if lastSyncedStr != "" {
		if parsed, err := strconv.ParseInt(lastSyncedStr, 10, 64); err == nil {
			lastSyncedBlock = parsed
		}
	}

	log.Printf("Last synced block: %d", lastSyncedBlock)

	// Process new blocks (limit to batch size to avoid overwhelming the API)
	endBlock := lastSyncedBlock + int64(s.config.BlockBatchSize)
	if endBlock > blockHeight {
		endBlock = blockHeight
	}

	if lastSyncedBlock >= blockHeight {
		log.Printf("Last synced block (%d) >= current height (%d) - waiting for new blocks...", lastSyncedBlock, blockHeight)
		// This can happen if:
		// 1. Block height estimation is inaccurate
		// 2. We're actually caught up
		// Just return without error - next sync will check again
		return nil
	}

	log.Printf("Processing blocks %d to %d", lastSyncedBlock+1, endBlock)

	for blockNum := lastSyncedBlock + 1; blockNum <= endBlock; blockNum++ {

		// Get block hash via P2P
		blockHash, err := s.bitcoinP2P.GetBlockHash(blockNum)
		if err != nil {
			log.Printf("Failed to get block hash for %d: %v (STOPPING to prevent gaps, will retry next sync)", blockNum, err)
			break
		}

		// REST CROSS-VALIDATION: Spot-check P2P hashes against independent REST API
		// Check every 100th block + first block of each batch to catch chain divergence early
		if s.restValidator != nil && (blockNum == lastSyncedBlock+1 || blockNum%100 == 0) {
			restHash, restErr := s.restValidator.GetBlockHash(blockNum)
			if restErr == nil && restHash != blockHash {
				log.Printf("⚠️ CHAIN DIVERGENCE DETECTED at block %d!", blockNum)
				log.Printf("   P2P hash:  %s", blockHash)
				log.Printf("   REST hash: %s", restHash)
				log.Printf("   Clearing headerCache and aborting sync to recover...")
				// Clear the corrupted P2P headerCache to force re-sync from checkpoint
				s.bitcoinP2P.ClearHeaderCache()
				return fmt.Errorf("chain divergence at block %d: P2P=%s REST=%s", blockNum, blockHash[:16], restHash[:16])
			} else if restErr == nil {
				log.Printf("✓ Block %d hash cross-validated against REST API", blockNum)
			}
			// If REST fails, just continue with P2P data (REST is optional safety net)
		}

		// Get block data via P2P with input addresses
		block, err := s.bitcoinP2P.GetBlockWithInputs(blockHash)
		if err != nil {
			log.Printf("Failed to get block %s: %v (STOPPING to prevent gaps, will retry next sync)", blockHash, err)
			// CRITICAL: Stop processing here to prevent skipping this block
			break
		}

		// Calculate DNN block number (Bitcoin block - genesis block)
		dnnBlockNum := blockNum - genesisBlock

		// DNN block number already calculated above

		// Create or update DNN block entry in database with actual block timestamp
		blockTimestamp := block.Time // Extract from Bitcoin block header
		if err := s.db.CreateOrUpdateDNNBlock(dnnBlockNum, blockNum, blockHash, blockTimestamp); err != nil {
			log.Printf("Failed to create DNN block entry: %v", err)
		}

		// Process transactions in the block
		validTxs := s.findValidDNNTransactions(block)

		if len(validTxs) > 0 {
			log.Printf("Found %d valid DNN transactions in block %d (DNN block %d)", len(validTxs), blockNum, dnnBlockNum)

			// Store all Bitcoin transactions in database first
			// validTxs is already sorted by TXID digit sum, so array index = DNN position
			var storedTxs []database.BitcoinTransactionRecord
			for dnnIndex, tx := range validTxs {
				// DNN position is based on sorted order (1-indexed)
				dnnPosition := dnnIndex + 1
				if err := s.db.StoreBitcoinTransaction(tx.TxID, blockNum, dnnBlockNum, dnnPosition, tx.Address, tx.TxIDNS.Sum, tx.TxIDNS.TiePosition, tx.TxIDNS.TieDigit); err != nil {
					log.Printf("Failed to store Bitcoin transaction %s: %v", tx.TxID, err)
					continue
				}

				// Add to list for batch query
				storedTxs = append(storedTxs, database.BitcoinTransactionRecord{
					TransactionID: tx.TxID,
					BitcoinBlock:  blockNum,
					DNNBlock:      dnnBlockNum,
					Position:      dnnPosition,
				})
			}

			// Batch query for all anchor events at once (much faster!)
			if len(storedTxs) > 0 {
				log.Printf("Batch querying relays for %d transactions in block %d...", len(storedTxs), blockNum)
				foundEvents := s.findAnchorEventsForTransactionsBatch(storedTxs)

				// Process results
				for _, tx := range storedTxs {
					if event, found := foundEvents[tx.TransactionID]; found {
						log.Printf("✓ Found anchor event for transaction %s", truncateTxID(tx.TransactionID))

						// Validate anchor event (must use naddr references)
						if err := s.policy.ValidateTagReferences(event); err != nil {
							log.Printf("[SYNC REJECT] Anchor event %s failed validation: %v", event.ID[:8], err)
							continue
						}

						// Store the anchor event
						if err := s.db.StoreAnchorEvent(event, tx.BitcoinBlock, tx.DNNBlock, tx.Position); err != nil {
							log.Printf("Failed to store anchor event: %v", err)
							continue
						}
						// Fetch and store referenced events (name, connection, metadata)
						log.Printf("Fetching referenced events for anchor %s...", event.ID[:8])
						if err := s.fetchAndStoreReferencedEvents(event); err != nil {
							log.Printf("Failed to fetch referenced events: %v", err)
						}
					} else {
						log.Printf("No anchor event found for transaction %s (can be published later)", tx.TransactionID)
					}
				}
			}
		}

		// Update sync state after SUCCESSFULLY processing each block
		// This ensures we don't skip blocks if a future block fails
		if err := qb.UpdateSyncState("last_synced_block", strconv.FormatInt(blockNum, 10)); err != nil {
			log.Printf("Failed to update last synced block: %v", err)
		}
	}

	// Update last sync time
	if err := qb.UpdateSyncState("last_sync_time", time.Now().Format(time.RFC3339)); err != nil {
		log.Printf("Failed to update last sync time: %v", err)
	}

	// Check for blockchain reorganizations (runs every 10 DNN blocks for testing, normally 144)
	if s.reorgHandler != nil {
		// Use last synced block, not current Bitcoin height
		lastSyncedDNNBlock := endBlock - constants.GetGenesisBlock(s.config.Network)
		if err := s.reorgHandler.CheckAndHandleReorg(lastSyncedDNNBlock); err != nil {
			log.Printf("Warning: Reorg check failed: %v", err)
		}
	}

	return nil
}

// ValidDNNTransaction represents a valid DNN Bitcoin transaction
type ValidDNNTransaction struct {
	TxID          string
	Address       string
	TxIDNS        TxIDNSResult // TX ID Number Sum for ordering
	BlockPosition int          // Original position in the Bitcoin block (0-indexed)
}

// findValidDNNTransactions finds transactions that meet DNN criteria
// Returns transactions sorted by TXID digit sum (highest first) with DNN positions assigned
func (s *Syncer) findValidDNNTransactions(block *bitcoin.Block) []ValidDNNTransaction {
	var validTxs []ValidDNNTransaction

	for i := range block.Transactions {
		// Check if transaction is a valid self-transfer
		if addr, ok := s.isValidDNNTransaction(&block.Transactions[i]); ok {
			txid := block.Transactions[i].TxID
			sum := txidDigitSum(txid)
			validTxs = append(validTxs, ValidDNNTransaction{
				TxID:          txid,
				Address:       addr,
				TxIDNS:        TxIDNSResult{Sum: sum, TiePosition: 0, TieDigit: 0},
				BlockPosition: i,
			})
		}
	}

	// Sort by TXID digit sum (highest first) - this determines DNN position order
	// If sums are equal, compare digit-by-digit (higher digit wins)
	sort.Slice(validTxs, func(i, j int) bool {
		if validTxs[i].TxIDNS.Sum != validTxs[j].TxIDNS.Sum {
			return validTxs[i].TxIDNS.Sum > validTxs[j].TxIDNS.Sum
		}
		// Tiebreaker: compare digit by digit
		cmp, pos, iDigit, jDigit := compareTxidsByDigit(validTxs[i].TxID, validTxs[j].TxID)
		if cmp != 0 {
			// Record tiebreaker info for display
			validTxs[i].TxIDNS.TiePosition = pos
			validTxs[i].TxIDNS.TieDigit = iDigit
			validTxs[j].TxIDNS.TiePosition = pos
			validTxs[j].TxIDNS.TieDigit = jDigit
		}
		return cmp > 0
	})

	// Log summary if any valid transactions found
	if len(validTxs) > 0 {
		log.Printf("Found %d valid DNN transaction(s) in block", len(validTxs))
	}

	return validTxs
}

// isValidDNNTransaction checks if a transaction meets DNN criteria
// Returns: (bitcoinAddress, isValid)
// DNN criteria: valid self-transfer (all inputs and outputs have same P2WPKH address)
func (s *Syncer) isValidDNNTransaction(tx *bitcoin.Transaction) (string, bool) {
	txid := tx.TxID

	// Must have at least one input and output
	if len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		return "", false
	}

	// Extract unique addresses from ALL inputs and outputs
	allAddresses := make(map[string]bool)
	inputAddresses := make(map[string]bool)
	outputAddresses := make(map[string]bool)

	// Extract addresses from inputs
	// If ANY input has no address, skip this transaction
	for _, input := range tx.Inputs {
		if input.Address == "" {
			return "", false
		}
		inputAddresses[input.Address] = true
		allAddresses[input.Address] = true
	}

	// Extract addresses from outputs
	// DNN ONLY allows P2WPKH outputs (witness_v0_keyhash / witness_v0_scripthash)
	// Reject ANY other output type: OP_RETURN, nulldata, nonstandard, taproot, legacy, etc.
	// This prevents Ordinals, BRC-20, Runes, and other data embedding transactions
	for _, output := range tx.Outputs {
		scriptType := strings.ToLower(output.ScriptPubKey.Type)

		// ONLY allow P2WPKH outputs - reject everything else
		// btcd returns "witness_v0_keyhash" for P2WPKH (bc1q) addresses
		if scriptType != "witness_v0_keyhash" {
			log.Printf("[DNN-DEBUG] TX %s rejected: output type '%s' != 'witness_v0_keyhash'", txid[:16], scriptType)
			return "", false
		}

		if len(output.ScriptPubKey.Addresses) > 0 {
			for _, addr := range output.ScriptPubKey.Addresses {
				outputAddresses[addr] = true
				allAddresses[addr] = true
			}
		} else if output.ScriptPubKey.Address != "" {
			outputAddresses[output.ScriptPubKey.Address] = true
			allAddresses[output.ScriptPubKey.Address] = true
		}
	}

	// DNN requires exactly ONE unique address across all inputs and outputs (self-transfer)
	if len(allAddresses) != 1 {
		log.Printf("[DNN-DEBUG] TX %s rejected: %d unique addresses (need 1). Inputs: %v, Outputs: %v", txid[:16], len(allAddresses), inputAddresses, outputAddresses)
		return "", false
	}

	// Verify inputs and outputs both have addresses
	if len(inputAddresses) == 0 || len(outputAddresses) == 0 {
		return "", false
	}

	// Get the address
	var address string
	for addr := range outputAddresses {
		address = addr
		break
	}

	// DNN only accepts P2WPKH addresses (bc1q prefix)
	// Reject taproot (bc1p), legacy (1...), P2SH (3...), and other address types
	if !strings.HasPrefix(strings.ToLower(address), "bc1q") {
		log.Printf("[DNN-DEBUG] TX %s rejected: address '%s' doesn't start with 'bc1q'", txid[:16], address)
		return "", false
	}

	return address, true
}

// truncateTxID truncates a transaction ID for logging
func truncateTxID(txid string) string {
	if len(txid) > 8 {
		return txid[:8] + "..."
	}
	return txid
}

// truncateAddress truncates a Bitcoin address for logging
func truncateAddress(addr string) string {
	if len(addr) > 20 {
		return addr[:10] + "..." + addr[len(addr)-6:]
	}
	return addr
}

// txidDigitSum calculates the sum of all hex digits in a txid
// Each hex char (0-9, a-f) is converted to its decimal value (0-15) and summed
func txidDigitSum(txid string) int {
	sum := 0
	for _, c := range strings.ToLower(txid) {
		if c >= '0' && c <= '9' {
			sum += int(c - '0')
		} else if c >= 'a' && c <= 'f' {
			sum += int(c - 'a' + 10)
		}
	}
	return sum
}

// compareTxidsByDigit compares two txids digit-by-digit for tiebreaking
// Returns: positive if a > b, negative if a < b, 0 if equal
// Also returns the position and values of first differing digit
func compareTxidsByDigit(a, b string) (result int, position int, aDigit int, bDigit int) {
	aLower := strings.ToLower(a)
	bLower := strings.ToLower(b)

	minLen := len(aLower)
	if len(bLower) < minLen {
		minLen = len(bLower)
	}

	for i := 0; i < minLen; i++ {
		aVal := hexCharToInt(aLower[i])
		bVal := hexCharToInt(bLower[i])

		if aVal != bVal {
			return aVal - bVal, i + 1, aVal, bVal // 1-indexed position
		}
	}

	// If all compared digits are equal, longer txid wins (shouldn't happen for valid txids)
	return len(a) - len(b), 0, 0, 0
}

// hexCharToInt converts a hex character to its decimal value
func hexCharToInt(c byte) int {
	if c >= '0' && c <= '9' {
		return int(c - '0')
	}
	if c >= 'a' && c <= 'f' {
		return int(c - 'a' + 10)
	}
	return 0
}

// TxIDNSResult holds the digit sum and tiebreaker info for display
type TxIDNSResult struct {
	Sum         int
	TiePosition int // 0 if no tiebreak needed, otherwise 1-indexed position
	TieDigit    int // The digit value at tie position
}

// formatTxIDNS formats the TX ID Number Sum for display
// Returns format like "536" or "536 (1-9)" if tiebreaker was used
func formatTxIDNS(ns TxIDNSResult) string {
	if ns.TiePosition == 0 {
		return strconv.Itoa(ns.Sum)
	}
	return fmt.Sprintf("%d (%d-%d)", ns.Sum, ns.TiePosition, ns.TieDigit)
}

// OLD FUNCTION REMOVED: findAnchorEventForTransaction
// This function has been replaced by findAnchorEventsForTransactionsBatch()
// which provides 30x better performance by querying all transactions concurrently

// findAnchorEventsForTransactionsBatch queries relays for multiple transactions at once (MUCH faster!)
// Returns a map of txID -> anchor event for all found events
func (s *Syncer) findAnchorEventsForTransactionsBatch(transactions []database.BitcoinTransactionRecord) map[string]*nostr.Event {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := make(map[string]*nostr.Event)
	var resultsMu sync.Mutex // Protect concurrent writes to results map

	if len(transactions) == 0 {
		return results
	}

	// Build list of all transaction IDs we're looking for
	txIDs := make([]string, len(transactions))
	txMap := make(map[string]database.BitcoinTransactionRecord) // for quick lookup
	for i, tx := range transactions {
		txIDs[i] = tx.TransactionID
		txMap[tx.TransactionID] = tx
	}

	log.Printf("    Batch querying %d relays for %d transactions...", len(s.config.RelayURLs), len(txIDs))

	// Use WaitGroup to query all relays concurrently
	var wg sync.WaitGroup

	// Query each relay in parallel
	for _, relayURL := range s.config.RelayURLs {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			s.queryRelayForBatch(ctx, url, txMap, &results, &resultsMu, len(txIDs))
		}(relayURL)
	}

	// Wait for all relay queries to complete
	wg.Wait()

	log.Printf("    Batch query complete: Found %d/%d anchor events", len(results), len(txIDs))
	return results
}

// queryRelayForBatch queries a single relay for batch transactions (helper for concurrent queries)
func (s *Syncer) queryRelayForBatch(
	ctx context.Context,
	relayURL string,
	txMap map[string]database.BitcoinTransactionRecord,
	results *map[string]*nostr.Event,
	resultsMu *sync.Mutex,
	totalTxCount int,
) {
	relay := nostr.NewRelay(ctx, relayURL)
	if err := relay.Connect(ctx); err != nil {
		log.Printf("    %s: Connection failed (%v)", relayURL, err)
		return
	}
	defer relay.Close()

	log.Printf("    %s: Connected, querying for %d transactions...", relayURL, totalTxCount)

	// Build list of transaction IDs to search for
	txIDs := make([]string, 0, len(txMap))
	for txID := range txMap {
		txIDs = append(txIDs, txID)
	}

	// Query with #x tag filter for specific transaction IDs
	// This is more efficient than fetching all DNN events
	filter := nostr.Filter{
		Kinds: []int{60600},
		Tags: nostr.TagMap{
			"t": []string{"DNN"},
			"x": txIDs, // Filter by specific transaction IDs
		},
		Limit: 500,
	}

	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		log.Printf("    %s: Query failed (%v)", relayURL, err)

		// Fallback: Try without x filter (some relays may not support it)
		log.Printf("    %s: Trying fallback query without x filter...", relayURL)
		filterFallback := nostr.Filter{
			Kinds: []int{60600},
			Tags: nostr.TagMap{
				"t": []string{"DNN"},
			},
			Limit: 500,
		}
		events, err = relay.QuerySync(ctx, filterFallback)
		if err != nil {
			log.Printf("    %s: Fallback query also failed (%v)", relayURL, err)
			return
		}
	}

	log.Printf("    %s: Received %d events, matching against %d transactions...", relayURL, len(events), totalTxCount)

	// Check each event against ALL our pending transactions
	foundCount := 0
	eventsWithXTag := 0
	for _, event := range events {
		// Extract transaction ID from event tags (x = transaction, NIP-DN format only)
		var eventTxID string
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "x" {
				eventTxID = tag[1]
				break
			}
		}

		if eventTxID == "" {
			continue
		}
		eventsWithXTag++

		// Check if this is one of the transactions we're looking for
		if _, exists := txMap[eventTxID]; exists {
			// Thread-safe check and store - implement "FIRST CLAIM WINS" (older created_at wins)
			resultsMu.Lock()
			if existingEvent, alreadyFound := (*results)[eventTxID]; !alreadyFound {
				// No existing anchor for this tx - store this one
				(*results)[eventTxID] = event
				foundCount++
				log.Printf("    %s: ✓ Found anchor event for transaction %s (created_at=%d)", relayURL, truncateTxID(eventTxID), event.CreatedAt)
			} else {
				// Another anchor already exists for this tx - compare created_at (LATEST WINS)
				if event.CreatedAt > existingEvent.CreatedAt {
					// This anchor is NEWER - it wins (latest wins)
					(*results)[eventTxID] = event
					log.Printf("    %s: ✓ Replaced anchor with newer one for transaction %s (new created_at=%d > old created_at=%d) - LATEST WINS",
						relayURL, truncateTxID(eventTxID), event.CreatedAt, existingEvent.CreatedAt)
				} else if event.CreatedAt == existingEvent.CreatedAt && event.ID > existingEvent.ID {
					// Same created_at - higher event ID wins (deterministic tie-breaker)
					(*results)[eventTxID] = event
					log.Printf("    %s: ✓ Replaced anchor with higher ID for transaction %s (tie-breaker)",
						relayURL, truncateTxID(eventTxID))
				}
				// Otherwise, keep the existing NEWER anchor
			}
			resultsMu.Unlock()
		} else {
		}

	}

	log.Printf("    %s: Total events with x tag: %d, matching our txs: %d", relayURL, eventsWithXTag, foundCount)
	if foundCount > 0 {
		log.Printf("    %s: Found %d/%d matching events", relayURL, foundCount, totalTxCount)
	} else {
		log.Printf("    %s: No matching events found", relayURL)
	}
}

// processTransaction has been replaced by batch processing
// The function findAnchorEventsForTransactionsBatch() is now used instead
// This improves performance by 30x by querying all transactions at once

// SyncPendingTransactions checks for anchor events for Bitcoin transactions
// It checks both:
// 1. NEW unchecked transactions (never been queried on relays)
// 2. Already-checked transactions that still don't have an anchor (for late-published anchors)
// This function loops until all pending transactions are processed.
func (s *Syncer) SyncPendingTransactions() error {
	qb := database.NewQueryBuilder(s.db)
	const batchSize = 200

	totalProcessed := 0
	totalFound := 0
	iteration := 0
	maxIterations := 100 // Safety limit to prevent infinite loops

	for iteration < maxIterations {
		iteration++

		// Get unchecked transactions (never been queried on relays)
		unchecked, err := qb.GetUncheckedBitcoinTransactions(batchSize)
		if err != nil {
			return fmt.Errorf("failed to get unchecked transactions: %w", err)
		}

		// ALSO get already-checked transactions that still don't have an anchor
		// This allows finding late-published anchor events
		checkedWithoutAnchor, err := qb.GetCheckedTransactionsWithoutAnchor(batchSize)
		if err != nil {
			log.Printf("Warning: Failed to get checked transactions without anchor: %v", err)
			checkedWithoutAnchor = nil
		}

		// Combine both lists for batch querying
		allToCheck := append(unchecked, checkedWithoutAnchor...)

		if len(allToCheck) == 0 {
			// No more transactions to check
			break
		}

		log.Printf("=================================================")
		log.Printf("[Batch %d] Checking %d transactions for anchor events (%d new, %d rechecking)...",
			iteration, len(allToCheck), len(unchecked), len(checkedWithoutAnchor))

		// Batch query all transactions at once
		foundEvents := s.findAnchorEventsForTransactionsBatch(allToCheck)

		// Collect unchecked transaction IDs to mark as checked
		var txIDsToMark []string
		for _, tx := range unchecked {
			txIDsToMark = append(txIDsToMark, tx.TransactionID)
		}

		// Process the results
		foundCount := 0
		for i, tx := range allToCheck {
			if event, found := foundEvents[tx.TransactionID]; found {
				foundCount++
				log.Printf("[%d/%d] ✓ Transaction %s: Found anchor event %s",
					i+1, len(allToCheck), truncateTxID(tx.TransactionID), truncateTxID(event.ID))

				// Validate anchor event (must use naddr references)
				if err := s.policy.ValidateTagReferences(event); err != nil {
					log.Printf("[SYNC REJECT] Anchor event %s failed validation: %v", event.ID[:8], err)
					continue
				}

				// Store the anchor event
				genesisBlock := constants.GetGenesisBlock(s.config.Network)
				dnnBlockNum := tx.BitcoinBlock - genesisBlock
				if err := s.db.StoreAnchorEvent(event, tx.BitcoinBlock, dnnBlockNum, tx.Position); err != nil {
					log.Printf("  Failed to store anchor event: %v", err)
					continue
				}
				// Broadcast anchor found to dashboard
				s.broadcastUpdate("anchor_found", map[string]interface{}{
					"dnn_block":     dnnBlockNum,
					"position":      tx.Position,
					"bitcoin_block": tx.BitcoinBlock,
					"event_id":      event.ID,
				})
			}
		}

		// Mark ONLY newly checked transactions as checked
		if len(txIDsToMark) > 0 {
			if err := qb.MarkTransactionsRelayChecked(txIDsToMark); err != nil {
				log.Printf("Warning: Failed to mark transactions as checked: %v", err)
			}
		}

		totalProcessed += len(allToCheck)
		totalFound += foundCount

		log.Printf("[Batch %d] Found %d/%d anchor events", iteration, foundCount, len(allToCheck))

		// If this batch found no events and we're just rechecking (no new unchecked),
		// we can stop early since older transactions are unlikely to have new anchors
		if foundCount == 0 && len(unchecked) == 0 {
			log.Printf("No new unchecked transactions and no anchors found in recheck batch - stopping")
			break
		}
	}

	if totalProcessed > 0 {
		log.Printf("==================================================")
		log.Printf("Sync complete: Processed %d transactions in %d batches, found %d anchors",
			totalProcessed, iteration, totalFound)
		log.Printf("==================================================")
	}

	return nil
}

// syncPeers synchronizes with peer DNN nodes
func (s *Syncer) syncPeers() error {
	s.mu.RLock()
	peers := make([]*PeerNode, 0, len(s.peers))
	for _, peer := range s.peers {
		if peer.IsActive {
			peers = append(peers, peer)
		}
	}
	s.mu.RUnlock()

	for _, peer := range peers {
		if err := s.syncWithPeer(peer); err != nil {
			log.Printf("Failed to sync with peer %s: %v", peer.RelayURL, err)
		}
	}

	return nil
}

// syncWithPeer synchronizes with a specific peer node
func (s *Syncer) syncWithPeer(peer *PeerNode) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Connect to peer relay
	relay := nostr.NewRelay(ctx, peer.RelayURL)
	if err := relay.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer relay.Close()

	// Query for recent DNN events
	sinceTime := time.Now().Add(-24 * time.Hour).Unix()
	since := nostr.Timestamp(sinceTime)
	filter := nostr.Filter{
		Kinds: []int{61600, 62600, 63600, 60600},
		Tags: map[string][]string{
			"t": {"DNN"},
		},
		Since: &since,
	}

	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to query: %w", err)
	}

	// Process received events
	for _, event := range events {
		switch event.Kind {
		case 61600:
			s.db.StoreNameEvent(event)
		case 62600:
			s.db.StoreConnectionEvent(event)
		case 63600:
			s.db.StoreMetadataEvent(event)
		case 60600:
			// For anchor events from peers, we need to verify them
			// TODO: Implement verification and storage
		}
	}

	// Update peer last seen
	s.mu.Lock()
	peer.LastSeen = time.Now()
	peer.LastSync = time.Now()
	s.mu.Unlock()

	log.Printf("Synced %d events from peer %s", len(events), peer.RelayURL)

	return nil
}

// syncPeerAwareness fetches awareness lists from all known peer nodes.
// Each peer node publishes a kind:30000 d:"dnn-awareness" NIP-51 list.
// This function queries relays for those lists and stores marks via AddPeerMark.
func (s *Syncer) syncPeerAwareness() error {
	qb := database.NewQueryBuilder(s.db)

	// Get all active peer pubkeys from the peer_nodes table
	peerPubkeys, err := qb.GetPeerAdminPubkeys()
	if err != nil {
		return fmt.Errorf("failed to get peer pubkeys: %w", err)
	}

	if len(peerPubkeys) == 0 {
		return nil // No peers to sync awareness from
	}

	log.Printf("[AWARENESS] Syncing awareness lists from %d peer nodes...", len(peerPubkeys))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Query relays for all peer awareness lists at once (batch query)
	// kind:30000 with d:"dnn-awareness" from any of the peer pubkeys
	var foundEvents []*nostr.Event

	for _, relayURL := range s.config.RelayURLs {
		relay := nostr.NewRelay(ctx, relayURL)
		if err := relay.Connect(ctx); err != nil {
			continue
		}

		filter := nostr.Filter{
			Authors: peerPubkeys,
			Kinds:   []int{30000},
			Tags:    nostr.TagMap{"d": []string{"dnn-awareness"}},
			Limit:   len(peerPubkeys),
		}

		events, err := relay.QuerySync(ctx, filter)
		relay.Close()

		if err != nil {
			log.Printf("[AWARENESS] Failed to query %s: %v", relayURL, err)
			continue
		}

		// Keep the newest event per pubkey
		for _, ev := range events {
			foundEvents = append(foundEvents, ev)
		}
	}

	// Deduplicate: keep only the newest event per pubkey
	newestByPubkey := make(map[string]*nostr.Event)
	for _, ev := range foundEvents {
		if existing, ok := newestByPubkey[ev.PubKey]; !ok || ev.CreatedAt > existing.CreatedAt {
			newestByPubkey[ev.PubKey] = ev
		}
	}

	// Process each peer's awareness list
	totalSynced := 0
	for peerPubkey, event := range newestByPubkey {
		count := s.parseDNNAwarenessEvent(qb, event, peerPubkey)
		if count > 0 {
			totalSynced += count
			log.Printf("[AWARENESS] Synced %d marks from peer %s", count, peerPubkey[:8])
		}
	}

	if totalSynced > 0 {
		log.Printf("[AWARENESS] Peer awareness sync complete: %d marks from %d peers", totalSynced, len(newestByPubkey))
	}

	return nil
}

// parseDNNAwarenessEvent parses a kind:30000 d:"dnn-awareness" event and stores marks.
// Tag format: ["dnn", "{name.}n{block}.{pos}", "mark", "category", "reason"]
// Returns the number of marks stored.
func (s *Syncer) parseDNNAwarenessEvent(qb *database.QueryBuilder, event *nostr.Event, peerPubkey string) int {
	// Clear existing marks from this peer before re-importing (full sync)
	if err := qb.ClearPeerMarksForPeer(peerPubkey); err != nil {
		log.Printf("[AWARENESS] Failed to clear marks for peer %s: %v", peerPubkey[:8], err)
	}

	syncCount := 0
	for _, tag := range event.Tags {
		if len(tag) < 3 || tag[0] != "dnn" {
			continue
		}

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

		// Store as peer mark
		if err := qb.AddPeerMark(block, pos, name, peerPubkey, mark, category, reason); err == nil {
			syncCount++
		}
	}

	return syncCount
}

// IsSyncing returns whether the syncer is currently syncing
func (s *Syncer) IsSyncing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.syncing
}

// LastSyncTime returns the last sync time
func (s *Syncer) LastSyncTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSync
}

// GetPeers returns the list of peer nodes
func (s *Syncer) GetPeers() []PeerNode {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers := make([]PeerNode, 0, len(s.peers))
	for _, peer := range s.peers {
		peers = append(peers, *peer)
	}

	return peers
}

// SyncSpecificBlock syncs a specific Bitcoin block immediately
func (s *Syncer) SyncSpecificBlock(blockNum int64) error {
	log.Printf("Manual sync requested for block %d", blockNum)

	if s.bitcoinP2P == nil {
		return fmt.Errorf("no Bitcoin P2P client available")
	}

	// Get block hash via P2P
	blockHash, err := s.bitcoinP2P.GetBlockHash(blockNum)
	if err != nil {
		return fmt.Errorf("failed to get block hash for %d: %w", blockNum, err)
	}

	// Get block data via P2P with input addresses
	block, err := s.bitcoinP2P.GetBlockWithInputs(blockHash)
	if err != nil {
		return fmt.Errorf("failed to get block %s: %w", blockHash, err)
	}

	// Calculate DNN block number
	genesisBlock := constants.GetGenesisBlock(s.config.Network)
	dnnBlockNum := blockNum - genesisBlock

	// Create or update DNN block entry with actual block timestamp
	blockTimestamp := block.Time // Extract from Bitcoin block header
	if err := s.db.CreateOrUpdateDNNBlock(dnnBlockNum, blockNum, blockHash, blockTimestamp); err != nil {
		return fmt.Errorf("failed to create DNN block entry: %w", err)
	}

	// Process transactions in the block
	validTxs := s.findValidDNNTransactions(block)

	if len(validTxs) > 0 {
		log.Printf("Found %d valid DNN transactions in block %d (DNN block %d)", len(validTxs), blockNum, dnnBlockNum)

		// Store all Bitcoin transactions in database first
		// validTxs is already sorted by TXID digit sum, so array index = DNN position
		var storedTxs []database.BitcoinTransactionRecord
		for dnnIndex, tx := range validTxs {
			// DNN position is based on sorted order (1-indexed)
			dnnPosition := dnnIndex + 1
			if err := s.db.StoreBitcoinTransaction(tx.TxID, blockNum, dnnBlockNum, dnnPosition, tx.Address, tx.TxIDNS.Sum, tx.TxIDNS.TiePosition, tx.TxIDNS.TieDigit); err != nil {
				log.Printf("Failed to store Bitcoin transaction %s: %v", tx.TxID, err)
				continue
			}

			// Add to list for batch query
			storedTxs = append(storedTxs, database.BitcoinTransactionRecord{
				TransactionID: tx.TxID,
				BitcoinBlock:  blockNum,
				DNNBlock:      dnnBlockNum,
				Position:      dnnPosition,
			})
		}

		// Batch query for all anchor events at once (much faster!)
		if len(storedTxs) > 0 {
			log.Printf("Batch querying relays for %d transactions in block %d...", len(storedTxs), blockNum)
			foundEvents := s.findAnchorEventsForTransactionsBatch(storedTxs)

			// Process results
			for _, tx := range storedTxs {
				if event, found := foundEvents[tx.TransactionID]; found {
					log.Printf("✓ Found anchor event for transaction %s", truncateTxID(tx.TransactionID))

					// Validate anchor event (must use naddr references)
					if err := s.policy.ValidateTagReferences(event); err != nil {
						log.Printf("[SYNC REJECT] Anchor event %s failed validation: %v", event.ID[:8], err)
						continue
					}

					// Store the anchor event
					if err := s.db.StoreAnchorEvent(event, tx.BitcoinBlock, tx.DNNBlock, tx.Position); err != nil {
						log.Printf("Failed to store anchor event: %v", err)
						continue
					}
					// Referenced events are now fetched automatically via the AnchorFetchHook
				} else {
					log.Printf("No anchor event found for transaction %s (can be published later)", tx.TransactionID)
				}
			}
		}
	} else {
		log.Printf("No valid DNN transactions found in block %d", blockNum)
	}

	// Update sync state
	qb := database.NewQueryBuilder(s.db)
	currentLast, _ := qb.GetSyncState("last_synced_block")
	currentLastInt, _ := strconv.ParseInt(currentLast, 10, 64)

	// Only update if this block is higher than current
	if blockNum > currentLastInt {
		if err := qb.UpdateSyncState("last_synced_block", strconv.FormatInt(blockNum, 10)); err != nil {
			log.Printf("Failed to update last synced block: %v", err)
		}
	}

	log.Printf("Manual sync of block %d completed", blockNum)
	return nil
}

// SyncSpecificTransaction syncs a specific Bitcoin transaction by ID
func (s *Syncer) SyncSpecificTransaction(txID string) error {
	log.Printf("Manual transaction sync requested for %s", txID)

	// For transaction lookup, we need to know which block contains it
	// Without REST API, we can only check transactions we've already indexed

	if s.bitcoinP2P == nil {
		return fmt.Errorf("no Bitcoin P2P client available")
	}

	// Check if we know which block contains this transaction
	var tx *bitcoin.Transaction
	var err error

	// Try to find the block containing this transaction from our index
	blockHash, exists := s.bitcoinP2P.GetBlockHashForTransaction(txID)
	if !exists {
		return fmt.Errorf("transaction not in index - sync the block containing this transaction first")
	}

	// Get the block height from our header mapping
	blockHeight, hasHeight := s.bitcoinP2P.GetHeightForBlockHash(blockHash)
	if !hasHeight {
		return fmt.Errorf("block height unknown for hash %s - sync headers first", blockHash[:16])
	}

	// Download the block containing this transaction
	block, err := s.bitcoinP2P.GetBlockWithInputs(blockHash)
	if err != nil {
		return fmt.Errorf("failed to get block %s: %w", blockHash, err)
	}

	// Find the transaction in the block
	for i := range block.Transactions {
		if block.Transactions[i].TxID == txID {
			tx = &block.Transactions[i]
			// Ensure block info is set correctly
			tx.BlockHash = blockHash
			tx.BlockHeight = blockHeight
			tx.Confirmed = true
			break
		}
	}

	if tx == nil {
		return fmt.Errorf("transaction not found in block")
	}

	if err != nil {
		return fmt.Errorf("failed to get transaction: %w", err)
	}

	log.Printf("✓ Transaction %s fetched successfully", truncateTxID(txID))
	log.Printf("  Inputs: %d, Outputs: %d, Fee: %f BTC", len(tx.Inputs), len(tx.Outputs), tx.Fee)

	// Log detailed input/output info for debugging
	log.Printf("  Input addresses:")
	inputAddrs := make(map[string]bool)
	for i, input := range tx.Inputs {
		log.Printf("    [%d] %s", i, input.Address)
		if input.Address != "" {
			inputAddrs[input.Address] = true
		}
	}

	log.Printf("  Output addresses:")
	outputAddrs := make(map[string]bool)
	for i, output := range tx.Outputs {
		addr := output.ScriptPubKey.Address
		if addr == "" && len(output.ScriptPubKey.Addresses) > 0 {
			addr = output.ScriptPubKey.Addresses[0]
		}
		log.Printf("    [%d] %s (type: %s)", i, addr, output.ScriptPubKey.Type)
		if addr != "" {
			outputAddrs[addr] = true
		}
	}

	log.Printf("  Unique input addresses: %d", len(inputAddrs))
	log.Printf("  Unique output addresses: %d", len(outputAddrs))

	// Check if it's a valid DNN transaction
	addr, isValid := s.isValidDNNTransaction(tx)

	if !isValid {
		log.Printf("✗ Transaction does NOT meet DNN criteria")
		return fmt.Errorf("transaction does not meet DNN criteria (see logs for details)")
	}

	log.Printf("✓ Valid DNN self-transfer!")
	log.Printf("  Address: %s", addr)
	log.Printf("  TXID Digit Sum: %d", txidDigitSum(tx.TxID))

	// Check if we have block info
	if tx.BlockHeight == 0 || tx.BlockHash == "" {
		log.Printf("  Warning: No block info in transaction data (might be unconfirmed)")
		return fmt.Errorf("transaction not confirmed yet or missing block data")
	}

	if !tx.Confirmed {
		log.Printf("  Warning: Transaction not confirmed yet")
		return fmt.Errorf("transaction not confirmed in a block yet")
	}

	blockNum := tx.BlockHeight
	genesisBlock := constants.GetGenesisBlock(s.config.Network)
	dnnBlockNum := blockNum - genesisBlock

	log.Printf("  Block: %d (DNN block %d)", blockNum, dnnBlockNum)
	log.Printf("  Finding transaction position in block...")

	// To get accurate position, we need to scan the full block
	// and count valid DNN transactions before this one
	// For now, just use position 1 as placeholder
	position := 1

	log.Printf("  Position: %d (to get accurate position, sync the full block)", position)
	log.Printf("  Note: DNN position is determined by counting only valid self-transfers before this one")

	// For accurate DNN positioning, we'd need to scan all transactions before this one
	// For now, just store it with estimated position

	// Create DNN block entry if it doesn't exist
	// NOTE: For single transaction sync, we don't have the full block, so timestamp = 0
	// This will be updated when the full block is synced
	blockTimestamp := int64(0) // Will be updated on full block sync
	if err := s.db.CreateOrUpdateDNNBlock(dnnBlockNum, blockNum, tx.BlockHash, blockTimestamp); err != nil {
		log.Printf("  Failed to create DNN block: %v", err)
	}

	// Store the transaction (position will be updated when full block is scanned)
	// Note: For unconfirmed single tx, we don't have tie info so pass 0, 0
	if err := s.db.StoreBitcoinTransaction(txID, blockNum, dnnBlockNum, position+1, addr, txidDigitSum(txID), 0, 0); err != nil {
		log.Printf("  Failed to store transaction: %v", err)
		return err
	}

	log.Printf("✓ Transaction stored successfully!")
	log.Printf("  To get accurate DNN position, sync the full block: %d", blockNum)

	return nil
}

// fetchAndStoreReferencedEvents fetches name, connection, and metadata events referenced by an anchor event
func (s *Syncer) fetchAndStoreReferencedEvents(anchorEvent *nostr.Event) error {
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
		return s.fetchEventByDTag(decoded.Identifier, decoded.Pubkey, decoded.Kind)
	}

	// Fetch referenced events
	nameEvent := fetchEventFromNaddr(nameRef, 61600)
	connectionEvent := fetchEventFromNaddr(connectionRef, 62600)
	metadataEvent := fetchEventFromNaddr(metadataRef, 63600)

	// Store events if found
	if nameEvent != nil {
		if err := s.db.StoreNameEvent(nameEvent); err != nil {
			log.Printf("Failed to store name event: %v", err)
		} else {
			log.Printf("✓ Stored name event %s", nameEvent.ID[:8])
		}
	}

	if connectionEvent != nil {
		if err := s.db.StoreConnectionEvent(connectionEvent); err != nil {
			log.Printf("Failed to store connection event: %v", err)
		} else {
			log.Printf("✓ Stored connection event %s", connectionEvent.ID[:8])
		}
	}

	if metadataEvent != nil {
		if err := s.db.StoreMetadataEvent(metadataEvent); err != nil {
			log.Printf("Failed to store metadata event: %v", err)
		} else {
			log.Printf("✓ Stored metadata event %s", metadataEvent.ID[:8])
		}
	}

	return nil
}

// fetchEventByDTag fetches an event by its d-tag from relays
func (s *Syncer) fetchEventByDTag(dTag string, pubkey string, kind int) *nostr.Event {
	// Try local DB first
	events, err := s.db.QueryEvents(nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{kind},
		Tags:    nostr.TagMap{"d": []string{dTag}},
		Limit:   1,
	})
	if err == nil && len(events) > 0 {
		return events[0]
	}

	// If not in DB, try fetching from configured relays IN PARALLEL
	log.Printf("Event with d-tag %s (kind %d) not in local DB, querying %d relays in parallel...", dTag[:min(len(dTag), 16)], kind, len(s.config.RelayURLs))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		event *nostr.Event
		relay string
	}

	resultChan := make(chan result, len(s.config.RelayURLs))

	// Query all relays in parallel
	for _, relayURL := range s.config.RelayURLs {
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
	for i := 0; i < len(s.config.RelayURLs); i++ {
		select {
		case res := <-resultChan:
			if res.event != nil {
				log.Printf("Found event with d-tag %s on relay %s", dTag[:min(len(dTag), 16)], res.relay)
				return res.event
			}
		case <-ctx.Done():
			log.Printf("Timeout querying relays for event with d-tag %s", dTag[:min(len(dTag), 16)])
			return nil
		}
	}

	log.Printf("Event with d-tag %s (kind %d) not found in DB or relays", dTag[:min(len(dTag), 16)], kind)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildSubscriptionFilters creates smart subscription filters based on:
// 1. Pending transaction IDs (for anchor event discovery)
// 2. Known DNN authors (for updates from verified participants)
// 3. Node discovery and propagation events (64600, 65600)
func (s *Syncer) buildSubscriptionFilters() []nostr.Filter {
	var filters []nostr.Filter
	qb := database.NewQueryBuilder(s.db)

	// Note: We no longer filter by specific transaction IDs - the broad anchor filter
	// catches all DNN anchors and handleEventUpdate validates against local transactions

	// Get known DNN authors for updates filter
	knownAuthors, err := qb.GetKnownDNNAuthors()
	if err != nil {
		log.Printf("Failed to get known DNN authors: %v", err)
	}

	// Filter 1: ALL anchor events from last minute (real-time detection)
	// This catches every DNN anchor event; handleEventUpdate validates against our transactions
	since1min := nostr.Timestamp(time.Now().Add(-1 * time.Minute).Unix())
	anchorFilter := nostr.Filter{
		Kinds: []int{60600},
		Tags:  map[string][]string{"t": {"DNN"}},
		Since: &since1min,
	}
	filters = append(filters, anchorFilter)
	log.Printf("Added anchor filter for ALL DNN anchors (last 1 minute)")

	// Filter 2: Updates from known DNN participants (name/connection/metadata events)
	if len(knownAuthors) > 0 {
		updatesFilter := nostr.Filter{
			Kinds:   []int{61600, 62600, 63600}, // Name, Connection, Metadata only
			Authors: knownAuthors,
			Tags:    map[string][]string{"t": {"DNN"}},
		}
		filters = append(filters, updatesFilter)
		log.Printf("Added updates filter for %d known authors", len(knownAuthors))
	}

	// Filter 3: Node discovery events (Kind 64600) - listen for peer announcements
	// Subscribe to events from last 48 hours to catch recent node announcements
	since48h := nostr.Timestamp(time.Now().Add(-48 * time.Hour).Unix())
	nodeDiscoveryFilter := nostr.Filter{
		Kinds: []int{64600},
		Tags:  map[string][]string{"t": {"DNN"}},
		Since: &since48h,
	}
	filters = append(filters, nodeDiscoveryFilter)
	log.Printf("Added node discovery filter (64600) for last 48 hours")

	// Filter 4: Propagation events (Kind 65600) - listen for anchor sync requests
	// Calculate since time based on offline duration or default to 48 hours
	sinceProp := since48h
	lastSyncStr, _ := qb.GetSyncState("last_sync_time")
	if lastSyncStr != "" {
		if lastSync, err := time.Parse(time.RFC3339, lastSyncStr); err == nil {
			offlineDuration := time.Since(lastSync)
			if offlineDuration > 48*time.Hour {
				sinceProp = nostr.Timestamp(lastSync.Unix())
				log.Printf("Offline for %v, extending propagation filter window", offlineDuration)
			}
		}
	}
	propagationFilter := nostr.Filter{
		Kinds: []int{65600},
		Tags:  map[string][]string{"t": {"DNN"}},
		Since: &sinceProp,
	}
	filters = append(filters, propagationFilter)
	log.Printf("Added propagation filter (65600)")

	return filters
}

// startRelaySubscriptions starts persistent WebSocket subscriptions to relays
// for real-time updates to DNN events
func (s *Syncer) startRelaySubscriptions() {
	log.Println("Starting relay subscriptions for real-time event updates...")

	// Create a context that can be cancelled on shutdown
	ctx, cancel := context.WithCancel(context.Background())
	s.subCancelFunc = cancel

	// Subscribe to each relay in the config
	for _, relayURL := range s.config.RelayURLs {
		go s.subscribeToRelay(ctx, relayURL)
	}
}

// subscribeToRelay maintains a persistent subscription to a single relay
func (s *Syncer) subscribeToRelay(ctx context.Context, relayURL string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("Connecting to relay %s for event subscriptions...", relayURL)

		relay := nostr.NewRelay(ctx, relayURL)
		err := relay.Connect(ctx)
		if err != nil {
			log.Printf("Failed to connect to relay %s: %v (will retry in 30s)", relayURL, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				continue
			}
		}

		// Store the connection
		s.relayConnsMu.Lock()
		s.relayConns[relayURL] = relay
		s.relayConnsMu.Unlock()

		log.Printf("Connected to relay %s, subscribing to DNN events...", relayURL)

		// Build smart subscription filters
		filters := s.buildSubscriptionFilters()

		if len(filters) == 0 {
			log.Printf("No filters to subscribe with (no pending transactions or known authors), using basic DNN filter")
			// Fallback to basic DNN-tagged filter if no specific filters available
			filters = []nostr.Filter{
				{
					Kinds: []int{60600, 61600, 62600, 63600, 64600, 65600},
					Tags:  map[string][]string{"t": {"DNN"}},
				},
			}
		}

		log.Printf("Subscribing to relay %s with %d filter(s)", relayURL, len(filters))

		// Create subscription
		sub, err := relay.Subscribe(ctx, filters)
		if err != nil || sub == nil {
			log.Printf("Failed to subscribe to relay %s: %v, retrying in 30s", relayURL, err)
			relay.Close()
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				continue
			}
		}

		log.Printf("Subscribed to relay %s for real-time DNN event updates", relayURL)

		// Process events as they arrive
	eventLoop:
		for {
			select {
			case <-ctx.Done():
				relay.Close()
				return
			case event, ok := <-sub.Events:
				if !ok {
					// Subscription closed, break out of the event loop to reconnect
					log.Printf("Subscription to %s closed, reconnecting...", relayURL)
					break eventLoop
				}
				s.handleEventUpdate(event)
			}
		}

		// Clean up and reconnect
		s.relayConnsMu.Lock()
		delete(s.relayConns, relayURL)
		s.relayConnsMu.Unlock()
		relay.Close()

		log.Printf("Disconnected from %s, reconnecting in 10s...", relayURL)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// handleEventUpdate processes an incoming event update from a relay subscription
func (s *Syncer) handleEventUpdate(event *nostr.Event) {
	if event == nil {
		return
	}

	log.Printf("Received event update: kind=%d id=%s...", event.Kind, event.ID[:8])

	// Check if we already have a newer version
	// For replaceable events, check by pubkey + kind + d-tag
	dTag := ""
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			dTag = tag[1]
			break
		}
	}

	// Query for existing event
	existingEvents, err := s.db.QueryEvents(nostr.Filter{
		Authors: []string{event.PubKey},
		Kinds:   []int{event.Kind},
		Tags:    nostr.TagMap{"d": []string{dTag}},
		Limit:   1,
	})

	if err == nil && len(existingEvents) > 0 {
		existing := existingEvents[0]
		// Only update if the new event is newer
		if event.CreatedAt <= existing.CreatedAt {
			return // We already have this or a newer version
		}
		log.Printf("Updating event: kind=%d d-tag=%s (old: %d, new: %d)",
			event.Kind, dTag[:min(len(dTag), 16)], existing.CreatedAt, event.CreatedAt)
	}

	// Store the event
	switch event.Kind {
	case 60600:
		// Anchor events need special handling - only store if we have the transaction
		var txID string
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "x" {
				txID = tag[1]
				break
			}
		}

		if txID == "" {
			log.Printf("Anchor event %s has no x tag (transaction), skipping", event.ID[:8])
			return
		}

		// Check if we have this transaction in our database
		qb := database.NewQueryBuilder(s.db)
		txRecord, err := qb.GetBitcoinTransactionByID(txID)
		if err != nil || txRecord == nil {
			log.Printf("Anchor event %s references unknown transaction %s, skipping", event.ID[:8], txID[:16])
			return
		}

		// Store the updated anchor event with correct block position data
		if err := s.db.StoreAnchorEvent(event, txRecord.BitcoinBlock, txRecord.DNNBlock, txRecord.Position); err != nil {
			log.Printf("Failed to store anchor event update: %v", err)
		} else {
			log.Printf("✓ Updated anchor event %s for transaction %s", event.ID[:8], txID[:16])
			// Fetch updated referenced events
			s.fetchAndStoreReferencedEvents(event)
		}
	case 61600:
		if err := s.db.StoreNameEvent(event); err != nil {
			log.Printf("Failed to store name event update: %v", err)
		} else {
			log.Printf("✓ Updated name event %s", event.ID[:8])
		}
	case 62600:
		if err := s.db.StoreConnectionEvent(event); err != nil {
			log.Printf("Failed to store connection event update: %v", err)
		} else {
			log.Printf("✓ Updated connection event %s", event.ID[:8])
		}
	case 63600:
		if err := s.db.StoreMetadataEvent(event); err != nil {
			log.Printf("Failed to store metadata event update: %v", err)
		} else {
			log.Printf("✓ Updated metadata event %s", event.ID[:8])
		}
	case 64600:
		// Node discovery event - store peer node
		s.handleNodeDiscoveryEvent(event)
	case 65600:
		// Propagation event - verify and update anchor
		s.handlePropagationEvent(event)
	}

	// Rebroadcast fresh events to all known relays
	// Only rebroadcast if the event was created recently (prevents sync storms)
	eventTime := time.Unix(int64(event.CreatedAt), 0)
	if time.Since(eventTime) < rebroadcastFreshness {
		go s.rebroadcastEvent(event)
	}
}

// stopRelaySubscriptions stops all relay subscriptions
func (s *Syncer) stopRelaySubscriptions() {
	log.Println("Stopping relay subscriptions...")

	if s.subCancelFunc != nil {
		s.subCancelFunc()
	}

	s.relayConnsMu.Lock()
	for url, relay := range s.relayConns {
		relay.Close()
		delete(s.relayConns, url)
	}
	s.relayConnsMu.Unlock()

	log.Println("Relay subscriptions stopped")
}

// HandleNodeDiscoveryFromLocal is the exported wrapper for handleNodeDiscoveryEvent,
// called when a 64600 event is posted via the /dnn/store-local-event API.
func (s *Syncer) HandleNodeDiscoveryFromLocal(event *nostr.Event) {
	s.handleNodeDiscoveryEvent(event)
}

// handleNodeDiscoveryEvent processes Kind 64600 node discovery events
func (s *Syncer) handleNodeDiscoveryEvent(event *nostr.Event) {
	log.Printf("Processing node discovery event %s from %s", event.ID[:8], event.PubKey[:8])

	// Parse content JSON
	var content struct {
		DNSAddresses []string `json:"dns_addresses"`
		Tor          []string `json:"tor"`
		Relays       []string `json:"relays"`
	}

	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		log.Printf("Failed to parse node discovery content: %v", err)
		return
	}

	// Collect all addresses (DNS + Tor)
	var addresses []string
	addresses = append(addresses, content.DNSAddresses...)
	addresses = append(addresses, content.Tor...)

	if len(addresses) == 0 {
		log.Printf("Node discovery event %s has no addresses, ignoring", event.ID[:8])
		return
	}

	qb := database.NewQueryBuilder(s.db)

	// Build a set of our own addresses to skip self-discovery
	ownAddresses := make(map[string]bool)
	for _, addr := range s.config.AnnounceAddresses {
		ownAddresses[strings.ToLower(addr)] = true
	}

	for _, address := range addresses {
		// Skip our own addresses
		if ownAddresses[strings.ToLower(address)] {
			continue
		}

		// Check if address already exists in DB
		exists, err := qb.PeerAddressExists(address)
		if err != nil {
			log.Printf("Failed to check peer address %s: %v", address, err)
			continue
		}
		if exists {
			continue // Already tracked
		}

		// Verify the endpoint is a real DNN node (3 attempts, 10s timeout)
		log.Printf("🔍 Verifying new peer endpoint: %s", address)
		nodeNpub, nodePubkey, adminPubkey, err := s.verifyNodeEndpoint(address)
		if err != nil {
			log.Printf("❌ Peer verification failed for %s: %v", address, err)
			continue
		}

		// Store verified peer
		peerNode := &database.PeerNode{
			Address:     address,
			NodeNpub:    nodeNpub,
			NodePubkey:  nodePubkey,
			AdminPubkey: adminPubkey,
			AnnouncedBy: event.PubKey,
		}

		if err := qb.StorePeerNode(peerNode); err != nil {
			log.Printf("Failed to store peer node %s: %v", address, err)
		} else {
			log.Printf("✓ Verified and stored peer node %s (npub: %s)", address, nodeNpub)
		}
	}

	// Also store peer's relays to discovered_relays table
	if len(content.Relays) > 0 {
		hardcodedSet := make(map[string]bool)
		for _, r := range s.config.RelayURLs {
			hardcodedSet[strings.ToLower(r)] = true
		}

		var relaysToStore []string
		for _, relay := range content.Relays {
			if !hardcodedSet[strings.ToLower(relay)] {
				relaysToStore = append(relaysToStore, relay)
			}
		}

		if len(relaysToStore) > 0 {
			if err := qb.StoreDiscoveredRelays(relaysToStore, "peer", event.PubKey); err != nil {
				log.Printf("Failed to store peer relays: %v", err)
			}
		}
	}
}

// verifyNodeEndpoint pings a node's /dnn/node-info endpoint to verify it's a real DNN node.
// Returns the node's npub, pubkey, admin_pubkey, or an error. Retries up to 3 times with 10s timeouts.
// Also verifies genesis block match and performs a random data spot-check.
func (s *Syncer) verifyNodeEndpoint(address string) (npub string, pubkey string, adminPubkey string, err error) {
	client := &http.Client{Timeout: 10 * time.Second}

	for attempt := 1; attempt <= 3; attempt++ {
		url := strings.TrimRight(address, "/") + "/dnn/node-info"
		resp, reqErr := client.Get(url)
		if reqErr != nil {
			err = reqErr
			if attempt < 3 {
				time.Sleep(2 * time.Second)
			}
			continue
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			err = readErr
			continue
		}

		if resp.StatusCode != 200 {
			err = fmt.Errorf("HTTP %d from %s", resp.StatusCode, address)
			continue
		}

		var info struct {
			NodeNpub     string `json:"node_npub"`
			NodePubkey   string `json:"node_pubkey"`
			AdminNpub    string `json:"admin_npub"`
			GenesisBlock int64  `json:"genesis_block"`
			Network      string `json:"network"`
		}
		if jsonErr := json.Unmarshal(body, &info); jsonErr != nil {
			err = fmt.Errorf("invalid JSON from %s: %w", address, jsonErr)
			continue
		}

		if info.NodeNpub == "" && info.NodePubkey == "" {
			err = fmt.Errorf("no node identity in response from %s", address)
			continue
		}

		// Check 1: Genesis block must be present and must match ours
		ourGenesis := constants.GetGenesisBlock(s.config.Network)
		if info.GenesisBlock == 0 {
			return "", "", "", fmt.Errorf("peer %s does not report genesis_block (incompatible node)", address)
		}
		if info.GenesisBlock != ourGenesis {
			return "", "", "", fmt.Errorf("genesis mismatch with peer %s: peer=%d (network=%s), ours=%d (network=%s)",
				address, info.GenesisBlock, info.Network, ourGenesis, s.config.Network)
		}
		log.Printf("✓ Peer %s genesis matches: %d (network=%s)", address, info.GenesisBlock, info.Network)

		// Check 2: Random data spot-check — verify peer has the same transaction data
		if spotErr := s.spotCheckPeerData(address, client); spotErr != nil {
			return "", "", "", fmt.Errorf("data spot-check failed for peer %s: %w", address, spotErr)
		}

		// Convert admin npub to hex pubkey if present
		var adminHex string
		if info.AdminNpub != "" {
			if _, v, decErr := nip19.Decode(info.AdminNpub); decErr == nil {
				adminHex = v.(string)
			}
		}

		return info.NodeNpub, info.NodePubkey, adminHex, nil
	}

	return "", "", "", fmt.Errorf("verification failed after 3 attempts for %s: %w", address, err)
}

// spotCheckPeerData picks a random transaction from our database and verifies the peer has
// the same data. This detects malicious nodes that may fake identity but serve different data.
func (s *Syncer) spotCheckPeerData(address string, client *http.Client) error {
	qb := database.NewQueryBuilder(s.db)

	// Get a random transaction from our database
	ourTx, err := qb.GetRandomBitcoinTransaction()
	if err != nil {
		// No transactions in our database yet — skip spot-check (new node)
		log.Printf("⚠ Skipping spot-check for %s (no local transactions yet)", address)
		return nil
	}

	// Query the peer's API for this transaction
	searchURL := fmt.Sprintf("%s/dnn/anchors?search=%s&limit=1",
		strings.TrimRight(address, "/"), ourTx.TransactionID)

	resp, err := client.Get(searchURL)
	if err != nil {
		return fmt.Errorf("failed to query peer: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("peer returned HTTP %d for spot-check", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read spot-check response: %w", err)
	}

	// Parse the paginated response
	var peerResult struct {
		Items []struct {
			TransactionID string `json:"transaction_id"`
			BitcoinBlock  int64  `json:"bitcoin_block"`
			Address       string `json:"bitcoin_address"`
			DNNBlock      int64  `json:"dnn_block"`
			Position      int    `json:"position"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &peerResult); err != nil {
		return fmt.Errorf("invalid spot-check response: %w", err)
	}

	// Peer should have this transaction
	if peerResult.Total == 0 || len(peerResult.Items) == 0 {
		// Peer might not have synced this block yet — not necessarily malicious
		log.Printf("⚠ Spot-check: peer %s doesn't have tx %s yet (may still be syncing)", address, ourTx.TransactionID[:16])
		return nil
	}

	// Verify the data matches
	peerTx := peerResult.Items[0]
	if peerTx.TransactionID != ourTx.TransactionID {
		return fmt.Errorf("spot-check txid mismatch: expected %s, got %s", ourTx.TransactionID, peerTx.TransactionID)
	}
	if peerTx.BitcoinBlock != ourTx.BitcoinBlock {
		return fmt.Errorf("spot-check bitcoin_block mismatch for tx %s: ours=%d, peer=%d",
			ourTx.TransactionID[:16], ourTx.BitcoinBlock, peerTx.BitcoinBlock)
	}
	if peerTx.Address != ourTx.BitcoinAddress {
		return fmt.Errorf("spot-check address mismatch for tx %s: ours=%s, peer=%s",
			ourTx.TransactionID[:16], ourTx.BitcoinAddress, peerTx.Address)
	}

	log.Printf("✓ Spot-check passed for peer %s (tx %s matches)", address, ourTx.TransactionID[:16])
	return nil
}

// startPeerHealthChecks runs periodic health checks on all stored peers.
// Every 6 hours, pings each peer. After 4 consecutive failures (24h), removes the peer.
func (s *Syncer) startPeerHealthChecks() {
	// Wait 5 minutes after startup before first health check
	select {
	case <-time.After(5 * time.Minute):
	case <-s.shutdown:
		return
	}

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	// Run first check
	s.runPeerHealthChecks()

	for {
		select {
		case <-ticker.C:
			s.runPeerHealthChecks()
		case <-s.shutdown:
			return
		}
	}
}

// runPeerHealthChecks performs a single round of health checks on all peers
func (s *Syncer) runPeerHealthChecks() {
	qb := database.NewQueryBuilder(s.db)

	peers, err := qb.GetAllPeerAddresses()
	if err != nil {
		log.Printf("[HEALTH] Failed to get peers for health check: %v", err)
		return
	}

	if len(peers) == 0 {
		return
	}

	log.Printf("[HEALTH] Running health checks on %d peers", len(peers))

	for _, peer := range peers {
		nodeNpub, nodePubkey, adminPubkey, verifyErr := s.verifyNodeEndpoint(peer.Address)
		if verifyErr != nil {
			// Failed - increment fail count
			if err := qb.UpdatePeerHealthCheck(peer.Address, false, "", "", ""); err != nil {
				log.Printf("[HEALTH] Failed to update health check for %s: %v", peer.Address, err)
			}
			log.Printf("[HEALTH] ❌ %s failed (fail_count will be %d)", peer.Address, peer.FailCount+1)
		} else {
			// Success - reset fail count, update identity if changed
			if err := qb.UpdatePeerHealthCheck(peer.Address, true, nodeNpub, nodePubkey, adminPubkey); err != nil {
				log.Printf("[HEALTH] Failed to update health check for %s: %v", peer.Address, err)
			}
			if peer.NodeNpub != nodeNpub && peer.NodeNpub != "" {
				log.Printf("[HEALTH] ✓ %s alive (npub changed: %s → %s)", peer.Address, peer.NodeNpub[:16], nodeNpub[:16])
			}
		}
	}

	// Remove dead peers (4+ consecutive failures = 24h of being dead)
	removed, err := qb.RemoveDeadPeers(4)
	if err != nil {
		log.Printf("[HEALTH] Failed to remove dead peers: %v", err)
	} else if removed > 0 {
		log.Printf("[HEALTH] Removed %d dead peers (4+ consecutive failures)", removed)
	}
}

// startAutoAnnounce starts the automatic node announcement goroutine.
// Announces on startup if never announced or if 30+ days have passed, then monthly.
func (s *Syncer) startAutoAnnounce() {
	if len(s.config.AnnounceAddresses) == 0 {
		log.Println("[ANNOUNCE] No announce_addresses configured, auto-announce disabled")
		return
	}

	// Wait 30 seconds for relays to connect
	select {
	case <-time.After(30 * time.Second):
	case <-s.shutdown:
		return
	}

	qb := database.NewQueryBuilder(s.db)

	// Check last announce time
	shouldAnnounce := false
	lastAnnounceStr, err := qb.GetSyncState("last_announce_time")
	if err != nil || lastAnnounceStr == "" {
		// Never announced
		log.Println("[ANNOUNCE] First-time announcement")
		shouldAnnounce = true
	} else {
		lastAnnounce, parseErr := strconv.ParseInt(lastAnnounceStr, 10, 64)
		if parseErr != nil {
			shouldAnnounce = true
		} else {
			daysSince := time.Since(time.Unix(lastAnnounce, 0)).Hours() / 24
			if daysSince >= 30 {
				log.Printf("[ANNOUNCE] Last announced %.0f days ago, re-announcing", daysSince)
				shouldAnnounce = true
			} else {
				log.Printf("[ANNOUNCE] Last announced %.0f days ago, next in %.0f days",
					daysSince, 30-daysSince)
			}
		}
	}

	if shouldAnnounce {
		s.publishNodeAnnouncement()
	}

	// Monthly ticker
	ticker := time.NewTicker(30 * 24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.publishNodeAnnouncement()
		case <-s.shutdown:
			return
		}
	}
}

// publishNodeAnnouncement creates and publishes a Kind 64600 node discovery event
// signed with the node's own private key, using a static d-tag for NIP-33 replaceability.
func (s *Syncer) publishNodeAnnouncement() {
	if len(s.config.AnnounceAddresses) == 0 {
		return
	}

	log.Printf("[ANNOUNCE] Publishing node announcement with %d addresses", len(s.config.AnnounceAddresses))

	// Build content
	content := map[string]interface{}{
		"dns_addresses": s.config.AnnounceAddresses,
		"relays":        s.config.RelayURLs,
	}

	contentJSON, err := json.Marshal(content)
	if err != nil {
		log.Printf("[ANNOUNCE] Failed to marshal content: %v", err)
		return
	}

	// Build event with static d-tag (NIP-33 addressable replaceable)
	event := nostr.Event{
		Kind:      64600,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"d", "node-discovery"},
			{"t", "DNN"},
		},
		Content: string(contentJSON),
	}

	// Sign with node's private key
	if err := event.Sign(s.config.NodePrivateKey); err != nil {
		log.Printf("[ANNOUNCE] Failed to sign event: %v", err)
		return
	}

	log.Printf("[ANNOUNCE] Broadcasting event %s (pubkey: %s)", event.ID[:8], event.PubKey[:8])

	// Publish to all connected relays
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	successCount := 0
	s.relayConnsMu.RLock()
	relays := make(map[string]*nostr.Relay)
	for url, r := range s.relayConns {
		relays[url] = r
	}
	s.relayConnsMu.RUnlock()

	for url, relay := range relays {
		if err := relay.Publish(ctx, event); err != nil {
			log.Printf("[ANNOUNCE] Failed to publish to %s: %v", url, err)
		} else {
			successCount++
		}
	}

	// Also try connecting to any relays we're not currently connected to
	for _, url := range s.config.RelayURLs {
		if _, exists := relays[url]; exists {
			continue // Already published
		}
		relay, err := nostr.RelayConnect(ctx, url)
		if err != nil {
			continue
		}
		if err := relay.Publish(ctx, event); err == nil {
			successCount++
		}
		relay.Close()
	}

	log.Printf("[ANNOUNCE] ✓ Published to %d relays", successCount)

	// Update last announce time
	qb := database.NewQueryBuilder(s.db)
	_ = qb.UpdateSyncState("last_announce_time", strconv.FormatInt(time.Now().Unix(), 10))
}

// handlePropagationEvent processes Kind 65600 propagation events
func (s *Syncer) handlePropagationEvent(event *nostr.Event) {
	log.Printf("Processing propagation event %s from %s", event.ID[:8], event.PubKey[:8])

	// Parse content JSON
	var content struct {
		AnchorNaddr string `json:"anchor_naddr"`
		DNNID       string `json:"dnn_id"`
	}

	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		log.Printf("Failed to parse propagation content: %v", err)
		return
	}

	if content.AnchorNaddr == "" || content.DNNID == "" {
		log.Printf("Propagation event %s missing required fields", event.ID[:8])
		return
	}

	// Decode the anchor naddr to get coordinates
	prefix, anchorData, err := nip19.Decode(content.AnchorNaddr)
	if err != nil || prefix != "naddr" {
		log.Printf("Failed to decode anchor naddr: %v", err)
		return
	}

	entityPointer, ok := anchorData.(nostr.EntityPointer)
	if !ok {
		log.Printf("Invalid naddr entity pointer")
		return
	}

	// Verification Rule 1: Event author must own the DNN ID
	// (For now, we just check that the anchor event pubkey matches the propagation event pubkey)
	if entityPointer.PublicKey != event.PubKey {
		log.Printf("[PROPAGATION] Rejected: Author %s doesn't match anchor owner %s",
			event.PubKey[:8], entityPointer.PublicKey[:8])
		return
	}

	log.Printf("✓ Propagation event %s verified, fetching anchor from relays...", event.ID[:8])

	// Fetch the anchor event from relays
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var anchorEvent *nostr.Event
	for _, relayURL := range s.config.RelayURLs {
		relay := nostr.NewRelay(ctx, relayURL)
		if err := relay.Connect(ctx); err != nil {
			continue
		}
		defer relay.Close()

		filter := nostr.Filter{
			Kinds:   []int{60600},
			Authors: []string{entityPointer.PublicKey},
			Tags:    nostr.TagMap{"d": []string{entityPointer.Identifier}},
			Limit:   1,
		}

		events, err := relay.QuerySync(ctx, filter)
		if err == nil && len(events) > 0 {
			anchorEvent = events[0]
			break
		}
	}

	if anchorEvent == nil {
		log.Printf("Could not fetch anchor event for propagation %s", event.ID[:8])
		return
	}

	// Verification Rule 3: Validate the anchor event
	if err := s.policy.ValidateTagReferences(anchorEvent); err != nil {
		log.Printf("[PROPAGATION] Anchor validation failed: %v", err)
		return
	}

	// Extract transaction ID to get block info (use 'x' tag per NIP-DN)
	var txID string
	for _, tag := range anchorEvent.Tags {
		if len(tag) >= 2 && tag[0] == "x" {
			txID = tag[1]
			break
		}
	}

	if txID == "" {
		log.Printf("Propagation anchor has no transaction tag")
		return
	}

	// Check if we have this transaction
	qb := database.NewQueryBuilder(s.db)
	txRecord, err := qb.GetBitcoinTransactionByID(txID)
	if err != nil || txRecord == nil {
		log.Printf("Propagation anchor references unknown transaction %s", txID[:16])
		return
	}

	// Store the anchor event
	if err := s.db.StoreAnchorEvent(anchorEvent, txRecord.BitcoinBlock, txRecord.DNNBlock, txRecord.Position); err != nil {
		log.Printf("Failed to store propagated anchor: %v", err)
		return
	}

	log.Printf("✓ Propagation complete: Updated anchor for DNN ID %s", content.DNNID)
	// Note: rebroadcasting is handled by handleEventUpdate after this function returns
}

// RebroadcastEvent is the exported wrapper for rebroadcastEvent.
// It is called by the relay handler when a client submits a valid event.
// It applies the same freshness and dedup checks as the syncer path.
func (s *Syncer) RebroadcastEvent(event *nostr.Event) {
	if event == nil {
		return
	}
	// Apply freshness check
	eventTime := time.Unix(int64(event.CreatedAt), 0)
	if time.Since(eventTime) > rebroadcastFreshness {
		return
	}
	go s.rebroadcastEvent(event)
}

// rebroadcastEvent publishes an event to all known relays:
// 1. Connected subscription relays (already open WebSocket connections)
// 2. Config relays (from config.RelayURLs)
// 3. Discovered peer relays (from discovered_relays DB table)
// It deduplicates relay URLs and skips events already rebroadcasted.
func (s *Syncer) rebroadcastEvent(event *nostr.Event) {
	// Check dedup cache
	s.rebroadcastCacheMu.Lock()
	if s.rebroadcastCache == nil {
		s.rebroadcastCache = make(map[string]time.Time)
	}
	if lastSeen, exists := s.rebroadcastCache[event.ID]; exists {
		if time.Since(lastSeen) < rebroadcastCacheTTL {
			s.rebroadcastCacheMu.Unlock()
			return // Already rebroadcasted recently
		}
	}
	s.rebroadcastCache[event.ID] = time.Now()
	s.rebroadcastCacheMu.Unlock()

	// Build deduplicated set of relay URLs
	relaySet := make(map[string]bool)

	// 1. Config relays
	for _, url := range s.config.RelayURLs {
		relaySet[strings.ToLower(url)] = true
	}

	// 2. Discovered peer relays from DB
	qb := database.NewQueryBuilder(s.db)
	discoveredRelays, err := qb.GetAllDiscoveredRelays()
	if err == nil {
		for _, url := range discoveredRelays {
			relaySet[strings.ToLower(url)] = true
		}
	}

	if len(relaySet) == 0 {
		return
	}

	log.Printf("[REBROADCAST] Broadcasting event %s (kind %d) to %d relays", event.ID[:8], event.Kind, len(relaySet))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	successCount := 0
	var successMu sync.Mutex

	for relayURL := range relaySet {
		// Check if we already have an open connection
		s.relayConnsMu.RLock()
		existingRelay, hasConn := s.relayConns[relayURL]
		s.relayConnsMu.RUnlock()

		if hasConn {
			// Use existing connection
			wg.Add(1)
			go func(r *nostr.Relay, url string) {
				defer wg.Done()
				if err := r.Publish(ctx, *event); err != nil {
					log.Printf("[REBROADCAST] Failed to publish to %s (existing conn): %v", url, err)
				} else {
					successMu.Lock()
					successCount++
					successMu.Unlock()
				}
			}(existingRelay, relayURL)
		} else {
			// Open a short-lived connection
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				relay := nostr.NewRelay(ctx, url)
				if err := relay.Connect(ctx); err != nil {
					return // Silently skip unreachable relays
				}
				defer relay.Close()

				if err := relay.Publish(ctx, *event); err != nil {
					log.Printf("[REBROADCAST] Failed to publish to %s: %v", url, err)
				} else {
					successMu.Lock()
					successCount++
					successMu.Unlock()
				}
			}(relayURL)
		}
	}

	wg.Wait()
	log.Printf("[REBROADCAST] Event %s published to %d/%d relays", event.ID[:8], successCount, len(relaySet))

	// Periodic cache cleanup (every ~100 rebroadcasts)
	s.cleanupRebroadcastCache()
}

// cleanupRebroadcastCache removes expired entries from the dedup cache.
func (s *Syncer) cleanupRebroadcastCache() {
	s.rebroadcastCacheMu.Lock()
	defer s.rebroadcastCacheMu.Unlock()

	now := time.Now()
	for id, seen := range s.rebroadcastCache {
		if now.Sub(seen) > rebroadcastCacheTTL {
			delete(s.rebroadcastCache, id)
		}
	}
}
