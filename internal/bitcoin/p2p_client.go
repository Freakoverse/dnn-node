package bitcoin

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/peer"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// PeerStats tracks performance metrics for a peer
type PeerStats struct {
	Address             string
	SuccessCount        int
	FailureCount        int
	TotalResponseTime   time.Duration
	AverageResponseTime time.Duration
	LastSuccess         time.Time
	LastFailure         time.Time
}

// TxIndexStore interface for persistent tx-to-block index storage
// Allows the P2P client to persist transaction index to database
type TxIndexStore interface {
	StoreTxBlockIndexBatch(txids []string, blockHash string) error
	GetBlockHashForTx(txid string) (string, error)
}

// P2PClient connects to Bitcoin nodes via P2P protocol
type P2PClient struct {
	network           *chaincfg.Params
	peers             []*peer.Peer
	peerAddrs         []string
	discoveredPeers   []string // Pool of discovered peers
	blockCache        map[string]*Block
	headerCache       map[int64]string  // height -> hash
	blockHashToHeight map[string]int64  // hash -> height (reverse lookup)
	txToBlockHash     map[string]string // txid -> block hash (in-memory cache)
	txIndexStore      TxIndexStore      // Persistent storage for tx index (optional)
	mu                sync.RWMutex
	blockChan         chan *wire.MsgBlock
	headersChan       chan *wire.MsgHeaders
	txChan            chan *wire.MsgTx
	currentHeight     int64
	heightCacheTime   time.Time                      // When currentHeight was last updated
	RestFallback      *RestClient                    // Fallback for height/hash lookups (exported for syncer access)
	pendingBlocks     map[string]chan *wire.MsgBlock // hash -> response channel
	pendingTxs        map[string]chan *wire.MsgTx    // txid -> response channel
	nextPeerIndex     int                            // Index for round-robin peer selection
	peerFailures      map[string]int                 // Track peer timeout/failure counts
	peerLastFail      map[string]time.Time           // Last failure time for backoff
	peerStats         map[string]*PeerStats          // Performance metrics for each peer
}

// NewP2PClient creates a new Bitcoin P2P client
// restFallback: optional REST client for height/hash lookups (can be nil)
func NewP2PClient(testnet bool, peerAddrs []string, restFallback *RestClient) *P2PClient {
	network := &chaincfg.MainNetParams
	if testnet {
		network = &chaincfg.TestNet3Params
	}

	if len(peerAddrs) == 0 {
		// Bitcoin DNS seeds - these resolve to many peers
		peerAddrs = []string{
			"seed.bitcoin.sipa.be:8333",
			"dnsseed.bluematt.me:8333",
			"dnsseed.bitcoin.dashjr.org:8333",
			"seed.bitcoinstats.com:8333",
			"seed.bitcoin.jonasschnelli.ch:8333",
			"seed.btc.petertodd.org:8333",
			"seed.bitcoin.sprovoost.nl:8333",
			"dnsseed.emzy.de:8333",
		}
	}

	return &P2PClient{
		network:           network,
		peerAddrs:         peerAddrs,
		discoveredPeers:   []string{},
		blockCache:        make(map[string]*Block),
		headerCache:       make(map[int64]string),
		blockHashToHeight: make(map[string]int64),
		txToBlockHash:     make(map[string]string),
		blockChan:         make(chan *wire.MsgBlock, 10),
		headersChan:       make(chan *wire.MsgHeaders, 10),
		txChan:            make(chan *wire.MsgTx, 10),
		currentHeight:     0,
		RestFallback:      restFallback,
		pendingBlocks:     make(map[string]chan *wire.MsgBlock),
		pendingTxs:        make(map[string]chan *wire.MsgTx),
		nextPeerIndex:     0,
		peerFailures:      make(map[string]int),
		peerLastFail:      make(map[string]time.Time),
		peerStats:         make(map[string]*PeerStats),
	}
}

// SetTxIndexStore sets the persistent storage for tx-to-block index
// This should be called after NewP2PClient but before starting sync
func (p *P2PClient) SetTxIndexStore(store TxIndexStore) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.txIndexStore = store
	log.Println("P2P client: TX index store configured for persistent storage")
}

// Start connects to Bitcoin peers
func (p *P2PClient) Start() error {
	log.Println("Starting Bitcoin P2P client...")

	// Discover peers from DNS seeds
	p.discoveredPeers = p.discoverPeersFromDNS()
	log.Printf("Discovered %d potential peers from DNS seeds", len(p.discoveredPeers))

	// Try to connect to initial peers
	connected := 0
	maxInitialPeers := 8 // Connect to more peers initially

	for _, addr := range p.discoveredPeers {
		if connected >= maxInitialPeers {
			break
		}

		if err := p.connectToPeer(addr); err != nil {
			log.Printf("Failed to connect to peer %s: %v", addr, err)
			continue
		}

		connected++
		log.Printf("Connected to Bitcoin peer: %s", addr)
	}

	if connected == 0 {
		return fmt.Errorf("failed to connect to any Bitcoin peers")
	}

	log.Printf("Successfully connected to %d Bitcoin peers (pool of %d available)", connected, len(p.discoveredPeers))
	return nil
}

// discoverPeersFromDNS resolves DNS seeds to get a list of Bitcoin node IPs
func (p *P2PClient) discoverPeersFromDNS() []string {
	var allPeers []string

	for _, seed := range p.peerAddrs {
		// Extract hostname (remove port if present)
		host := seed
		if idx := strings.Index(seed, ":"); idx > 0 {
			host = seed[:idx]
		}

		// Resolve DNS
		addrs, err := net.LookupHost(host)
		if err != nil {
			log.Printf("Failed to resolve DNS seed %s: %v", host, err)
			// Fallback to using the seed directly
			allPeers = append(allPeers, seed)
			continue
		}

		// Add all resolved IPs with port 8333
		for _, ip := range addrs {
			peerAddr := fmt.Sprintf("%s:8333", ip)
			allPeers = append(allPeers, peerAddr)
		}
	}

	return allPeers
}

// connectToPeer connects to a single Bitcoin peer
func (p *P2PClient) connectToPeer(addr string) error {
	// Create peer configuration
	peerCfg := &peer.Config{
		UserAgentName:    "DNN-Node",
		UserAgentVersion: "0.1.0",
		ChainParams:      p.network,
		Services:         wire.SFNodeNetwork,
		TrickleInterval:  time.Second * 10,
		Listeners: peer.MessageListeners{
			OnBlock:   p.onBlock,
			OnHeaders: p.onHeaders,
			OnTx:      p.onTx,
			OnInv:     p.onInv,
		},
	}

	// Create peer instance
	peerInstance, err := peer.NewOutboundPeer(peerCfg, addr)
	if err != nil {
		return fmt.Errorf("failed to create peer: %w", err)
	}

	// Manually create network connection
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	// Associate the connection with the peer
	// This starts the peer goroutines and begins the handshake
	peerInstance.AssociateConnection(conn)

	// Wait for the peer to complete version handshake
	// The peer starts in disconnected state and transitions to connected
	// after successful version exchange
	maxWait := 10 // 10 seconds max wait
	for i := 0; i < maxWait; i++ {
		if peerInstance.Connected() {
			log.Printf("Peer %s handshake completed", addr)
			break
		}
		if i == maxWait-1 {
			return fmt.Errorf("peer handshake timeout after %d seconds", maxWait)
		}
		time.Sleep(1 * time.Second)
	}

	p.mu.Lock()
	p.peers = append(p.peers, peerInstance)
	p.mu.Unlock()

	return nil
}

// onBlock handles incoming block messages
func (p *P2PClient) onBlock(peer *peer.Peer, msg *wire.MsgBlock, buf []byte) {
	blockHash := msg.BlockHash().String()

	// Check if this block is being waited for
	p.mu.Lock()
	if respChan, exists := p.pendingBlocks[blockHash]; exists {
		delete(p.pendingBlocks, blockHash)
		p.mu.Unlock()

		// Send to the waiting goroutine
		select {
		case respChan <- msg:
			// Block delivered
		default:
			log.Printf("Response channel closed for block %s", blockHash[:16])
		}
		return
	}
	p.mu.Unlock()

	// Not expected, queue it anyway
	select {
	case p.blockChan <- msg:
		// Block queued for processing
	default:
		log.Printf("Block channel full, dropping block %s", blockHash[:16])
	}
}

// onHeaders handles headers messages
func (p *P2PClient) onHeaders(peer *peer.Peer, msg *wire.MsgHeaders) {
	log.Printf("Received headers message with %d headers from peer", len(msg.Headers))
	select {
	case p.headersChan <- msg:
		log.Printf("Headers queued for processing")
	default:
		log.Printf("Headers channel full, dropping headers")
	}
}

// onTx handles incoming transaction messages
func (p *P2PClient) onTx(peer *peer.Peer, msg *wire.MsgTx) {
	txHash := msg.TxHash().String()

	// Check if this transaction is being waited for
	p.mu.Lock()
	if respChan, exists := p.pendingTxs[txHash]; exists {
		delete(p.pendingTxs, txHash)
		p.mu.Unlock()

		// Send to the waiting goroutine
		select {
		case respChan <- msg:
			// Transaction delivered
		default:
			log.Printf("Response channel closed for tx %s", txHash[:16])
		}
		return
	}
	p.mu.Unlock()

	// Not expected, queue it anyway
	select {
	case p.txChan <- msg:
		// Transaction queued
	default:
		// Channel full, drop it
	}
}

// onInv handles inventory messages
func (p *P2PClient) onInv(peer *peer.Peer, msg *wire.MsgInv) {
	// We don't need to handle inventory for our use case
	// We'll request blocks and transactions directly by hash
}

// GetBlockCount returns the current block height using peer consensus
// This queries multiple peers and uses the median height to prevent lying peers
func (p *P2PClient) GetBlockCount() (int64, error) {
	// Check if we have a recent cached height (within last 30 seconds - reduced from 5 minutes)
	p.mu.RLock()
	if p.currentHeight > 0 && time.Since(p.heightCacheTime) < 30*time.Second {
		height := p.currentHeight
		cacheAge := time.Since(p.heightCacheTime)
		p.mu.RUnlock()
		log.Printf("Using cached block height %d (cached %v ago)", height, cacheAge.Round(time.Second))
		return height, nil
	}
	p.mu.RUnlock()

	// Get heights from multiple peers for consensus
	return p.getBlockCountConsensus()
}

// getBlockCountConsensus queries multiple peers and returns median height
// This prevents a single malicious peer from lying about block height
// NOTE: Queries peers SEQUENTIALLY to avoid headerCache race conditions
func (p *P2PClient) getBlockCountConsensus() (int64, error) {
	p.mu.RLock()
	numPeers := len(p.peers)
	if numPeers == 0 {
		p.mu.RUnlock()
		return 0, fmt.Errorf("no connected peers")
	}

	// Query up to 3 peers for consensus (reduced for sequential)
	maxPeersToQuery := 3
	if numPeers < maxPeersToQuery {
		maxPeersToQuery = numPeers
	}

	// Copy peer list to avoid holding lock during queries
	peersToQuery := make([]*peer.Peer, maxPeersToQuery)
	for i := 0; i < maxPeersToQuery; i++ {
		peersToQuery[i] = p.peers[(p.nextPeerIndex+i)%numPeers]
	}
	p.nextPeerIndex += maxPeersToQuery
	p.mu.RUnlock()

	log.Printf("Querying %d peers for height consensus (sequential)...", maxPeersToQuery)

	// Query peers SEQUENTIALLY to prevent headerCache race conditions
	var heights []int64
	for _, selectedPeer := range peersToQuery {
		peerAddr := selectedPeer.Addr()
		height, err := p.getBlockCountFromPeer(selectedPeer)
		if err == nil {
			heights = append(heights, height)
			log.Printf("  Peer %s reports height: %d", peerAddr, height)
			p.recordPeerSuccess(peerAddr)
			// If we have at least 2 heights agreeing, we can stop early
			if len(heights) >= 2 {
				break
			}
		} else {
			log.Printf("  Peer %s failed: %v", peerAddr, err)
			p.recordPeerFailure(peerAddr)
		}
	}

	if len(heights) == 0 {
		return 0, fmt.Errorf("all %d peers failed to respond", maxPeersToQuery)
	}

	// Sort heights and take median
	for i := 0; i < len(heights)-1; i++ {
		for j := i + 1; j < len(heights); j++ {
			if heights[i] > heights[j] {
				heights[i], heights[j] = heights[j], heights[i]
			}
		}
	}

	medianHeight := heights[len(heights)/2]
	log.Printf("✓ Consensus height: %d (from %d peers: %v)", medianHeight, len(heights), heights)

	// Cache the consensus height
	p.mu.Lock()
	p.currentHeight = medianHeight
	p.heightCacheTime = time.Now()
	p.mu.Unlock()

	return medianHeight, nil
}

// getBlockCountFromPeer attempts to get block count from a specific peer
func (p *P2PClient) getBlockCountFromPeer(selectedPeer *peer.Peer) (int64, error) {
	// Start from a known checkpoint before genesis
	// Using block 900,000 as a stable checkpoint (well before our genesis)
	checkpointHeight := int64(900000)
	checkpointHashStr := "000000000000000000010538edbfd2d5b809a33dd83f284aeea41c6d0d96968a"

	// Check if we have cached headers - start from last known position
	p.mu.RLock()
	var currentHeight int64
	var currentHashStr string

	// Find the highest cached block to resume from
	highestCached := checkpointHeight
	for height, hash := range p.headerCache {
		if height > highestCached && height >= checkpointHeight {
			highestCached = height
			currentHashStr = hash
		}
	}
	p.mu.RUnlock()

	if currentHashStr != "" {
		currentHeight = highestCached + 1
		log.Printf("Resuming from cached block %d (hash: %s...)", highestCached, currentHashStr[:16])
	} else {
		currentHeight = checkpointHeight + 1
		currentHashStr = checkpointHashStr
		log.Printf("Starting from checkpoint block %d (hash: %s...)", checkpointHeight, checkpointHashStr[:16])
	}

	// IMPORTANT: GetHeaders returns headers AFTER the locator block, so first header
	// will be block 900001. Start counting from checkpoint + 1.

	// Keep requesting headers until we reach the tip
	for {
		currentHash, err := chainhash.NewHashFromStr(currentHashStr)
		if err != nil {
			return 0, fmt.Errorf("invalid hash: %w", err)
		}

		// IMPORTANT: Drain any stale headers from previous requests/timeouts
		// This prevents double-counting when late headers arrive after timeout
	drainLoop:
		for {
			select {
			case <-p.headersChan:
				log.Printf("  Drained stale headers message from channel")
			default:
				break drainLoop
			}
		}

		getHeaders := wire.NewMsgGetHeaders()
		getHeaders.AddBlockLocatorHash(currentHash)
		log.Printf("Sending getheaders request for block %s...", currentHashStr[:16])
		selectedPeer.QueueMessage(getHeaders, nil)

		// Wait for headers
		timeout := time.After(5 * time.Second)
		log.Printf("Waiting for headers response (5s timeout)...")
		select {
		case headers := <-p.headersChan:
			if len(headers.Headers) == 0 {
				// No more headers - we've reached the tip
				p.mu.Lock()
				p.currentHeight = currentHeight - 1
				p.heightCacheTime = time.Now()
				p.mu.Unlock()
				log.Printf("✓ Current block height: %d (reached tip)", currentHeight-1)
				return currentHeight - 1, nil
			}

			// Cache headers and update current height and hash
			// IMPORTANT: Validate each header before caching (prevents fake chains)
			p.mu.Lock()
			prevHashStr := currentHashStr // Track previous hash for chain validation
			invalidFound := false
			for _, header := range headers.Headers {
				hash := header.BlockHash()
				hashStr := hash.String()

				// Validate header: check prev_hash links to previous block
				// Use string comparison to avoid struct comparison issues
				if header.PrevBlock.String() != prevHashStr {
					log.Printf("⚠️ INVALID HEADER: Block at height %d has wrong prev_hash!", currentHeight)
					log.Printf("   Expected prev: %s", prevHashStr)
					log.Printf("   Got prev: %s", header.PrevBlock.String())
					invalidFound = true
					break
				}

				// Validate proof of work: block hash must be less than target
				// The hash is already in little-endian, just check leading zeros
				// A valid Bitcoin block hash has many leading zeros
				// For mainnet, difficulty is so high that blocks have ~19+ leading zero bits
				hashBytes := hash.CloneBytes()
				leadingZeros := 0
				for i := len(hashBytes) - 1; i >= 0; i-- {
					if hashBytes[i] == 0 {
						leadingZeros += 8
					} else {
						// Count leading zero bits in this byte
						for b := uint8(0x80); b > 0 && hashBytes[i]&b == 0; b >>= 1 {
							leadingZeros++
						}
						break
					}
				}
				// Mainnet blocks since 2020 have hash with at least 70 leading zero bits
				// We use a conservative threshold of 60 to avoid false positives
				if leadingZeros < 60 {
					log.Printf("⚠️ INVALID HEADER: Block %s fails PoW check (only %d leading zeros)", hashStr[:16], leadingZeros)
					invalidFound = true
					break
				}

				// Header is valid - cache it
				p.headerCache[currentHeight] = hashStr
				p.blockHashToHeight[hashStr] = currentHeight

				prevHashStr = hashStr
				currentHashStr = hashStr
				currentHeight++
			}
			p.mu.Unlock()

			// If invalid headers found, reject this peer's response
			if invalidFound {
				return 0, fmt.Errorf("peer sent invalid headers - possible attack")
			}

			// If we got less than 2000 headers, we've reached the tip
			if len(headers.Headers) < 2000 {
				p.mu.Lock()
				p.currentHeight = currentHeight - 1
				p.heightCacheTime = time.Now()
				p.mu.Unlock()
				log.Printf("✓ Current block height: %d (from headers)", currentHeight-1)
				return currentHeight - 1, nil
			}

			// Got full batch of 2000, continue to next batch
			log.Printf("  Got batch of %d headers (now at ~%d), requesting more...", len(headers.Headers), currentHeight-1)

		case <-timeout:
			// Return error to trigger peer rotation in parent function
			return 0, fmt.Errorf("timeout waiting for headers")
		}
	}
}

// GetBlockHash returns the block hash for a given height
func (p *P2PClient) GetBlockHash(height int64) (string, error) {
	// Check cache first
	p.mu.RLock()
	if hash, exists := p.headerCache[height]; exists {
		p.mu.RUnlock()
		return hash, nil
	}
	p.mu.RUnlock()

	// For P2P: We need to download headers from a known checkpoint
	// Since DNN starts at block 900,000, we can request headers from there

	// If we don't have this block's hash yet, we need to sync headers
	if err := p.syncHeadersToHeight(height); err != nil {
		return "", fmt.Errorf("failed to sync headers to height %d: %w", height, err)
	}

	// Check cache again after syncing
	p.mu.RLock()
	if hash, exists := p.headerCache[height]; exists {
		p.mu.RUnlock()
		return hash, nil
	}
	p.mu.RUnlock()

	return "", fmt.Errorf("block hash not found for height %d after header sync", height)
}

// syncHeadersToHeight syncs block headers up to the specified height
func (p *P2PClient) syncHeadersToHeight(targetHeight int64) error {
	// Select a peer using round-robin
	p.mu.Lock()
	if len(p.peers) == 0 {
		p.mu.Unlock()
		return fmt.Errorf("no connected peers")
	}
	selectedPeer := p.peers[p.nextPeerIndex%len(p.peers)]
	p.nextPeerIndex++
	peerAddr := selectedPeer.Addr()
	p.mu.Unlock()

	log.Printf("Using peer %s for header sync to height %d", peerAddr, targetHeight)

	// Start from a known checkpoint (block 900,000)
	// This is before our genesis so we can build headers up to current genesis
	checkpointHeight := int64(900000)
	checkpointHashStr := "000000000000000000010538edbfd2d5b809a33dd83f284aeea41c6d0d96968a"

	// IMPORTANT: When requesting headers with a block locator, the Bitcoin protocol
	// returns headers STARTING FROM THE NEXT BLOCK after the locator.
	// So if we request headers from block 900000, we get headers for 900001, 900002, etc.
	// Therefore, currentHeight should start at checkpoint + 1.
	currentHeight := checkpointHeight + 1
	currentHashStr := checkpointHashStr

	log.Printf("Syncing headers from block %d to %d...", checkpointHeight+1, targetHeight)

	totalHeadersSynced := 0

	// Keep requesting headers until we reach target height
	// Each request can return up to 2000 headers
	for currentHeight <= targetHeight {
		currentHash, err := chainhash.NewHashFromStr(currentHashStr)
		if err != nil {
			return fmt.Errorf("invalid hash: %w", err)
		}

		// IMPORTANT: Drain any stale headers from previous requests/timeouts
	drainLoop2:
		for {
			select {
			case <-p.headersChan:
				log.Printf("  Drained stale headers message from channel")
			default:
				break drainLoop2
			}
		}

		// Request next batch of headers
		getHeaders := wire.NewMsgGetHeaders()
		getHeaders.AddBlockLocatorHash(currentHash)

		selectedPeer.QueueMessage(getHeaders, nil)

		// Wait for headers response
		timeout := time.After(5 * time.Second)
		select {
		case headers := <-p.headersChan:
			if len(headers.Headers) == 0 {
				log.Printf("✓ Synced %d total headers (reached tip at block %d)", totalHeadersSynced, currentHeight-1)
				return nil
			}

			p.mu.Lock()
			// Process headers and build height->hash and hash->height mappings
			for _, header := range headers.Headers {
				hash := header.BlockHash()
				hashStr := hash.String()
				p.headerCache[currentHeight] = hashStr
				p.blockHashToHeight[hashStr] = currentHeight // Reverse mapping
				currentHashStr = hashStr                     // Update for next batch
				currentHeight++
				totalHeadersSynced++

				if currentHeight > targetHeight {
					break
				}
			}
			p.mu.Unlock()

			log.Printf("  Synced batch of %d headers (now at block %d)", len(headers.Headers), currentHeight-1)

			// If we got less than 2000 headers, we've reached the tip
			if len(headers.Headers) < 2000 {
				log.Printf("✓ Synced %d total headers (reached tip)", totalHeadersSynced)
				return nil
			}

			// If we've reached target, stop
			if currentHeight > targetHeight {
				log.Printf("✓ Synced %d total headers (reached target height %d)", totalHeadersSynced, targetHeight)
				return nil
			}

		case <-timeout:
			log.Printf("⚠️ Timeout waiting for headers at height %d from peer %s - rotating to next peer...", currentHeight, peerAddr)

			// Record failure for this peer
			p.recordPeerFailure(peerAddr)

			// Select next peer and retry this batch
			p.mu.Lock()
			if len(p.peers) == 0 {
				p.mu.Unlock()
				return fmt.Errorf("no connected peers")
			}
			selectedPeer = p.peers[p.nextPeerIndex%len(p.peers)]
			p.nextPeerIndex++
			peerAddr = selectedPeer.Addr()
			p.mu.Unlock()

			log.Printf("Retrying with peer %s...", peerAddr)
			// Continue the loop to retry with new peer
		}
	}

	return nil
}

// GetBlock retrieves a block by hash from connected peers
func (p *P2PClient) GetBlock(hashStr string) (*Block, error) {
	// Check cache first
	p.mu.RLock()
	if cached, exists := p.blockCache[hashStr]; exists {
		p.mu.RUnlock()
		log.Printf("🔍 DEBUG: Block %s... from CACHE (txToBlockHash NOT updated)", hashStr[:16])
		return cached, nil
	}
	p.mu.RUnlock()
	log.Printf("🔍 DEBUG: Block %s... needs DOWNLOAD (will index TXs)", hashStr[:16])

	// Parse block hash
	hash, err := chainhash.NewHashFromStr(hashStr)
	if err != nil {
		return nil, fmt.Errorf("invalid block hash: %w", err)
	}

	// PARALLEL BLOCK REQUEST: Query multiple peers simultaneously
	// This is much faster than sequential attempts

	p.mu.RLock()
	availablePeers := make([]*peer.Peer, 0, len(p.peers))
	for _, peer := range p.peers {
		if peer.Connected() {
			availablePeers = append(availablePeers, peer)
		}
	}
	p.mu.RUnlock()

	if len(availablePeers) == 0 {
		// Try to connect to new peers
		p.tryConnectNewPeer()
		return nil, fmt.Errorf("no connected peers available")
	}

	// Request from multiple peers in parallel (use top 3 fastest peers)
	numParallelRequests := 3
	if len(availablePeers) < numParallelRequests {
		numParallelRequests = len(availablePeers)
	}

	// Sort peers by performance (fastest first)
	sortedPeers := p.getSortedPeersByPerformance(availablePeers)

	log.Printf("Requesting block %s... from %d peers in parallel", hashStr[:16], numParallelRequests)

	// Channel to receive first successful response
	type blockResult struct {
		block *Block
		peer  string
		err   error
	}
	resultChan := make(chan blockResult, numParallelRequests)

	// Request from top N fastest peers simultaneously
	for i := 0; i < numParallelRequests; i++ {
		selectedPeer := sortedPeers[i]
		peerAddr := selectedPeer.Addr()

		go func(pr *peer.Peer, addr string) {
			startTime := time.Now()
			block, err := p.requestBlockFromPeer(pr, addr, hash, hashStr, 0)
			responseTime := time.Since(startTime)

			// Update peer stats
			p.updatePeerStats(addr, err == nil, responseTime)

			resultChan <- blockResult{block: block, peer: addr, err: err}
		}(selectedPeer, peerAddr)
	}

	// Wait for first successful response or all to fail
	var lastErr error
	for i := 0; i < numParallelRequests; i++ {
		result := <-resultChan

		if result.err == nil && result.block != nil {
			log.Printf("✓ Received block %s... from %s (parallel request)", hashStr[:16], result.peer)
			return result.block, nil
		}

		if result.err != nil {
			lastErr = result.err
			log.Printf("✗ Peer %s failed: %v", result.peer, result.err)
		}
	}

	// All parallel requests failed, try connecting to new peers and retry once
	log.Printf("All %d parallel requests failed, trying new peers...", numParallelRequests)
	p.tryConnectNewPeer()

	return nil, fmt.Errorf("failed to get block from %d peers: %v", numParallelRequests, lastErr)
}

// pingPeer tests if a peer is responsive with a quick ping (5 second timeout)
func (p *P2PClient) pingPeer(peer *peer.Peer, peerAddr string) bool {
	// Send a ping message
	nonce := uint64(time.Now().UnixNano())
	pingMsg := wire.NewMsgPing(nonce)
	peer.QueueMessage(pingMsg, nil)

	// For simplicity, we'll just wait a bit and assume it's alive
	// A full implementation would track pong responses
	time.Sleep(100 * time.Millisecond)

	// Check if peer is still connected
	return peer.Connected()
}

// tryConnectNewPeer attempts to connect to a new peer from the discovered pool
func (p *P2PClient) tryConnectNewPeer() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Already have enough peers
	if len(p.peers) >= 15 {
		return true
	}

	// Try to find an unconnected peer
	connectedAddrs := make(map[string]bool)
	for _, peer := range p.peers {
		connectedAddrs[peer.Addr()] = true
	}

	for _, addr := range p.discoveredPeers {
		if connectedAddrs[addr] {
			continue
		}

		// Try to connect (temporarily unlock for connection)
		p.mu.Unlock()
		err := p.connectToPeer(addr)
		p.mu.Lock()

		if err == nil {
			log.Printf("✓ Connected to new peer: %s (total: %d peers)", addr, len(p.peers))
			return true
		}
	}

	return false
}

// recordPeerFailure tracks when a peer times out or fails
func (p *P2PClient) recordPeerFailure(peerAddr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.peerFailures[peerAddr]++
	p.peerLastFail[peerAddr] = time.Now()

	failCount := p.peerFailures[peerAddr]
	if failCount > 3 {
		log.Printf("⚠️ Peer %s has failed %d times - may be unreliable", peerAddr, failCount)
	}
}

// recordPeerSuccess clears failure count on successful response
func (p *P2PClient) recordPeerSuccess(peerAddr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear failure count on success
	delete(p.peerFailures, peerAddr)
	delete(p.peerLastFail, peerAddr)
}

// updatePeerStats updates performance metrics for a peer
func (p *P2PClient) updatePeerStats(peerAddr string, success bool, responseTime time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats, exists := p.peerStats[peerAddr]
	if !exists {
		stats = &PeerStats{Address: peerAddr}
		p.peerStats[peerAddr] = stats
	}

	if success {
		stats.SuccessCount++
		stats.TotalResponseTime += responseTime
		stats.AverageResponseTime = stats.TotalResponseTime / time.Duration(stats.SuccessCount)
		stats.LastSuccess = time.Now()
	} else {
		stats.FailureCount++
		stats.LastFailure = time.Now()
	}
}

// getSortedPeersByPerformance returns peers sorted by performance (fastest first)
func (p *P2PClient) getSortedPeersByPerformance(peers []*peer.Peer) []*peer.Peer {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Create a copy to sort
	sorted := make([]*peer.Peer, len(peers))
	copy(sorted, peers)

	// Sort by average response time (fastest first)
	// Peers with no stats go to the end
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			addrI := sorted[i].Addr()
			addrJ := sorted[j].Addr()

			statsI := p.peerStats[addrI]
			statsJ := p.peerStats[addrJ]

			// Peers with stats and successes come first
			if statsI == nil || statsI.SuccessCount == 0 {
				// i has no stats, j should come first if it has stats
				if statsJ != nil && statsJ.SuccessCount > 0 {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
				continue
			}

			if statsJ == nil || statsJ.SuccessCount == 0 {
				// j has no stats, i stays first
				continue
			}

			// Both have stats, sort by average response time
			if statsI.AverageResponseTime > statsJ.AverageResponseTime {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

// requestBlockFromPeer requests a block from a specific peer
func (p *P2PClient) requestBlockFromPeer(selectedPeer *peer.Peer, peerAddr string, hash *chainhash.Hash, hashStr string, height int64) (*Block, error) {
	// Create response channel for this specific block
	respChan := make(chan *wire.MsgBlock, 1)

	p.mu.Lock()
	p.pendingBlocks[hashStr] = respChan
	p.mu.Unlock()

	// Clean up on exit
	defer func() {
		p.mu.Lock()
		delete(p.pendingBlocks, hashStr)
		p.mu.Unlock()
		close(respChan)
	}()

	// Send getdata message to request the block
	getData := wire.NewMsgGetData()
	inv := wire.NewInvVect(wire.InvTypeBlock, hash)
	if err := getData.AddInvVect(inv); err != nil {
		return nil, fmt.Errorf("failed to add inv vect: %w", err)
	}

	selectedPeer.QueueMessage(getData, nil)

	// Wait for block response (short timeout, we have many peers to try)
	timeout := time.After(15 * time.Second)
	select {
	case msgBlock := <-respChan:
		log.Printf("✓ Received block %s... with %d transactions from %s", hashStr[:16], len(msgBlock.Transactions), peerAddr)

		// CRITICAL: Validate merkle root matches transactions
		// This ensures we received the complete, unmodified block
		if err := p.validateBlockMerkleRoot(msgBlock); err != nil {
			log.Printf("⚠️ MERKLE VALIDATION FAILED for block %s from %s: %v", hashStr[:16], peerAddr, err)
			return nil, fmt.Errorf("merkle validation failed: %w", err)
		}
		log.Printf("✓ Merkle root validated for block %s", hashStr[:16])

		// Convert wire.MsgBlock to our Block format
		block := p.convertWireBlock(msgBlock, hashStr, height)

		// Cache the block (only after validation passes)
		p.mu.Lock()
		p.blockCache[hashStr] = block
		p.mu.Unlock()

		return block, nil

	case <-timeout:
		return nil, fmt.Errorf("timeout")
	}
}

// validateBlockMerkleRoot validates that the merkle root in the block header
// matches the merkle root calculated from the block's transactions.
// This ensures we received the complete block without any missing transactions.
func (p *P2PClient) validateBlockMerkleRoot(msgBlock *wire.MsgBlock) error {
	if len(msgBlock.Transactions) == 0 {
		return fmt.Errorf("block has no transactions")
	}

	// Calculate merkle root from transactions
	txHashes := make([]*chainhash.Hash, len(msgBlock.Transactions))
	for i, tx := range msgBlock.Transactions {
		hash := tx.TxHash()
		txHashes[i] = &hash
	}

	calculatedMerkle := calcMerkleRoot(txHashes)
	headerMerkle := msgBlock.Header.MerkleRoot

	if !calculatedMerkle.IsEqual(&headerMerkle) {
		return fmt.Errorf("merkle mismatch: calculated=%s, header=%s (tx count: %d)",
			calculatedMerkle.String()[:16], headerMerkle.String()[:16], len(msgBlock.Transactions))
	}

	return nil
}

// calcMerkleRoot calculates the merkle root from a list of transaction hashes
// Uses the standard Bitcoin merkle tree algorithm
func calcMerkleRoot(txHashes []*chainhash.Hash) chainhash.Hash {
	if len(txHashes) == 0 {
		return chainhash.Hash{}
	}

	// Make a copy to avoid modifying the original
	hashes := make([]*chainhash.Hash, len(txHashes))
	for i, h := range txHashes {
		hashCopy := *h
		hashes[i] = &hashCopy
	}

	// Build merkle tree level by level
	for len(hashes) > 1 {
		// If odd number of hashes, duplicate the last one
		if len(hashes)%2 != 0 {
			hashes = append(hashes, hashes[len(hashes)-1])
		}

		// Combine pairs
		nextLevel := make([]*chainhash.Hash, len(hashes)/2)
		for i := 0; i < len(hashes); i += 2 {
			combined := chainhash.DoubleHashH(append(hashes[i][:], hashes[i+1][:]...))
			nextLevel[i/2] = &combined
		}
		hashes = nextLevel
	}

	return *hashes[0]
}

// GetBlockWithInputs retrieves a block with full input address data
// Pure P2P approach: download referenced blocks to resolve input addresses
func (p *P2PClient) GetBlockWithInputs(hashStr string) (*Block, error) {
	// Check cache first
	p.mu.RLock()
	if cached, exists := p.blockCache[hashStr]; exists {
		// Check if cached block has input addresses
		if len(cached.Transactions) > 1 && len(cached.Transactions[1].Inputs) > 0 && cached.Transactions[1].Inputs[0].Address != "" {
			p.mu.RUnlock()
			log.Printf("Block %s... found in cache with input addresses", hashStr[:16])
			return cached, nil
		}
	}
	p.mu.RUnlock()

	// Get the block height from our header mapping
	height, hasHeight := p.GetHeightForBlockHash(hashStr)
	if !hasHeight {
		height = 0 // Unknown height
	}

	// Get the block via P2P
	block, err := p.GetBlock(hashStr)
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	// Set the height if we know it
	block.Height = height

	log.Printf("Resolving input addresses for %d transactions (100%% P2P)...", len(block.Transactions))

	// Build tx map for current block
	currentBlockTxs := make(map[string]*Transaction)
	for i := range block.Transactions {
		currentBlockTxs[block.Transactions[i].TxID] = &block.Transactions[i]
	}

	// Collect all unique blocks we need to download for input resolution
	// Only look for blocks that are in our index (from previous sequential sync)
	neededBlocks := make(map[string]bool)
	unknownInputCount := 0

	for txIdx := 1; txIdx < len(block.Transactions); txIdx++ {
		tx := &block.Transactions[txIdx]
		for _, input := range tx.Inputs {
			// For P2WPKH inputs, address is already set from witness data
			// (see convertWireTransaction). Non-P2WPKH inputs won't have addresses
			// but DNN only accepts P2WPKH anyway, so those TXs will be rejected.
			if input.Address != "" {
				continue // Already have address from witness data
			}

			// Check if we already have this tx in current block
			if _, exists := currentBlockTxs[input.TxID]; exists {
				continue
			}

			// Check our in-memory index (for blocks downloaded this session)
			p.mu.RLock()
			blockHash, indexExists := p.txToBlockHash[input.TxID]
			p.mu.RUnlock()

			if indexExists {
				neededBlocks[blockHash] = true
			} else {
				// Not in our index - likely a legacy/taproot input which won't pass DNN validation anyway
				unknownInputCount++
			}
		}
	}

	if unknownInputCount > 0 {
		log.Printf("⚠ Warning: %d inputs reference transactions not in our index (will not be validated)", unknownInputCount)
		log.Printf("   Tip: Sync sequentially from genesis block onwards to build complete tx index")
	}

	log.Printf("Need to download %d previous blocks to resolve inputs (100%% P2P)...", len(neededBlocks))

	// Download all needed blocks
	downloadedBlocks := make(map[string]*Block)
	for neededBlockHash := range neededBlocks {
		prevBlock, err := p.GetBlock(neededBlockHash)
		if err != nil {
			log.Printf("⚠ Couldn't download block %s...: %v", neededBlockHash[:16], err)
			continue
		}

		// Set block height if we know it
		if prevHeight, hasHeight := p.GetHeightForBlockHash(neededBlockHash); hasHeight {
			prevBlock.Height = prevHeight
		}

		downloadedBlocks[neededBlockHash] = prevBlock
		log.Printf("✓ Downloaded previous block %s... (height %d) with %d transactions", neededBlockHash[:16], prevBlock.Height, len(prevBlock.Transactions))
	}

	// Now resolve input addresses using the downloaded blocks
	resolvedCount := 0
	for txIdx := 1; txIdx < len(block.Transactions); txIdx++ {
		tx := &block.Transactions[txIdx]

		for inputIdx := range tx.Inputs {
			input := &tx.Inputs[inputIdx]
			if input.Address != "" {
				continue
			}

			// Find the previous transaction
			var prevTx *Transaction

			// Check current block first
			if cached, exists := currentBlockTxs[input.TxID]; exists {
				prevTx = cached
			} else {
				// Check downloaded blocks
				p.mu.RLock()
				if blockHash, exists := p.txToBlockHash[input.TxID]; exists {
					if prevBlock, exists := downloadedBlocks[blockHash]; exists {
						// Find the transaction in that block
						for i := range prevBlock.Transactions {
							if prevBlock.Transactions[i].TxID == input.TxID {
								prevTx = &prevBlock.Transactions[i]
								break
							}
						}
					}
				}
				p.mu.RUnlock()
			}

			if prevTx != nil && int(input.Vout) < len(prevTx.Outputs) {
				prevOutput := prevTx.Outputs[input.Vout]

				// Set the input address from the previous output
				if len(prevOutput.ScriptPubKey.Addresses) > 0 {
					input.Address = prevOutput.ScriptPubKey.Addresses[0]
					resolvedCount++
				} else if prevOutput.ScriptPubKey.Address != "" {
					input.Address = prevOutput.ScriptPubKey.Address
					resolvedCount++
				}

				// CRITICAL: Also set the input value from the previous output
				// This is needed for fee calculation!
				input.Value = int64(prevOutput.Value * 100000000) // BTC to satoshis
			}
		}
	}

	log.Printf("✓ Resolved %d input addresses via pure P2P", resolvedCount)

	// Update cache
	p.mu.Lock()
	p.blockCache[hashStr] = block
	p.mu.Unlock()

	return block, nil
}

// convertWireBlock converts wire.MsgBlock to our Block format
func (p *P2PClient) convertWireBlock(msgBlock *wire.MsgBlock, hash string, height int64) *Block {
	block := &Block{
		Hash:         hash,
		Height:       height,
		Time:         msgBlock.Header.Timestamp.Unix(),
		Transactions: make([]Transaction, 0, len(msgBlock.Transactions)),
	}

	// Convert each transaction
	// Note: tx_block_index is no longer persisted to database since P2WPKH inputs
	// can have their addresses derived directly from witness data. This saves ~50%
	// of database storage. In-memory cache is kept for current session only.
	for _, msgTx := range msgBlock.Transactions {
		tx := p.convertWireTransaction(msgTx)

		// Set block info on the transaction
		tx.BlockHash = hash
		tx.BlockHeight = height
		tx.Confirmed = true

		block.Transactions = append(block.Transactions, *tx)

		// Index in memory only (fast cache for current session)
		p.mu.Lock()
		p.txToBlockHash[tx.TxID] = hash
		p.mu.Unlock()
	}

	return block
}

// convertWireTransaction converts wire.MsgTx to our Transaction format
func (p *P2PClient) convertWireTransaction(msgTx *wire.MsgTx) *Transaction {
	// Calculate transaction size metrics
	// Size = full serialized size including witness data
	// Weight = base size * 3 + total size (SegWit formula)
	// VSize = weight / 4 (rounded up)

	var buf bytes.Buffer
	msgTx.Serialize(&buf)
	size := buf.Len()

	// For weight calculation, we need base size (without witness)
	buf.Reset()
	msgTx.SerializeNoWitness(&buf)
	baseSize := buf.Len()

	// Weight = base_size * 3 + total_size
	weight := baseSize*3 + size

	// VSize = ceil(weight / 4)
	vsize := (weight + 3) / 4 // Integer division with rounding up

	tx := &Transaction{
		TxID:    msgTx.TxHash().String(),
		Inputs:  make([]TxInput, 0, len(msgTx.TxIn)),
		Outputs: make([]TxOutput, 0, len(msgTx.TxOut)),
		Size:    size,
		Weight:  weight,
		VSize:   vsize,
	}

	// Convert inputs
	// Note: P2P protocol doesn't include input addresses directly
	// We only get the previous output reference (txid:vout)
	// For DNN validation, we need the actual addresses, so we'll
	// fall back to REST API to get the full transaction data
	// HOWEVER: For P2WPKH inputs, we CAN extract the address from witness data!
	// This enables lightweight mode without needing previous TX lookups
	for _, txIn := range msgTx.TxIn {
		input := TxInput{
			TxID:     txIn.PreviousOutPoint.Hash.String(),
			Vout:     txIn.PreviousOutPoint.Index,
			Sequence: txIn.Sequence,
		}

		// P2WPKH witness extraction: witness[0] = signature, witness[1] = compressed pubkey
		// If we detect a P2WPKH witness pattern, derive the address directly!
		if len(txIn.Witness) == 2 && len(txIn.Witness[1]) == 33 {
			// This is a P2WPKH input - pubkey is 33 bytes (compressed)
			pubkeyBytes := txIn.Witness[1]
			addr := deriveP2WPKHAddressFromPubkey(pubkeyBytes, p.network)
			if addr != "" {
				input.Address = addr
			}
		}
		// Note: For Taproot (bc1p) inputs, witness only contains signature, no pubkey
		// Those still require previous TX lookup (but DNN will only accept P2WPKH)

		tx.Inputs = append(tx.Inputs, input)
	}

	// Convert outputs
	for i, txOut := range msgTx.TxOut {
		// Extract address from script
		scriptClass, addresses, _, err := txscript.ExtractPkScriptAddrs(txOut.PkScript, p.network)

		output := TxOutput{
			Value: float64(txOut.Value) / 100000000.0, // satoshis to BTC
			N:     uint32(i),
			ScriptPubKey: ScriptPubKey{
				Type: scriptClass.String(),
			},
		}

		if err == nil && len(addresses) > 0 {
			addrStrings := make([]string, len(addresses))
			for i, addr := range addresses {
				addrStrings[i] = addr.EncodeAddress()
			}
			output.ScriptPubKey.Addresses = addrStrings
			output.ScriptPubKey.Address = addrStrings[0]
		}

		tx.Outputs = append(tx.Outputs, output)
	}

	return tx
}

// deriveP2WPKHAddressFromPubkey derives a P2WPKH (bc1q) address from a 33-byte compressed public key
// This is used to extract input addresses from witness data for lightweight mode
func deriveP2WPKHAddressFromPubkey(compressedPubkey []byte, network *chaincfg.Params) string {
	if len(compressedPubkey) != 33 {
		return ""
	}

	// Hash160 the compressed public key (SHA256 + RIPEMD160)
	pubKeyHash := btcutil.Hash160(compressedPubkey)

	// Create P2WPKH address from pubkey hash
	addr, err := btcutil.NewAddressWitnessPubKeyHash(pubKeyHash, network)
	if err != nil {
		return ""
	}

	return addr.EncodeAddress()
}

// Stop disconnects from all peers
func (p *P2PClient) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, peer := range p.peers {
		peer.Disconnect()
		peer.WaitForDisconnect()
	}

	p.peers = nil
	log.Println("Bitcoin P2P client stopped")
}

// GetBlockHashForTransaction returns the block hash containing a transaction
func (p *P2PClient) GetBlockHashForTransaction(txID string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	hash, exists := p.txToBlockHash[txID]
	return hash, exists
}

// GetTransactionFromIndex checks if a transaction is in our index and returns its block hash
func (p *P2PClient) GetTransactionFromIndex(txID string) (blockHash string, exists bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	blockHash, exists = p.txToBlockHash[txID]
	return
}

// GetHeightForBlockHash returns the height for a given block hash
func (p *P2PClient) GetHeightForBlockHash(blockHash string) (int64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	height, exists := p.blockHashToHeight[blockHash]
	return height, exists
}
