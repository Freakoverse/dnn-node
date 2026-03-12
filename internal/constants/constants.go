package constants

// Network modes
const (
	NetworkMainnet = "mainnet"
	NetworkTestnet = "testnet"
	NetworkDev     = "dev"
)

// Genesis block configuration
// The first DNN block (block 0) is anchored to this Bitcoin block
const (
	// Production genesis (when Bitcoin reaches block 1,000,000)
	GenesisBlockMainnet = 940000

	// Testnet genesis (Bitcoin block 932,300)
	GenesisBlockTestnet = 940000

	// Development genesis (for faster local testing)
	GenesisBlockDev = 940000
)

// Fee rate policy (in satoshis per vByte)
// Set to 1 sat/vByte - the lowest possible value to prevent future changes
// from affecting existing name registrations. Cannot be decreased further.
const (
	// Production minimum fee rate (official DNN standard)
	MinFeeRateMainnet = 1

	// Testing minimum fee rate (matches production for consistency)
	MinFeeRateTestnet = 1

	// Development minimum fee rate (matches production)
	MinFeeRateDev = 1
)

// Event size limits (in bytes)
const (
	MaxSizeKind60600 = 10 * 1024 // 10 KB - Anchor events
	MaxSizeKind61600 = 10 * 1024 // 10 KB - Name events
	MaxSizeKind62600 = 10 * 1024 // 10 KB - Connection events
	MaxSizeKind63600 = 50 * 1024 // 50 KB - Metadata events
)

// Reorg detection policy
const (
	ReorgCheckInterval = 144  // Check every 144 DNN blocks (~1 day)
	ReorgLookback      = 1008 // Look back 1008 Bitcoin blocks (7 days)
)

// Event rate limits (per npub)
const (
	MaxVersionsPerDTag = 10 // Max versions of same d-tag per kind per npub
	MaxDTagsPerKind    = 5  // Max unique d-tags per kind per npub
)

// Encoder configuration
const (
	DefaultBlocksPerBatch = 5       // Blocks per encoded batch
	DefaultTxMax          = 1000000 // Max transaction position
)

// Reserved prefixes for blockchain names
var ReservedPrefixes = map[string]string{
	"n": "DNN",     // Reserved for DNN system
	"b": "Bitcoin", // Reserved for Bitcoin
	// Future: "e" for Ethereum Classic, "d" for Dogecoin, etc.
}

// Reserved system addresses
var ReservedAddresses = map[string]string{
	"n0.0":       "DNN Node Registry",
	"b1m.0":      "Bitcoin Node Registry",
	"b1000000.0": "Bitcoin Node Registry", // Alternative notation
}

// GetGenesisBlock returns the genesis block for the given network mode
func GetGenesisBlock(network string) int64 {
	switch network {
	case NetworkMainnet:
		return GenesisBlockMainnet
	case NetworkTestnet:
		return GenesisBlockTestnet
	case NetworkDev:
		return GenesisBlockDev
	default:
		return GenesisBlockTestnet // Default to testnet for safety
	}
}

// GetMinFeeRate returns the minimum fee rate for the given network mode
func GetMinFeeRate(network string) int {
	switch network {
	case NetworkMainnet:
		return MinFeeRateMainnet
	case NetworkTestnet:
		return MinFeeRateTestnet
	case NetworkDev:
		return MinFeeRateDev
	default:
		return MinFeeRateTestnet // Default to testnet for safety
	}
}
