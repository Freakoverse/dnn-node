package policy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"dnn-node/internal/constants"
)

// Re-export constants for backward compatibility
// All constants are now centralized in internal/constants package
var (
	ReservedPrefixes  = constants.ReservedPrefixes
	ReservedAddresses = constants.ReservedAddresses
)

const (
	// Re-export event size limits
	MaxSizeKind60600 = constants.MaxSizeKind60600
	MaxSizeKind61600 = constants.MaxSizeKind61600
	MaxSizeKind62600 = constants.MaxSizeKind62600
	MaxSizeKind63600 = constants.MaxSizeKind63600

	// Re-export reorg settings
	ReorgCheckInterval = constants.ReorgCheckInterval
	ReorgLookback      = constants.ReorgLookback

	// Re-export rate limits
	MaxVersionsPerDTag = constants.MaxVersionsPerDTag
	MaxDTagsPerKind    = constants.MaxDTagsPerKind
)

// PolicyEnforcer enforces DNN node policy rules
type PolicyEnforcer struct {
	network        *chaincfg.Params
	networkMode    string // "mainnet", "testnet", or "dev"
	genesisBlock   int64  // Dynamically set based on network mode
	minFeeRate     int    // Dynamically set based on network mode
	base64Pattern  *regexp.Regexp
	dataURLPattern *regexp.Regexp
	binaryPatterns [][]byte
}

// NewPolicyEnforcer creates a new policy enforcer
func NewPolicyEnforcer(networkMode string) *PolicyEnforcer {
	// Determine Bitcoin network params
	network := &chaincfg.MainNetParams
	if networkMode == constants.NetworkTestnet || networkMode == constants.NetworkDev {
		network = &chaincfg.TestNet3Params
	}

	return &PolicyEnforcer{
		network:        network,
		networkMode:    networkMode,
		genesisBlock:   constants.GetGenesisBlock(networkMode),
		minFeeRate:     constants.GetMinFeeRate(networkMode),
		base64Pattern:  regexp.MustCompile(`^[A-Za-z0-9+/]{100,}={0,2}$`),
		dataURLPattern: regexp.MustCompile(`^data:[a-zA-Z]+/[a-zA-Z0-9.+-]+;base64,`),
		binaryPatterns: [][]byte{
			{0xFF, 0xD8},       // JPEG
			{0x89, 0x50, 0x4E}, // PNG
			{0x47, 0x49, 0x46}, // GIF
			{0x25, 0x50, 0x44}, // PDF
			{0x50, 0x4B},       // ZIP
		},
	}
}

// GetGenesisBlock returns the genesis block for this enforcer's network
func (pe *PolicyEnforcer) GetGenesisBlock() int64 {
	return pe.genesisBlock
}

// GetMinFeeRate returns the minimum fee rate for this enforcer's network
func (pe *PolicyEnforcer) GetMinFeeRate() int {
	return pe.minFeeRate
}

// ValidateEventSize validates that an event doesn't exceed size limits
func (pe *PolicyEnforcer) ValidateEventSize(event *nostr.Event) error {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	size := len(eventJSON)

	switch event.Kind {
	case 60600:
		if size > MaxSizeKind60600 {
			return fmt.Errorf("event exceeds maximum size of %d KB", MaxSizeKind60600/1024)
		}
	case 61600:
		if size > MaxSizeKind61600 {
			return fmt.Errorf("event exceeds maximum size of %d KB", MaxSizeKind61600/1024)
		}
	case 62600:
		if size > MaxSizeKind62600 {
			return fmt.Errorf("event exceeds maximum size of %d KB", MaxSizeKind62600/1024)
		}
	case 63600:
		if size > MaxSizeKind63600 {
			return fmt.Errorf("event exceeds maximum size of %d KB", MaxSizeKind63600/1024)
		}
	case 64600:
		// Reserved for future DNN protocol use - allow same size as anchor events
		if size > MaxSizeKind60600 {
			return fmt.Errorf("event exceeds maximum size of %d KB", MaxSizeKind60600/1024)
		}
	case 65600:
		// Sync request events - allow same size as anchor events
		if size > MaxSizeKind60600 {
			return fmt.Errorf("event exceeds maximum size of %d KB", MaxSizeKind60600/1024)
		}
	default:
		return fmt.Errorf("unsupported event kind: %d", event.Kind)
	}

	return nil
}

// ValidateAnchorTransaction validates that a Bitcoin transaction meets DNN criteria
func (pe *PolicyEnforcer) ValidateAnchorTransaction(tx Transaction) error {
	// Check self-transfer (same input and output address)
	if !pe.isSelfTransfer(tx) {
		return fmt.Errorf("transaction is not a self-transfer")
	}

	// Check fee rate >= minimum (network-dependent)
	if tx.FeeRate < pe.minFeeRate {
		return fmt.Errorf("fee rate %d is below minimum of %d sat/vB", tx.FeeRate, pe.minFeeRate)
	}

	// Verify address matches pubkey-derived Bitcoin address
	if !pe.verifyAddressMatch(tx.Address, tx.EventPubkey) {
		return fmt.Errorf("Bitcoin address does not match event author's pubkey")
	}

	return nil
}

// isSelfTransfer checks if a transaction has identical input and output addresses
func (pe *PolicyEnforcer) isSelfTransfer(tx Transaction) bool {
	// Check that there's exactly one unique address
	uniqueAddrs := make(map[string]bool)

	for _, addr := range tx.InputAddresses {
		uniqueAddrs[addr] = true
	}

	for _, addr := range tx.OutputAddresses {
		uniqueAddrs[addr] = true
	}

	// Must have exactly one unique address for self-transfer
	return len(uniqueAddrs) == 1 && len(tx.InputAddresses) > 0 && len(tx.OutputAddresses) > 0
}

// verifyAddressMatch verifies that a Bitcoin address matches a Nostr pubkey
func (pe *PolicyEnforcer) verifyAddressMatch(bitcoinAddr string, nostrPubkey string) bool {
	// The nostrPubkey should be in hex format
	// Derive Bitcoin address from Nostr pubkey
	// This is a simplified version - actual implementation would need proper key derivation
	derivedAddr, err := pe.deriveBitcoinAddress(nostrPubkey)
	if err != nil {
		return false
	}

	return derivedAddr == bitcoinAddr
}

// deriveBitcoinAddress derives a Bitcoin address from a Nostr pubkey
func (pe *PolicyEnforcer) deriveBitcoinAddress(pubkeyHex string) (string, error) {
	// This is a placeholder - actual implementation would:
	// 1. Convert Nostr pubkey to secp256k1 public key
	// 2. Generate Bitcoin address based on network (mainnet/testnet)
	// 3. Support different address types (P2WPKH, P2TR, etc.)

	// For now, return a dummy implementation
	return "", fmt.Errorf("address derivation not yet implemented")
}

// ValidateMetadataContent validates that metadata doesn't contain prohibited binary data
func (pe *PolicyEnforcer) ValidateMetadataContent(event *nostr.Event) error {
	if event.Kind != 63600 {
		return nil
	}

	// Parse content as JSON
	var content map[string]interface{}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return fmt.Errorf("invalid JSON content: %w", err)
	}

	// Check for metadata field
	metadata, ok := content["metadata"]
	if !ok {
		return fmt.Errorf("missing metadata field")
	}

	// Validate metadata doesn't contain binary data
	if err := pe.checkForBinaryData(metadata); err != nil {
		return fmt.Errorf("prohibited content in metadata: %w", err)
	}

	return nil
}

// checkForBinaryData recursively checks for binary data in JSON structure
func (pe *PolicyEnforcer) checkForBinaryData(data interface{}) error {
	switch v := data.(type) {
	case string:
		// Check for data URLs
		if pe.dataURLPattern.MatchString(v) {
			return fmt.Errorf("data URL detected")
		}

		// Check for base64 encoded data (heuristic)
		if len(v) > 2048 && pe.base64Pattern.MatchString(v) {
			return fmt.Errorf("suspected base64-encoded file")
		}

		// Check for binary signatures
		for _, sig := range pe.binaryPatterns {
			if strings.Contains(v, string(sig)) {
				return fmt.Errorf("binary signature detected")
			}
		}

	case map[string]interface{}:
		for key, value := range v {
			if err := pe.checkForBinaryData(value); err != nil {
				return fmt.Errorf("in field '%s': %w", key, err)
			}
		}

	case []interface{}:
		for i, item := range v {
			if err := pe.checkForBinaryData(item); err != nil {
				return fmt.Errorf("in array index %d: %w", i, err)
			}
		}
	}

	return nil
}

// IsReservedAddress checks if an address is reserved for system use
func (pe *PolicyEnforcer) IsReservedAddress(address string) bool {
	_, reserved := ReservedAddresses[address]
	return reserved
}

// IsReservedPrefix checks if a prefix is reserved for a specific blockchain
func (pe *PolicyEnforcer) IsReservedPrefix(prefix string) bool {
	_, reserved := ReservedPrefixes[strings.ToLower(prefix)]
	return reserved
}

// GetReservedPrefixOwner returns the owner of a reserved prefix
func (pe *PolicyEnforcer) GetReservedPrefixOwner(prefix string) string {
	return ReservedPrefixes[strings.ToLower(prefix)]
}

// ValidateTagReferences validates that anchor event tags reference valid events
// Single-character tags for relay indexability: n=names, c=connection, m=metadata, x=transaction
func (pe *PolicyEnforcer) ValidateTagReferences(event *nostr.Event) error {
	if event.Kind != 60600 {
		return nil
	}

	var hasD, hasNames, hasConnection, hasMetadata, hasTransaction bool

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}

		switch tag[0] {
		case "d":
			if tag[1] == "" {
				return fmt.Errorf("empty 'd' tag value")
			}
			hasD = true
		case "n":
			if tag[1] == "" {
				return fmt.Errorf("empty 'n' tag value (names reference)")
			}
			// Validate that n tag contains naddr address
			if !strings.HasPrefix(tag[1], "naddr1") {
				return fmt.Errorf("'n' tag must contain naddr address (got '%s')", tag[1])
			}
			// Try to decode to ensure it's valid
			if _, _, err := nip19.Decode(tag[1]); err != nil {
				return fmt.Errorf("invalid naddr in 'n' tag: %v", err)
			}
			hasNames = true
		case "c":
			if tag[1] == "" {
				return fmt.Errorf("empty 'c' tag value (connection reference)")
			}
			// Validate that c tag contains naddr address
			if !strings.HasPrefix(tag[1], "naddr1") {
				return fmt.Errorf("'c' tag must contain naddr address (got '%s')", tag[1])
			}
			// Try to decode to ensure it's valid
			if _, _, err := nip19.Decode(tag[1]); err != nil {
				return fmt.Errorf("invalid naddr in 'c' tag: %v", err)
			}
			hasConnection = true
		case "m":
			if tag[1] == "" {
				return fmt.Errorf("empty 'm' tag value (metadata reference)")
			}
			// Validate that m tag contains naddr address
			if !strings.HasPrefix(tag[1], "naddr1") {
				return fmt.Errorf("'m' tag must contain naddr address (got '%s')", tag[1])
			}
			// Try to decode to ensure it's valid
			if _, _, err := nip19.Decode(tag[1]); err != nil {
				return fmt.Errorf("invalid naddr in 'm' tag: %v", err)
			}
			hasMetadata = true
		case "x":
			if tag[1] == "" {
				return fmt.Errorf("empty 'x' tag value (transaction ID)")
			}
			if len(tag[1]) != 64 {
				return fmt.Errorf("invalid transaction ID length: expected 64, got %d", len(tag[1]))
			}
			hasTransaction = true
		}
	}

	if !hasD {
		return fmt.Errorf("missing required 'd' tag")
	}
	if !hasNames {
		return fmt.Errorf("missing required 'n' tag (names reference)")
	}
	if !hasConnection {
		return fmt.Errorf("missing required 'c' tag (connection reference)")
	}
	if !hasMetadata {
		return fmt.Errorf("missing required 'm' tag (metadata reference)")
	}
	if !hasTransaction {
		return fmt.Errorf("missing required 'x' tag (transaction ID)")
	}

	return nil
}

// ShouldCheckReorg determines if a reorg check should be performed
func (pe *PolicyEnforcer) ShouldCheckReorg(currentDNNBlock int64) bool {
	return currentDNNBlock > 0 && currentDNNBlock%ReorgCheckInterval == 0
}

// GetReorgCheckRange returns the Bitcoin block range to check for reorgs
func (pe *PolicyEnforcer) GetReorgCheckRange(currentBitcoinBlock int64) (start, end int64) {
	end = currentBitcoinBlock
	start = currentBitcoinBlock - ReorgLookback
	if start < pe.genesisBlock {
		start = pe.genesisBlock
	}
	return start, end
}

// Transaction represents a Bitcoin transaction for validation
type Transaction struct {
	TxID            string
	InputAddresses  []string
	OutputAddresses []string
	FeeRate         int    // satoshis per vByte
	Address         string // The single address (for self-transfer)
	EventPubkey     string // The Nostr event author's pubkey
}

// AddressSecurity provides security checks for Bitcoin addresses
type AddressSecurity struct {
	enforcer *PolicyEnforcer
}

// NewAddressSecurity creates a new address security checker
func NewAddressSecurity(enforcer *PolicyEnforcer) *AddressSecurity {
	return &AddressSecurity{enforcer: enforcer}
}

// ValidateBitcoinAddress validates a Bitcoin address format
func (as *AddressSecurity) ValidateBitcoinAddress(address string) error {
	_, err := btcutil.DecodeAddress(address, as.enforcer.network)
	if err != nil {
		return fmt.Errorf("invalid Bitcoin address: %w", err)
	}
	return nil
}

// StoredBitcoinAddress represents a stored Bitcoin address for validation
type StoredBitcoinAddress struct {
	Address   string
	Pubkey    string
	CreatedAt int64
}

// ValidateEventAuthor validates that an event author has a matching Bitcoin address
func (pe *PolicyEnforcer) ValidateEventAuthor(event *nostr.Event, storedAddresses []StoredBitcoinAddress) error {
	// Only check for kinds 61600, 62600, 63600
	if event.Kind != 61600 && event.Kind != 62600 && event.Kind != 63600 {
		return nil
	}

	// Check if the event author's pubkey has a matching Bitcoin address in storage
	for _, stored := range storedAddresses {
		if stored.Pubkey == event.PubKey {
			// Found matching pubkey with stored Bitcoin address
			return nil
		}
	}

	return fmt.Errorf("event author has no matching Bitcoin address in storage")
}

// RateLimitResult contains the result of rate limit enforcement
type RateLimitResult struct {
	EventsToDelete  []string // Event IDs to delete before storing
	DTagsToDelete   []string // D-tags to fully delete before storing
	VersionExceeded bool     // True if version limit was exceeded
	DTagExceeded    bool     // True if d-tag limit was exceeded
}

// RateLimitChecker interface for database operations needed for rate limiting
type RateLimitChecker interface {
	CountEventVersions(pubkey string, kind int, dTag string) (int, error)
	CountDTagsForKind(pubkey string, kind int) (int, error)
	GetOldestEventVersion(pubkey string, kind int, dTag string) (string, error)
	GetOldestDTagForKind(pubkey string, kind int) (string, error)
	IsDTagReferencedByAnchor(pubkey string, kind int, dTag string) (bool, error)
}

// EnforceRateLimits checks rate limits and returns events/d-tags to delete
// This should be called BEFORE storing a new event
func (pe *PolicyEnforcer) EnforceRateLimits(event *nostr.Event, db RateLimitChecker) (*RateLimitResult, error) {
	result := &RateLimitResult{
		EventsToDelete: []string{},
		DTagsToDelete:  []string{},
	}

	// Only apply to DNN event kinds
	if event.Kind < 60600 || event.Kind > 65600 {
		return result, nil
	}

	// Extract d-tag
	dTag := pe.extractDTag(event)
	if dTag == "" {
		return result, nil // No d-tag, can't enforce limits
	}

	pubkey := event.PubKey
	kind := event.Kind

	// Check 1: Version limit per d-tag
	versionCount, err := db.CountEventVersions(pubkey, kind, dTag)
	if err != nil {
		return result, err
	}

	if versionCount >= MaxVersionsPerDTag {
		// Need to delete oldest version
		oldestID, err := db.GetOldestEventVersion(pubkey, kind, dTag)
		if err != nil {
			return result, err
		}
		if oldestID != "" {
			result.EventsToDelete = append(result.EventsToDelete, oldestID)
			result.VersionExceeded = true
		}
	}

	// Check 2: D-tag limit per kind (only if this is a NEW d-tag)
	// We need to check if this d-tag already exists for this pubkey/kind
	existingCount, _ := db.CountEventVersions(pubkey, kind, dTag)
	if existingCount == 0 {
		// This is a new d-tag, check if we're at the limit
		dTagCount, err := db.CountDTagsForKind(pubkey, kind)
		if err != nil {
			return result, err
		}

		if dTagCount >= MaxDTagsPerKind {
			// Need to delete oldest d-tag entirely
			oldestDTag, err := db.GetOldestDTagForKind(pubkey, kind)
			if err != nil {
				return result, err
			}
			if oldestDTag != "" {
				// Check if the oldest d-tag is referenced by any anchor
				isReferenced, err := db.IsDTagReferencedByAnchor(pubkey, kind, oldestDTag)
				if err != nil {
					return result, err
				}
				if !isReferenced {
					// Safe to delete - not referenced by any anchor
					result.DTagsToDelete = append(result.DTagsToDelete, oldestDTag)
					result.DTagExceeded = true
				}
				// If referenced, skip deletion - let user exceed limit to preserve anchor integrity
			}
		}
	}

	return result, nil
}

// extractDTag extracts the d-tag from an event
func (pe *PolicyEnforcer) extractDTag(event *nostr.Event) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			return tag[1]
		}
	}
	return ""
}
