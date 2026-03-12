package bitcoin

// BlockchainProvider defines the interface for blockchain data access
// Both Client (RPC) and P2PClient implement these methods
type BlockchainProvider interface {
	// GetBlockCount returns the current block height
	GetBlockCount() (int64, error)

	// GetBlockHash returns the block hash for a given height
	GetBlockHash(height int64) (string, error)

	// GetBlock returns block data for a given hash
	GetBlock(hash string) (*Block, error)
}
