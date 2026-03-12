package reorg

import (
	"fmt"
	"log"
	"time"

	"dnn-node/internal/bitcoin"
	"dnn-node/internal/database"
	"dnn-node/internal/policy"
)

// ReorgHandler handles blockchain reorganizations
type ReorgHandler struct {
	db         *database.Database
	blockchain bitcoin.BlockchainProvider
	policy     *policy.PolicyEnforcer
}

// NewReorgHandler creates a new reorg handler
// blockchain can be either *bitcoin.Client (RPC) or *bitcoin.P2PClient
func NewReorgHandler(db *database.Database, blockchain bitcoin.BlockchainProvider, networkMode string) *ReorgHandler {
	return &ReorgHandler{
		db:         db,
		blockchain: blockchain,
		policy:     policy.NewPolicyEnforcer(networkMode),
	}
}

// CheckAndHandleReorg checks for blockchain reorganizations and handles them
// According to policy: Every 144 DNN blocks, check past 1008 Bitcoin blocks (7 days)
func (rh *ReorgHandler) CheckAndHandleReorg(currentDNNBlock int64) error {
	// Check if we should perform a reorg check
	if !rh.policy.ShouldCheckReorg(currentDNNBlock) {
		return nil
	}

	log.Printf("Performing reorg check at DNN block %d", currentDNNBlock)

	// Get current Bitcoin block height
	currentBitcoinBlock, err := rh.blockchain.GetBlockCount()
	if err != nil {
		return fmt.Errorf("failed to get current Bitcoin block: %w", err)
	}

	// Get the range to check (past 1008 blocks)
	startBlock, endBlock := rh.policy.GetReorgCheckRange(currentBitcoinBlock)

	log.Printf("Checking Bitcoin blocks %d to %d for reorgs", startBlock, endBlock)

	// Verify each block hash matches our stored hash
	reorgDetected := false
	reorgStartBlock := int64(0)

	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		// Get stored hash from database
		storedHash, err := rh.db.GetBitcoinBlockHash(blockNum)
		if err != nil {
			// Block not in our database yet, skip
			continue
		}

		// Get current hash from Bitcoin node
		currentHash, err := rh.blockchain.GetBlockHash(blockNum)
		if err != nil {
			log.Printf("Warning: Failed to get hash for block %d: %v", blockNum, err)
			continue
		}

		// Compare hashes
		if storedHash != currentHash {
			log.Printf("REORG DETECTED at block %d: stored=%s, current=%s",
				blockNum, storedHash, currentHash)
			reorgDetected = true
			if reorgStartBlock == 0 || blockNum < reorgStartBlock {
				reorgStartBlock = blockNum
			}
		}
	}

	if !reorgDetected {
		log.Println("No reorganization detected")
		// Update last reorg check time
		return rh.db.UpdateReorgCheckTime(time.Now())
	}

	// Handle the reorganization
	log.Printf("Handling reorganization starting from block %d", reorgStartBlock)
	return rh.handleReorg(reorgStartBlock)
}

// handleReorg handles a detected blockchain reorganization
func (rh *ReorgHandler) handleReorg(startBlock int64) error {
	log.Printf("Starting reorg recovery from Bitcoin block %d", startBlock)

	// Begin transaction for atomic updates
	tx, err := rh.db.BeginTransaction()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Mark all DNN blocks after the reorg point as invalid
	dnnStartBlock := startBlock - rh.policy.GetGenesisBlock()
	if dnnStartBlock < 0 {
		dnnStartBlock = 0
	}

	if err := rh.db.InvalidateDNNBlocksAfter(tx, dnnStartBlock); err != nil {
		return fmt.Errorf("failed to invalidate DNN blocks: %w", err)
	}

	// 2. Remove anchor events for invalidated blocks
	if err := rh.db.RemoveAnchorsAfterBlock(tx, dnnStartBlock); err != nil {
		return fmt.Errorf("failed to remove invalid anchors: %w", err)
	}

	// 3. Rescan from the reorg point
	log.Printf("Rescanning from block %d", startBlock)

	currentHeight, err := rh.blockchain.GetBlockCount()
	if err != nil {
		return fmt.Errorf("failed to get current block height: %w", err)
	}

	for blockNum := startBlock; blockNum <= currentHeight; blockNum++ {
		// Get block hash
		blockHash, err := rh.blockchain.GetBlockHash(blockNum)
		if err != nil {
			return fmt.Errorf("failed to get block hash for %d: %w", blockNum, err)
		}

		// Get block data
		block, err := rh.blockchain.GetBlock(blockHash)
		if err != nil {
			return fmt.Errorf("failed to get block %d: %w", blockNum, err)
		}

		// Update stored block hash
		dnnBlockNum := blockNum - rh.policy.GetGenesisBlock()
		if dnnBlockNum >= 0 {
			if err := rh.db.UpdateDNNBlock(tx, dnnBlockNum, blockNum, blockHash, block.Time); err != nil {
				return fmt.Errorf("failed to update DNN block %d: %w", dnnBlockNum, err)
			}

			// Process transactions in the block
			if err := rh.processBlockTransactions(tx, block, dnnBlockNum); err != nil {
				log.Printf("Error processing block %d transactions: %v", blockNum, err)
			}
		}

		// Log progress
		if blockNum%100 == 0 {
			log.Printf("Rescan progress: %d/%d blocks", blockNum-startBlock, currentHeight-startBlock)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit reorg recovery: %w", err)
	}

	// Update last reorg check time
	if err := rh.db.UpdateReorgCheckTime(time.Now()); err != nil {
		log.Printf("Warning: Failed to update reorg check time: %v", err)
	}

	log.Printf("Reorg recovery completed. Rescanned %d blocks", currentHeight-startBlock+1)
	return nil
}

// processBlockTransactions processes transactions in a block during reorg recovery
func (rh *ReorgHandler) processBlockTransactions(tx *database.Transaction, block *bitcoin.Block, dnnBlockNum int64) error {
	position := 0

	for _, btxn := range block.Transactions {
		// Check if transaction meets DNN criteria
		if rh.isValidDNNTransaction(&btxn) {
			position++

			// Check if we have a corresponding anchor event for this transaction
			anchor, err := rh.db.GetAnchorByTransactionID(tx, btxn.TxID)
			if err != nil {
				log.Printf("Failed to get anchor for tx %s: %v", btxn.TxID, err)
				continue
			}

			if anchor != nil {
				// Update the anchor's block and position
				if err := rh.db.UpdateAnchorPosition(tx, anchor.ID, dnnBlockNum, block.Height, position); err != nil {
					log.Printf("Failed to update anchor position: %v", err)
				}
			}
		}
	}

	return nil
}

// isValidDNNTransaction checks if a transaction meets DNN criteria
func (rh *ReorgHandler) isValidDNNTransaction(tx *bitcoin.Transaction) bool {
	// Check for self-transfer with minimum fee
	// This is a simplified check - full implementation would verify all criteria

	if len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		return false
	}

	// Check if it's a self-transfer (simplified)
	// In reality, we'd need to look up the input addresses
	// and verify they match the output addresses

	// Check minimum fee rate (1 sat/vB)
	// This would require calculating the transaction size in vBytes

	return true // Placeholder
}

// ReorgStats contains statistics about reorg checks
type ReorgStats struct {
	LastCheck           time.Time `json:"last_check"`
	LastReorgDetected   time.Time `json:"last_reorg_detected"`
	ReorgsDetected      int       `json:"reorgs_detected"`
	BlocksRescanned     int64     `json:"blocks_rescanned"`
	CurrentDNNBlock     int64     `json:"current_dnn_block"`
	CurrentBitcoinBlock int64     `json:"current_bitcoin_block"`
}

// GetReorgStats returns statistics about reorg checks
func (rh *ReorgHandler) GetReorgStats() (*ReorgStats, error) {
	stats := &ReorgStats{}

	// Get last check time
	lastCheck, err := rh.db.GetSyncState("last_reorg_check")
	if err == nil && lastCheck != "" {
		if t, err := time.Parse(time.RFC3339, lastCheck); err == nil {
			stats.LastCheck = t
		}
	}

	// Get last reorg detected time
	lastReorg, err := rh.db.GetSyncState("last_reorg_detected")
	if err == nil && lastReorg != "" {
		if t, err := time.Parse(time.RFC3339, lastReorg); err == nil {
			stats.LastReorgDetected = t
		}
	}

	// Get reorg count
	reorgCount, err := rh.db.GetSyncState("reorg_count")
	if err == nil && reorgCount != "" {
		fmt.Sscanf(reorgCount, "%d", &stats.ReorgsDetected)
	}

	// Get current block heights
	currentDNN, err := rh.db.GetSyncState("last_dnn_block")
	if err == nil && currentDNN != "" {
		fmt.Sscanf(currentDNN, "%d", &stats.CurrentDNNBlock)
	}

	currentBTC, err := rh.db.GetSyncState("last_bitcoin_block")
	if err == nil && currentBTC != "" {
		fmt.Sscanf(currentBTC, "%d", &stats.CurrentBitcoinBlock)
	}

	return stats, nil
}
