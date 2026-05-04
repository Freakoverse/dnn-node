package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"dnn-node/internal/encoder"

	"github.com/nbd-wtf/go-nostr/nip19"
)

// QueryBuilder helps construct complex database queries
type QueryBuilder struct {
	db *Database
}

// NewQueryBuilder creates a new query builder
func NewQueryBuilder(db *Database) *QueryBuilder {
	return &QueryBuilder{db: db}
}

// GetRandomBitcoinTransaction returns a random Bitcoin transaction from the database.
// Used for peer verification spot-checks.
func (qb *QueryBuilder) GetRandomBitcoinTransaction() (*BitcoinTransactionRecord, error) {
	query := `
		SELECT
			transaction_id,
			bitcoin_block_number,
			dnn_block_number,
			position,
			bitcoin_address,
			fee_rate,
			discovered_at,
			has_anchor_event,
			COALESCE(anchor_event_id, '') as anchor_event_id
		FROM bitcoin_transactions
		ORDER BY RANDOM()
		LIMIT 1
	`

	var record BitcoinTransactionRecord
	var discoveredAt int64
	err := qb.db.db.QueryRow(query).Scan(
		&record.TransactionID,
		&record.BitcoinBlock,
		&record.DNNBlock,
		&record.Position,
		&record.BitcoinAddress,
		&record.FeeRate,
		&discoveredAt,
		&record.HasAnchorEvent,
		&record.AnchorEventID,
	)

	if err != nil {
		return nil, err
	}

	record.DiscoveredAt = time.Unix(discoveredAt, 0)
	return &record, nil
}

// GetBitcoinTransactionByID retrieves a Bitcoin transaction by its transaction ID
func (qb *QueryBuilder) GetBitcoinTransactionByID(txID string) (*BitcoinTransactionRecord, error) {
	query := `
		SELECT
			transaction_id,
			bitcoin_block_number,
			dnn_block_number,
			position,
			bitcoin_address,
			fee_rate,
			discovered_at,
			has_anchor_event,
			COALESCE(anchor_event_id, '') as anchor_event_id
		FROM bitcoin_transactions
		WHERE transaction_id = ?
		LIMIT 1
	`

	var record BitcoinTransactionRecord
	var discoveredAt int64
	err := qb.db.db.QueryRow(query, txID).Scan(
		&record.TransactionID,
		&record.BitcoinBlock,
		&record.DNNBlock,
		&record.Position,
		&record.BitcoinAddress,
		&record.FeeRate,
		&discoveredAt,
		&record.HasAnchorEvent,
		&record.AnchorEventID,
	)

	if err != nil {
		return nil, err
	}

	record.DiscoveredAt = time.Unix(discoveredAt, 0)
	return &record, nil
}

// HasValidTransactionForAddress checks if any Bitcoin transaction exists for the given address
// Used to validate that a npub owner can publish DNN events
func (qb *QueryBuilder) HasValidTransactionForAddress(bitcoinAddress string) (bool, error) {
	var count int
	err := qb.db.db.QueryRow(
		"SELECT COUNT(*) FROM bitcoin_transactions WHERE bitcoin_address = ?",
		bitcoinAddress,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check transaction: %w", err)
	}
	return count > 0, nil
}

// HasValidTransactionForAddresses checks if any Bitcoin transaction exists for any of the given addresses
// Used to validate that a npub owner can publish DNN events (checks all derived address types)
func (qb *QueryBuilder) HasValidTransactionForAddresses(addresses []string) (bool, error) {
	if len(addresses) == 0 {
		return false, nil
	}
	placeholders := strings.Repeat("?,", len(addresses))
	placeholders = placeholders[:len(placeholders)-1]

	query := "SELECT COUNT(*) FROM bitcoin_transactions WHERE bitcoin_address IN (" + placeholders + ")"
	args := make([]interface{}, len(addresses))
	for i, addr := range addresses {
		args[i] = addr
	}

	var count int
	err := qb.db.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check transactions: %w", err)
	}
	return count > 0, nil
}

// SearchResult represents a search result
type SearchResult struct {
	Name         string    `json:"name"`
	Pubkey       string    `json:"pubkey"`
	DNNBlock     int64     `json:"dnn_block"`
	BitcoinBlock int64     `json:"bitcoin_block"`
	Position     int       `json:"position"`
	CreatedAt    time.Time `json:"created_at"`
	Encoded      string    `json:"encoded,omitempty"`
}

// BlockInfo represents information about a DNN block
type BlockInfo struct {
	BlockNumber        int64      `json:"block_number"`
	BitcoinBlockNumber int64      `json:"bitcoin_block_number"`
	BitcoinBlockHash   string     `json:"bitcoin_block_hash"`
	AnchorCount        int        `json:"anchor_count"`
	CreatedAt          time.Time  `json:"created_at"`
	SyncedAt           *time.Time `json:"synced_at,omitempty"`
}

// Stats represents node statistics
type Stats struct {
	TotalNames         int        `json:"total_names"`
	TotalAnchors       int        `json:"total_anchors"`
	TotalBitcoinTxs    int        `json:"total_bitcoin_txs"`
	TotalPendingTxs    int        `json:"total_pending_txs"`
	LatestDNNBlock     int64      `json:"latest_dnn_block"`
	LatestBitcoinBlock int64      `json:"latest_bitcoin_block"`
	LastSync           *time.Time `json:"last_sync,omitempty"`
	DatabaseSize       int64      `json:"database_size_bytes"`
}

// SearchNames searches for names with various filters
func (qb *QueryBuilder) SearchNames(query string, limit int, offset int) ([]SearchResult, error) {
	// First, search name_events for matching names
	nameQuery := `
		SELECT DISTINCT primary_name, pubkey, d_tag
		FROM name_events
		WHERE primary_name LIKE ?
		ORDER BY primary_name ASC
	`

	nameRows, err := qb.db.db.Query(nameQuery, "%"+query+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to search names: %w", err)
	}
	defer nameRows.Close()

	// Collect matching name events
	type nameMatch struct {
		name   string
		pubkey string
		dTag   string
	}
	var matches []nameMatch

	for nameRows.Next() {
		var m nameMatch
		if err := nameRows.Scan(&m.name, &m.pubkey, &m.dTag); err != nil {
			continue
		}
		matches = append(matches, m)
	}

	// Now find anchors that reference these name events
	var results []SearchResult
	seen := make(map[string]bool) // Deduplicate by anchor ID

	for _, match := range matches {
		// Find anchors whose name_event_ref naddr points to this name event
		// We'll fetch all anchors and decode their naddr to find matches
		anchorQuery := `
			SELECT id, pubkey, dnn_block_number, bitcoin_block_number, position, created_at, name_event_ref
			FROM anchor_events
			ORDER BY dnn_block_number DESC, position ASC
		`

		anchorRows, err := qb.db.db.Query(anchorQuery)
		if err != nil {
			continue
		}

		for anchorRows.Next() {
			var anchorID, anchorPubkey, nameEventRef string
			var dnnBlock, bitcoinBlock int64
			var position int
			var createdAt int64

			if err := anchorRows.Scan(&anchorID, &anchorPubkey, &dnnBlock, &bitcoinBlock, &position, &createdAt, &nameEventRef); err != nil {
				continue
			}

			// Skip if already seen
			if seen[anchorID] {
				continue
			}

			// Decode naddr to get coordinates
			_, _, refPubkey, refDTag, err := extractEventIDFromNaddr(nameEventRef)
			if err != nil {
				continue
			}

			// Check if this anchor references the name we found
			if refPubkey == match.pubkey && refDTag == match.dTag {
				seen[anchorID] = true
				results = append(results, SearchResult{
					Name:         match.name,
					Pubkey:       anchorPubkey,
					DNNBlock:     dnnBlock,
					BitcoinBlock: bitcoinBlock,
					Position:     position,
					CreatedAt:    time.Unix(createdAt, 0),
				})

				// Check if we've collected enough results
				if len(results) >= offset+limit {
					break
				}
			}
		}
		anchorRows.Close()

		if len(results) >= offset+limit {
			break
		}
	}

	// Apply offset and limit
	if offset >= len(results) {
		return []SearchResult{}, nil
	}

	end := offset + limit
	if end > len(results) {
		end = len(results)
	}

	return results[offset:end], nil
}

// GetNameHistory retrieves the history of a specific name
func (qb *QueryBuilder) GetNameHistory(name string) ([]SearchResult, error) {
	// First, find all name_events with this name (could be multiple pubkeys)
	nameQuery := `
		SELECT pubkey, d_tag
		FROM name_events
		WHERE primary_name = ?
	`

	nameRows, err := qb.db.db.Query(nameQuery, name)
	if err != nil {
		return nil, fmt.Errorf("failed to query name events: %w", err)
	}
	defer nameRows.Close()

	// Collect matching name events
	type nameMatch struct {
		pubkey string
		dTag   string
	}
	var matches []nameMatch

	for nameRows.Next() {
		var m nameMatch
		if err := nameRows.Scan(&m.pubkey, &m.dTag); err != nil {
			continue
		}
		matches = append(matches, m)
	}

	if len(matches) == 0 {
		return []SearchResult{}, nil
	}

	// Now find all anchors that reference these name events
	var results []SearchResult

	for _, match := range matches {
		// Find anchors whose name_event_ref points to this name event
		anchorQuery := `
			SELECT id, pubkey, dnn_block_number, bitcoin_block_number, position, created_at, name_event_ref
			FROM anchor_events
			ORDER BY dnn_block_number DESC, position ASC
		`

		anchorRows, err := qb.db.db.Query(anchorQuery)
		if err != nil {
			continue
		}

		for anchorRows.Next() {
			var anchorID, anchorPubkey, nameEventRef string
			var dnnBlock, bitcoinBlock int64
			var position int
			var createdAt int64

			if err := anchorRows.Scan(&anchorID, &anchorPubkey, &dnnBlock, &bitcoinBlock, &position, &createdAt, &nameEventRef); err != nil {
				continue
			}

			// Decode naddr to get coordinates
			_, _, refPubkey, refDTag, err := extractEventIDFromNaddr(nameEventRef)
			if err != nil {
				continue
			}

			// Check if this anchor references the name we found
			if refPubkey == match.pubkey && refDTag == match.dTag {
				results = append(results, SearchResult{
					Name:         name,
					Pubkey:       anchorPubkey,
					DNNBlock:     dnnBlock,
					BitcoinBlock: bitcoinBlock,
					Position:     position,
					CreatedAt:    time.Unix(createdAt, 0),
				})
			}
		}
		anchorRows.Close()
	}

	return results, nil
}

// GetBlockInfo retrieves information about a specific DNN block
func (qb *QueryBuilder) GetBlockInfo(blockNumber int64) (*BlockInfo, error) {
	query := `
		SELECT
			block_number,
			bitcoin_block_number,
			bitcoin_block_hash,
			created_at,
			synced_at,
			(SELECT COUNT(*) FROM anchor_events WHERE dnn_block_number = ?) as anchor_count
		FROM dnn_blocks
		WHERE block_number = ?
	`

	var info BlockInfo
	var createdAt int64
	var syncedAt sql.NullInt64

	err := qb.db.db.QueryRow(query, blockNumber, blockNumber).Scan(
		&info.BlockNumber,
		&info.BitcoinBlockNumber,
		&info.BitcoinBlockHash,
		&createdAt,
		&syncedAt,
		&info.AnchorCount,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get block info: %w", err)
	}

	info.CreatedAt = time.Unix(createdAt, 0)
	if syncedAt.Valid {
		t := time.Unix(syncedAt.Int64, 0)
		info.SyncedAt = &t
	}

	return &info, nil
}

// GetBlockAnchors retrieves all anchors in a specific block
func (qb *QueryBuilder) GetBlockAnchors(blockNumber int64) ([]AnchorRecord, error) {
	query := `
		SELECT
			id, pubkey, created_at,
			bitcoin_block_number, dnn_block_number, position,
			name_event_ref, connection_event_ref, metadata_event_ref
		FROM anchor_events
		WHERE dnn_block_number = ?
		ORDER BY position ASC
	`

	rows, err := qb.db.db.Query(query, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get block anchors: %w", err)
	}
	defer rows.Close()

	var anchors []AnchorRecord
	for rows.Next() {
		var id, pubkey string
		var createdAt int64
		var bitcoinBlock, dnnBlock int64
		var position int
		var nameEventRef, connEventRef, metaEventRef string

		err := rows.Scan(
			&id, &pubkey, &createdAt,
			&bitcoinBlock, &dnnBlock, &position,
			&nameEventRef, &connEventRef, &metaEventRef,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan anchor: %w", err)
		}

		// Decode naddr references and fetch referenced events
		var nameContent, connectionContent, metadataContent string
		var primaryName, otherNames string

		// Fetch name event
		_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
		if err == nil {
			_ = qb.db.db.QueryRow(
				"SELECT primary_name, other_names, content FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				namePubkey, nameDTag,
			).Scan(&primaryName, &otherNames, &nameContent)
		}

		// Fetch connection event - use both pubkey AND d_tag
		_, _, connPubkey, connDTag, err := extractEventIDFromNaddr(connEventRef)
		if err == nil {
			_ = qb.db.db.QueryRow(
				"SELECT content FROM connection_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				connPubkey, connDTag,
			).Scan(&connectionContent)
		}

		// Fetch metadata event - use both pubkey AND d_tag, get latest version
		_, _, metaPubkey, metaDTag, err := extractEventIDFromNaddr(metaEventRef)
		if err == nil {
			if metaDTag != "" {
				_ = qb.db.db.QueryRow(
					"SELECT content FROM metadata_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
					metaPubkey, metaDTag,
				).Scan(&metadataContent)
			} else {
				_ = qb.db.db.QueryRow(
					"SELECT content FROM metadata_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
					metaPubkey,
				).Scan(&metadataContent)
			}
		}

		a := AnchorRecord{
			ID:                id,
			Pubkey:            pubkey,
			CreatedAt:         createdAt,
			BitcoinBlock:      bitcoinBlock,
			DNNBlock:          dnnBlock,
			Position:          position,
			Name:              primaryName,
			OtherNames:        otherNames,
			NameContent:       nameContent,
			ConnectionContent: connectionContent,
			MetadataContent:   metadataContent,
		}

		anchors = append(anchors, a)
	}

	return anchors, nil
}

// GetUserNames retrieves all names registered by a specific pubkey
func (qb *QueryBuilder) GetUserNames(pubkey string) ([]SearchResult, error) {
	query := `
		SELECT
			id, dnn_block_number, bitcoin_block_number, position, created_at, name_event_ref
		FROM anchor_events
		WHERE pubkey = ?
		ORDER BY dnn_block_number DESC, position ASC
	`

	rows, err := qb.db.db.Query(query, pubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to get user names: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var id, nameEventRef string
		var dnnBlock, bitcoinBlock int64
		var position int
		var createdAt int64

		err := rows.Scan(
			&id,
			&dnnBlock,
			&bitcoinBlock,
			&position,
			&createdAt,
			&nameEventRef,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan result: %w", err)
		}

		// Decode naddr to get name event coordinates
		_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
		if err != nil {
			log.Printf("[QB] Failed to decode name_event_ref for anchor %s: %v", id[:8], err)
			continue
		}

		// Fetch the primary name
		var primaryName string
		err = qb.db.db.QueryRow(
			"SELECT primary_name FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
			namePubkey, nameDTag,
		).Scan(&primaryName)
		if err != nil {
			log.Printf("[QB] Name event not found for anchor %s: %v", id[:8], err)
			continue
		}

		results = append(results, SearchResult{
			Name:         primaryName,
			Pubkey:       pubkey,
			DNNBlock:     dnnBlock,
			BitcoinBlock: bitcoinBlock,
			Position:     position,
			CreatedAt:    time.Unix(createdAt, 0),
		})
	}

	return results, nil
}

// GetStats retrieves node statistics
func (qb *QueryBuilder) GetStats() (*Stats, error) {
	var stats Stats

	// Get total names
	err := qb.db.db.QueryRow("SELECT COUNT(DISTINCT d_tag) FROM name_events").Scan(&stats.TotalNames)
	if err != nil {
		return nil, fmt.Errorf("failed to get total names: %w", err)
	}

	// Get total anchors
	err = qb.db.db.QueryRow("SELECT COUNT(*) FROM anchor_events").Scan(&stats.TotalAnchors)
	if err != nil {
		return nil, fmt.Errorf("failed to get total anchors: %w", err)
	}

	// Get total Bitcoin transactions
	err = qb.db.db.QueryRow("SELECT COUNT(*) FROM bitcoin_transactions").Scan(&stats.TotalBitcoinTxs)
	if err != nil {
		return nil, fmt.Errorf("failed to get total Bitcoin transactions: %w", err)
	}

	// Get total pending transactions (without anchor events)
	err = qb.db.db.QueryRow("SELECT COUNT(*) FROM bitcoin_transactions WHERE has_anchor_event = 0").Scan(&stats.TotalPendingTxs)
	if err != nil {
		return nil, fmt.Errorf("failed to get total pending transactions: %w", err)
	}

	// Get latest blocks
	err = qb.db.db.QueryRow(`
		SELECT
			COALESCE(MAX(block_number), 0),
			COALESCE(MAX(bitcoin_block_number), 0)
		FROM dnn_blocks
	`).Scan(&stats.LatestDNNBlock, &stats.LatestBitcoinBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest blocks: %w", err)
	}

	// Get last sync time
	var lastSyncStr string
	err = qb.db.db.QueryRow("SELECT value FROM sync_state WHERE key = 'last_sync_time'").Scan(&lastSyncStr)
	if err == nil && lastSyncStr != "0" {
		lastSync, _ := time.Parse(time.RFC3339, lastSyncStr)
		stats.LastSync = &lastSync
	}

	// Get database size
	var pageCount, pageSize int64
	qb.db.db.QueryRow("PRAGMA page_count").Scan(&pageCount)
	qb.db.db.QueryRow("PRAGMA page_size").Scan(&pageSize)
	stats.DatabaseSize = pageCount * pageSize

	return &stats, nil
}

// ========== Awareness Database Functions (Enhanced: allow/neutral/block + categories) ==========

// GetLocalMarks retrieves all local awareness marks
func (qb *QueryBuilder) GetLocalMarks() ([]LocalMark, error) {
	query := `
		SELECT dnn_block, position, name, mark, category, reason, updated_at
		FROM awareness_marks_local
		ORDER BY updated_at DESC
	`

	rows, err := qb.db.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get local marks: %w", err)
	}
	defer rows.Close()

	var marks []LocalMark
	for rows.Next() {
		var m LocalMark
		var updatedAt sql.NullInt64
		err := rows.Scan(&m.DNNBlock, &m.Position, &m.Name, &m.Mark, &m.Category, &m.Reason, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan mark: %w", err)
		}
		if updatedAt.Valid {
			t := time.Unix(updatedAt.Int64, 0)
			m.UpdatedAt = &t
		}
		marks = append(marks, m)
	}

	return marks, nil
}

// AddLocalMark adds or updates a local awareness mark
func (qb *QueryBuilder) AddLocalMark(dnnBlock int64, position int, name, mark, category, reason string) error {
	if mark != "allow" && mark != "neutral" && mark != "block" {
		return fmt.Errorf("invalid mark: must be 'allow', 'neutral', or 'block'")
	}

	if category != "" && category != "malware" && category != "phishing" && category != "scam" && category != "adult" && category != "other" {
		return fmt.Errorf("invalid category: must be 'malware', 'phishing', 'scam', 'adult', 'other', or empty")
	}

	query := `
		INSERT OR REPLACE INTO awareness_marks_local
		(dnn_block, position, name, mark, category, reason, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
	`

	_, err := qb.db.db.Exec(query, dnnBlock, position, name, mark, category, reason)
	if err != nil {
		return fmt.Errorf("failed to add local mark: %w", err)
	}

	return nil
}

// DeleteLocalMark removes a local awareness mark (name="" for TLD-level)
func (qb *QueryBuilder) DeleteLocalMark(dnnBlock int64, position int, name string) error {
	query := `DELETE FROM awareness_marks_local WHERE dnn_block = ? AND position = ? AND name = ?`
	_, err := qb.db.db.Exec(query, dnnBlock, position, name)
	if err != nil {
		return fmt.Errorf("failed to delete local mark: %w", err)
	}
	return nil
}

// GetLocalMarkByID retrieves a specific local mark by DNN ID (name="" for TLD-level)
func (qb *QueryBuilder) GetLocalMarkByID(dnnBlock int64, position int, name string) (*LocalMark, error) {
	query := `
		SELECT dnn_block, position, name, mark, category, reason, updated_at
		FROM awareness_marks_local
		WHERE dnn_block = ? AND position = ? AND name = ?
	`

	var m LocalMark
	var updatedAt sql.NullInt64
	err := qb.db.db.QueryRow(query, dnnBlock, position, name).Scan(
		&m.DNNBlock, &m.Position, &m.Name, &m.Mark, &m.Category, &m.Reason, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get local mark: %w", err)
	}
	if updatedAt.Valid {
		t := time.Unix(updatedAt.Int64, 0)
		m.UpdatedAt = &t
	}
	return &m, nil
}

// ClearLocalMarks removes all local awareness marks (for sync reset)
func (qb *QueryBuilder) ClearLocalMarks() error {
	_, err := qb.db.db.Exec("DELETE FROM awareness_marks_local")
	return err
}

// GetPeerMarks retrieves peer marks for a DNN ID
func (qb *QueryBuilder) GetPeerMarks(dnnBlock int64, position int) ([]PeerMark, error) {
	query := `
		SELECT dnn_block, position, name, peer_pubkey, mark, category, reason, received_at
		FROM awareness_marks_peers
		WHERE dnn_block = ? AND position = ?
		ORDER BY received_at DESC
	`

	rows, err := qb.db.db.Query(query, dnnBlock, position)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer marks: %w", err)
	}
	defer rows.Close()

	var marks []PeerMark
	for rows.Next() {
		var m PeerMark
		var receivedAt sql.NullInt64
		err := rows.Scan(&m.DNNBlock, &m.Position, &m.Name, &m.PeerPubkey, &m.Mark, &m.Category, &m.Reason, &receivedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan peer mark: %w", err)
		}
		if receivedAt.Valid {
			t := time.Unix(receivedAt.Int64, 0)
			m.ReceivedAt = &t
		}
		marks = append(marks, m)
	}

	return marks, nil
}

// PeerConsensus represents the aggregated peer consensus for a specific DNN ID.
// This is the data a client (browser/OS) uses to apply its own filter threshold.
type PeerConsensus struct {
	AllowCount   int            `json:"allow"`
	NeutralCount int            `json:"neutral"`
	BlockCount   int            `json:"block"`
	TotalPeers   int            `json:"total_peers"`
	Categories   map[string]int `json:"categories,omitempty"` // category â†’ count of block marks
}

// GetPeerConsensus returns aggregated peer consensus for a specific DNN ID.
// When name is empty, returns TLD-level consensus. When name is set, returns name-specific consensus.
// Includes mark counts and a category breakdown of block marks.
func (qb *QueryBuilder) GetPeerConsensus(dnnBlock int64, position int, name string) (*PeerConsensus, error) {
	// Get aggregated counts
	consensus := &PeerConsensus{
		Categories: make(map[string]int),
	}

	err := qb.db.db.QueryRow(`
		SELECT 
			COALESCE(SUM(CASE WHEN mark = 'allow' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN mark = 'neutral' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN mark = 'block' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM awareness_marks_peers
		WHERE dnn_block = ? AND position = ? AND name = ?
	`, dnnBlock, position, name).Scan(&consensus.AllowCount, &consensus.NeutralCount, &consensus.BlockCount, &consensus.TotalPeers)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer consensus: %w", err)
	}

	if consensus.TotalPeers == 0 {
		return consensus, nil
	}

	// Get category breakdown for block marks
	rows, err := qb.db.db.Query(`
		SELECT category, COUNT(*) as cnt
		FROM awareness_marks_peers
		WHERE dnn_block = ? AND position = ? AND name = ? AND mark = 'block' AND category != ''
		GROUP BY category
	`, dnnBlock, position, name)
	if err != nil {
		return consensus, nil // Return what we have, categories are supplemental
	}
	defer rows.Close()

	for rows.Next() {
		var cat string
		var cnt int
		if err := rows.Scan(&cat, &cnt); err == nil && cat != "" {
			consensus.Categories[cat] = cnt
		}
	}

	return consensus, nil
}

// GetAllPeerMarks retrieves all peer marks with aggregation
func (qb *QueryBuilder) GetAllPeerMarks() ([]PeerMarkAggregate, error) {
	query := `
		SELECT 
			dnn_block, position,
			SUM(CASE WHEN mark = 'allow' THEN 1 ELSE 0 END) as allow_count,
			SUM(CASE WHEN mark = 'neutral' THEN 1 ELSE 0 END) as neutral_count,
			SUM(CASE WHEN mark = 'block' THEN 1 ELSE 0 END) as block_count,
			COUNT(*) as total_peers
		FROM awareness_marks_peers
		GROUP BY dnn_block, position
		ORDER BY total_peers DESC
	`

	rows, err := qb.db.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer marks aggregate: %w", err)
	}
	defer rows.Close()

	var aggregates []PeerMarkAggregate
	for rows.Next() {
		var a PeerMarkAggregate
		err := rows.Scan(&a.DNNBlock, &a.Position, &a.AllowCount, &a.NeutralCount, &a.BlockCount, &a.TotalPeers)
		if err != nil {
			return nil, fmt.Errorf("failed to scan aggregate: %w", err)
		}
		aggregates = append(aggregates, a)
	}

	return aggregates, nil
}

// AddPeerMark adds a peer's awareness mark (used during peer sync)
func (qb *QueryBuilder) AddPeerMark(dnnBlock int64, position int, name, peerPubkey, mark, category, reason string) error {
	if mark != "allow" && mark != "neutral" && mark != "block" {
		return fmt.Errorf("invalid mark: must be 'allow', 'neutral', or 'block'")
	}

	query := `
		INSERT OR REPLACE INTO awareness_marks_peers
		(dnn_block, position, name, peer_pubkey, mark, category, reason, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
	`

	_, err := qb.db.db.Exec(query, dnnBlock, position, name, peerPubkey, mark, category, reason)
	if err != nil {
		return fmt.Errorf("failed to add peer mark: %w", err)
	}

	return nil
}

// ClearPeerMarksForPeer removes all awareness marks from a specific peer (for full re-sync)
func (qb *QueryBuilder) ClearPeerMarksForPeer(peerPubkey string) error {
	_, err := qb.db.db.Exec("DELETE FROM awareness_marks_peers WHERE peer_pubkey = ?", peerPubkey)
	return err
}

// GetPeerAdminPubkeys returns admin pubkeys of all active peer nodes (for awareness sync).
// Falls back to node_pubkey if admin_pubkey is not set.
func (qb *QueryBuilder) GetPeerAdminPubkeys() ([]string, error) {
	rows, err := qb.db.db.Query(`
		SELECT CASE WHEN admin_pubkey != '' THEN admin_pubkey ELSE node_pubkey END AS pubkey
		FROM peer_nodes
		WHERE is_verified = 1 AND fail_count < 4
		  AND COALESCE(admin_pubkey, node_pubkey, '') != ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pubkeys []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err == nil && pk != "" {
			pubkeys = append(pubkeys, pk)
		}
	}
	return pubkeys, nil
}

// GetAwarenessStats retrieves awareness database statistics
func (qb *QueryBuilder) GetAwarenessStats() (*AwarenessStats, error) {
	stats := &AwarenessStats{}

	// Count local marks
	err := qb.db.db.QueryRow(`
		SELECT 
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN mark = 'allow' THEN 1 ELSE 0 END), 0) as allow_count,
			COALESCE(SUM(CASE WHEN mark = 'neutral' THEN 1 ELSE 0 END), 0) as neutral_count,
			COALESCE(SUM(CASE WHEN mark = 'block' THEN 1 ELSE 0 END), 0) as block_count
		FROM awareness_marks_local
	`).Scan(&stats.LocalTotal, &stats.LocalAllow, &stats.LocalNeutral, &stats.LocalBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get local stats: %w", err)
	}

	// Count peer marks
	err = qb.db.db.QueryRow(`
		SELECT 
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN mark = 'allow' THEN 1 ELSE 0 END), 0) as allow_count,
			COALESCE(SUM(CASE WHEN mark = 'neutral' THEN 1 ELSE 0 END), 0) as neutral_count,
			COALESCE(SUM(CASE WHEN mark = 'block' THEN 1 ELSE 0 END), 0) as block_count
		FROM awareness_marks_peers
	`).Scan(&stats.PeerTotal, &stats.PeerAllow, &stats.PeerNeutral, &stats.PeerBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer stats: %w", err)
	}

	return stats, nil
}

// IsBlocked checks if a DNN ID (or specific name) should be blocked based on filter level.
// Resolution order: 1) TLD blocked â†’ blocked. 2) Name-specific blocked â†’ blocked. 3) Otherwise â†’ allowed.
// filterLevel: "off" = only explicit block, "security" = block + neutral(malware/phishing/scam),
// "strict" = block + neutral(any category)
// IsBlocked checks if a DNN ID (or specific name) has an explicit "block" mark set by the node operator.
// Resolution order: 1) TLD blocked -> blocked. 2) Name-specific blocked -> blocked. 3) Otherwise -> allowed.
// Note: Category-based filtering (security/strict) is a client-side responsibility, not the node's.
func (qb *QueryBuilder) IsBlocked(dnnBlock int64, position int, name string) (bool, error) {
	// Check TLD-level mark first (name = '')
	var mark string
	err := qb.db.db.QueryRow(`
		SELECT mark FROM awareness_marks_local
		WHERE dnn_block = ? AND position = ? AND name = ''
	`, dnnBlock, position).Scan(&mark)

	if err == nil {
		if mark == "block" {
			return true, nil
		}
	} else if err != sql.ErrNoRows {
		return false, fmt.Errorf("failed to check TLD mark: %w", err)
	}

	// Check name-specific mark (if a name was provided)
	if name != "" {
		err = qb.db.db.QueryRow(`
			SELECT mark FROM awareness_marks_local
			WHERE dnn_block = ? AND position = ? AND name = ?
		`, dnnBlock, position, name).Scan(&mark)

		if err == nil {
			return mark == "block", nil
		} else if err != sql.ErrNoRows {
			return false, fmt.Errorf("failed to check name mark: %w", err)
		}
	}

	return false, nil
}

// shouldBlock determines if a mark+category combination should be blocked at the given filter level
func shouldBlock(mark, category, filterLevel string) bool {
	// "block" mark is always enforced regardless of filter level
	if mark == "block" {
		return true
	}

	// "allow" mark is never blocked regardless of filter level
	if mark == "allow" {
		return false
	}

	// "neutral" mark â€” depends on filter level and category
	if mark == "neutral" {
		switch filterLevel {
		case "security":
			// Block neutral entries with security-related categories
			return category == "malware" || category == "phishing" || category == "scam"
		case "strict":
			// Block neutral entries with any category
			return category != ""
		default: // "off" or unrecognized
			return false
		}
	}

	return false
}

// IsBadMarked is a backward-compatible wrapper around IsBlocked.
// It checks if a DNN ID has a 'block' mark (TLD-level only, no filter level logic).
func (qb *QueryBuilder) IsBadMarked(dnnBlock int64, position int) (bool, error) {
	return qb.IsBlocked(dnnBlock, position, "")
}

// GetPendingAnchors retrieves anchor events that need Bitcoin validation
func (qb *QueryBuilder) GetPendingAnchors() ([]string, error) {
	query := `
		SELECT transaction_id
		FROM anchor_events
		WHERE bitcoin_block_number = 0 OR dnn_block_number = 0
		ORDER BY created_at ASC
		LIMIT 100
	`

	rows, err := qb.db.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending anchors: %w", err)
	}
	defer rows.Close()

	var txIDs []string
	for rows.Next() {
		var txID string
		if err := rows.Scan(&txID); err != nil {
			return nil, fmt.Errorf("failed to scan transaction ID: %w", err)
		}
		txIDs = append(txIDs, txID)
	}

	return txIDs, nil
}

// UpdateSyncState updates the sync state
func (qb *QueryBuilder) UpdateSyncState(key, value string) error {
	query := `
		INSERT OR REPLACE INTO sync_state (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
	`

	_, err := qb.db.db.Exec(query, key, value)
	if err != nil {
		return fmt.Errorf("failed to update sync state: %w", err)
	}

	return nil
}

// GetSyncState retrieves a sync state value
func (qb *QueryBuilder) GetSyncState(key string) (string, error) {
	var value string
	err := qb.db.db.QueryRow("SELECT value FROM sync_state WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get sync state: %w", err)
	}

	return value, nil
}

// LocalMark represents a local awareness mark (from admin)
type LocalMark struct {
	DNNBlock  int64      `json:"dnn_block"`
	Position  int        `json:"position"`
	Name      string     `json:"name,omitempty"`     // empty = whole TLD, set = specific domain
	Mark      string     `json:"mark"`               // "allow", "neutral", "block"
	Category  string     `json:"category,omitempty"` // "malware", "phishing", "scam", "adult", "other"
	Reason    string     `json:"reason"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// DNNID returns the formatted DNN ID string (e.g., "n50.1" or "banana.n50.1")
func (m LocalMark) DNNID() string {
	if m.Name != "" {
		return fmt.Sprintf("%s.n%d.%d", m.Name, m.DNNBlock, m.Position)
	}
	return fmt.Sprintf("n%d.%d", m.DNNBlock, m.Position)
}

// PeerMark represents an awareness mark from a peer node
type PeerMark struct {
	DNNBlock   int64      `json:"dnn_block"`
	Position   int        `json:"position"`
	Name       string     `json:"name,omitempty"`
	PeerPubkey string     `json:"peer_pubkey"`
	Mark       string     `json:"mark"`
	Category   string     `json:"category,omitempty"`
	Reason     string     `json:"reason"`
	ReceivedAt *time.Time `json:"received_at,omitempty"`
}

// PeerMarkAggregate represents aggregated peer marks for a DNN ID
type PeerMarkAggregate struct {
	DNNBlock     int64 `json:"dnn_block"`
	Position     int   `json:"position"`
	AllowCount   int   `json:"allow_count"`
	NeutralCount int   `json:"neutral_count"`
	BlockCount   int   `json:"block_count"`
	TotalPeers   int   `json:"total_peers"`
}

// DNNID returns the formatted DNN ID string
func (a PeerMarkAggregate) DNNID() string {
	return fmt.Sprintf("n%d.%d", a.DNNBlock, a.Position)
}

// AwarenessStats represents awareness database statistics
type AwarenessStats struct {
	LocalTotal   int `json:"local_total"`
	LocalAllow   int `json:"local_allow"`
	LocalNeutral int `json:"local_neutral"`
	LocalBlock   int `json:"local_block"`
	PeerTotal    int `json:"peer_total"`
	PeerAllow    int `json:"peer_allow"`
	PeerNeutral  int `json:"peer_neutral"`
	PeerBlock    int `json:"peer_block"`
}

// CleanupOldData removes old data based on retention policy
func (qb *QueryBuilder) CleanupOldData(retentionDays int) error {
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays).Unix()

	// Since anchor_events now store naddr references (not event IDs),
	// we need to be more careful about cleanup.
	// For now, we'll keep all name/connection/metadata events that might be referenced.
	// A more sophisticated approach would decode all naddr refs and check which events are referenced.

	// Get all anchor naddr references to determine which events are in use
	anchorRows, err := qb.db.db.Query(`
		SELECT name_event_ref, connection_event_ref, metadata_event_ref
		FROM anchor_events
	`)
	if err != nil {
		return fmt.Errorf("failed to query anchor refs: %w", err)
	}

	// Collect referenced event coordinates
	referencedNameEvents := make(map[string]bool)
	referencedConnEvents := make(map[string]bool)
	referencedMetaEvents := make(map[string]bool)

	for anchorRows.Next() {
		var nameRef, connRef, metaRef string
		if err := anchorRows.Scan(&nameRef, &connRef, &metaRef); err != nil {
			continue
		}

		// Decode each naddr and mark the event as referenced
		if _, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameRef); err == nil {
			referencedNameEvents[namePubkey+":"+nameDTag] = true
		}
		if _, _, connPubkey, _, err := extractEventIDFromNaddr(connRef); err == nil {
			referencedConnEvents[connPubkey] = true
		}
		if _, _, metaPubkey, _, err := extractEventIDFromNaddr(metaRef); err == nil {
			referencedMetaEvents[metaPubkey] = true
		}
	}
	anchorRows.Close()

	// Delete unreferenced name events
	nameRows, err := qb.db.db.Query("SELECT id, pubkey, d_tag, created_at FROM name_events")
	if err == nil {
		defer nameRows.Close()
		for nameRows.Next() {
			var id, pubkey, dTag string
			var createdAt int64
			if err := nameRows.Scan(&id, &pubkey, &dTag, &createdAt); err != nil {
				continue
			}

			if createdAt < cutoffTime && !referencedNameEvents[pubkey+":"+dTag] {
				qb.db.db.Exec("DELETE FROM name_events WHERE id = ?", id)
			}
		}
	}

	// Delete unreferenced connection events
	connRows, err := qb.db.db.Query("SELECT id, pubkey, created_at FROM connection_events")
	if err == nil {
		defer connRows.Close()
		for connRows.Next() {
			var id, pubkey string
			var createdAt int64
			if err := connRows.Scan(&id, &pubkey, &createdAt); err != nil {
				continue
			}

			if createdAt < cutoffTime && !referencedConnEvents[pubkey] {
				qb.db.db.Exec("DELETE FROM connection_events WHERE id = ?", id)
			}
		}
	}

	// Delete unreferenced metadata events
	metaRows, err := qb.db.db.Query("SELECT id, pubkey, created_at FROM metadata_events")
	if err == nil {
		defer metaRows.Close()
		for metaRows.Next() {
			var id, pubkey string
			var createdAt int64
			if err := metaRows.Scan(&id, &pubkey, &createdAt); err != nil {
				continue
			}

			if createdAt < cutoffTime && !referencedMetaEvents[pubkey] {
				qb.db.db.Exec("DELETE FROM metadata_events WHERE id = ?", id)
			}
		}
	}

	// Vacuum database to reclaim space
	if _, err := qb.db.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("failed to vacuum database: %w", err)
	}

	return nil
}

// ExportNames exports all names to JSON
func (qb *QueryBuilder) ExportNames() ([]byte, error) {
	results, err := qb.SearchNames("", 100000, 0) // Get all names
	if err != nil {
		return nil, fmt.Errorf("failed to export names: %w", err)
	}

	return json.MarshalIndent(results, "", "  ")
}

// BitcoinTransactionRecord represents a Bitcoin transaction with optional anchor event info
type BitcoinTransactionRecord struct {
	TransactionID  string    `json:"transaction_id"`
	BitcoinBlock   int64     `json:"bitcoin_block"`
	DNNBlock       int64     `json:"dnn_block"`
	Position       int       `json:"position"`
	BitcoinAddress string    `json:"bitcoin_address"`
	FeeRate        string    `json:"fee_rate"` // TX ID Number Sum display (e.g., "433" or "433 (1-9)")
	DiscoveredAt   time.Time `json:"discovered_at"`
	BlockTime      *int64    `json:"block_time,omitempty"` // Actual Bitcoin block timestamp (Unix seconds)
	HasAnchorEvent bool      `json:"has_anchor_event"`
	AnchorEventID  string    `json:"anchor_event_id,omitempty"`
	Name           string    `json:"name,omitempty"`
	Pubkey         string    `json:"pubkey,omitempty"`
	Npub           string    `json:"npub,omitempty"`
	Naddr          string    `json:"naddr,omitempty"`
	Encoded        string    `json:"encoded,omitempty"`
}

// GetAllBitcoinTransactions retrieves all Bitcoin transactions with anchor event status
func (qb *QueryBuilder) GetAllBitcoinTransactions(limit int, offset int) ([]BitcoinTransactionRecord, error) {
	// Import encoder to generate encoded names
	enc := encoder.NewEncoder()

	// First import the encoder package at the top of the file
	// Use block_timestamp when available, fall back to discovered_at
	query := `
		SELECT
			bt.transaction_id,
			bt.bitcoin_block_number,
			bt.dnn_block_number,
			bt.position,
			bt.bitcoin_address,
			bt.fee_rate,
			COALESCE(bt.tie_position, 0) as tie_position,
			COALESCE(bt.tie_digit, 0) as tie_digit,
			COALESCE(db.block_timestamp, bt.discovered_at) as block_time,
			bt.has_anchor_event,
			COALESCE(bt.anchor_event_id, '') as anchor_event_id,
			COALESCE(ae.pubkey, '') as anchor_pubkey,
			COALESCE(ae.d_tag, '') as anchor_d_tag,
			COALESCE(ae.name_event_ref, '') as name_event_ref
		FROM bitcoin_transactions bt
		LEFT JOIN anchor_events ae ON bt.transaction_id = ae.transaction_id
		LEFT JOIN dnn_blocks db ON bt.bitcoin_block_number = db.bitcoin_block_number
		ORDER BY bt.bitcoin_block_number DESC, bt.position ASC
		LIMIT ? OFFSET ?
	`

	rows, err := qb.db.db.Query(query, limit, offset)

	// If query fails (likely missing block_timestamp column), try fallback query
	if err != nil {
		log.Printf("Query with block_timestamp failed, using fallback: %v", err)

		// Fallback query without block_timestamp column
		query = `
			SELECT
				bt.transaction_id,
				bt.bitcoin_block_number,
				bt.dnn_block_number,
				bt.position,
				bt.bitcoin_address,
				bt.fee_rate,
				COALESCE(bt.tie_position, 0) as tie_position,
				COALESCE(bt.tie_digit, 0) as tie_digit,
				bt.discovered_at as block_time,
				bt.has_anchor_event,
				COALESCE(bt.anchor_event_id, '') as anchor_event_id,
				COALESCE(ae.pubkey, '') as anchor_pubkey,
				COALESCE(ae.d_tag, '') as anchor_d_tag,
				COALESCE(ae.name_event_ref, '') as name_event_ref
			FROM bitcoin_transactions bt
			LEFT JOIN anchor_events ae ON bt.transaction_id = ae.transaction_id
			ORDER BY bt.bitcoin_block_number DESC, bt.position ASC
			LIMIT ? OFFSET ?
		`

		rows, err = qb.db.db.Query(query, limit, offset)
		if err != nil {
			return nil, fmt.Errorf("failed to get Bitcoin transactions (fallback): %w", err)
		}
	}
	defer rows.Close()

	var results []BitcoinTransactionRecord
	for rows.Next() {
		var r BitcoinTransactionRecord
		var discoveredAt int64
		var feeRate, tiePosition, tieDigit int
		var anchorPubkey, anchorDTag, nameEventRef string

		err := rows.Scan(
			&r.TransactionID,
			&r.BitcoinBlock,
			&r.DNNBlock,
			&r.Position,
			&r.BitcoinAddress,
			&feeRate,
			&tiePosition,
			&tieDigit,
			&discoveredAt,
			&r.HasAnchorEvent,
			&r.AnchorEventID,
			&anchorPubkey,
			&anchorDTag,
			&nameEventRef,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan transaction: %w", err)
		}

		r.DiscoveredAt = time.Unix(discoveredAt, 0)
		r.Pubkey = anchorPubkey

		// Format TX ID NS display (e.g., "433" or "433 (1-9)")
		if tiePosition > 0 {
			r.FeeRate = fmt.Sprintf("%d (%d-%d)", feeRate, tiePosition, tieDigit)
		} else {
			r.FeeRate = fmt.Sprintf("%d", feeRate)
		}

		// If there's an anchor, decode the name_event_ref to get the name
		if nameEventRef != "" {
			_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
			if err == nil {
				var primaryName string
				err = qb.db.db.QueryRow(
					"SELECT primary_name FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
					namePubkey, nameDTag,
				).Scan(&primaryName)
				if err == nil {
					r.Name = primaryName
				}
			}
		}

		// Always generate encoded name for consistent display
		encoded, err := enc.Encode(r.DNNBlock, r.Position)
		if err == nil {
			r.Encoded = encoded
		}

		// Generate npub from pubkey if available
		if r.Pubkey != "" {
			npub, err := nip19.EncodePublicKey(r.Pubkey)
			if err == nil {
				r.Npub = npub
			}
		}

		// Generate naddr from anchor event if available
		if anchorPubkey != "" && anchorDTag != "" {
			naddr, err := nip19.EncodeEntity(anchorPubkey, 60600, anchorDTag, []string{})
			if err != nil {
				log.Printf("[QB] Warning: Failed to encode naddr for anchor pubkey=%s d_tag=%s: %v", anchorPubkey[:8], anchorDTag[:8], err)
			} else {
				r.Naddr = naddr
			}
		}

		results = append(results, r)
	}

	return results, nil
}

// PaginatedTransactionsResult holds paginated transaction results
type PaginatedTransactionsResult struct {
	Results []BitcoinTransactionRecord `json:"results"`
	Total   int                        `json:"total"`
	Limit   int                        `json:"limit"`
	Offset  int                        `json:"offset"`
}

// GetBitcoinTransactionsPaginated retrieves paginated Bitcoin transactions with filtering
// status: "complete", "pending", or "all"
// search: search term for name, address, or txid
// bitcoinBlock: filter by specific bitcoin block number (0 = no filter)
func (qb *QueryBuilder) GetBitcoinTransactionsPaginated(limit, offset int, status, search string, bitcoinBlock int) (*PaginatedTransactionsResult, error) {
	enc := encoder.NewEncoder()

	// Build WHERE clause based on filters
	var conditions []string
	var args []interface{}

	if status == "complete" {
		conditions = append(conditions, "bt.has_anchor_event = 1")
	} else if status == "pending" {
		conditions = append(conditions, "bt.has_anchor_event = 0")
	}

	// Filter by specific bitcoin block
	if bitcoinBlock > 0 {
		conditions = append(conditions, "bt.bitcoin_block_number = ?")
		args = append(args, bitcoinBlock)
	}

	if search != "" {
		// Check if search is an npub - decode to hex pubkey
		if strings.HasPrefix(strings.ToLower(search), "npub1") {
			// Try to decode npub to hex pubkey
			prefix, pubkeyHex, err := nip19.Decode(search)
			if err == nil && prefix == "npub" {
				// Search by hex pubkey
				searchPattern := "%" + pubkeyHex.(string) + "%"
				conditions = append(conditions, "COALESCE(ae.pubkey, '') LIKE ?")
				args = append(args, searchPattern)
			} else {
				// Invalid npub, do general search
				searchPattern := "%" + search + "%"
				conditions = append(conditions, "bt.transaction_id LIKE ?")
				args = append(args, searchPattern)
			}
		} else if strings.HasPrefix(strings.ToLower(search), "n") && strings.Contains(search, ".") {
			// Parse nX.Y format - exact DNN block and position search
			parts := strings.SplitN(search[1:], ".", 2) // Remove 'n' prefix
			if len(parts) == 2 {
				conditions = append(conditions, "(bt.dnn_block_number = ? AND bt.position = ?)")
				args = append(args, parts[0], parts[1])
			} else {
				// Invalid format, do general search
				searchPattern := "%" + search + "%"
				conditions = append(conditions, "bt.transaction_id LIKE ?")
				args = append(args, searchPattern)
			}
		} else if strings.HasPrefix(strings.ToLower(search), "b") && strings.Contains(search, ".") {
			// Parse bX.Y format - exact Bitcoin block and position search
			parts := strings.SplitN(search[1:], ".", 2) // Remove 'b' prefix
			if len(parts) == 2 {
				conditions = append(conditions, "(bt.bitcoin_block_number = ? AND bt.position = ?)")
				args = append(args, parts[0], parts[1])
			} else {
				// Invalid format, do general search
				searchPattern := "%" + search + "%"
				conditions = append(conditions, "bt.transaction_id LIKE ?")
				args = append(args, searchPattern)
			}
		} else {
			// Try to decode as encoded DNN name (e.g., "nabceabsurd", "nabce-absurd")
			decodedBlock, decodedPos, decodeErr := enc.Decode(search)
			if decodeErr == nil {
				// Successfully decoded - search by exact block and position
				conditions = append(conditions, "(bt.dnn_block_number = ? AND bt.position = ?)")
				args = append(args, decodedBlock, decodedPos)
			} else {
				// General search on multiple fields
				searchPattern := "%" + search + "%"
				conditions = append(conditions, `(
					bt.bitcoin_address LIKE ? OR 
					bt.transaction_id LIKE ? OR 
					CAST(bt.dnn_block_number AS TEXT) LIKE ? OR 
					CAST(bt.position AS TEXT) LIKE ? OR 
					CAST(bt.bitcoin_block_number AS TEXT) LIKE ? OR
					COALESCE(ae.pubkey, '') LIKE ?
				)`)
				args = append(args, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern, searchPattern)
			}
		}
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Count total matching records (with anchor_events join for pubkey search)
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM bitcoin_transactions bt
		LEFT JOIN anchor_events ae ON bt.transaction_id = ae.transaction_id
		%s
	`, whereClause)

	var total int
	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)
	if err := qb.db.db.QueryRow(countQuery, countArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count transactions: %w", err)
	}

	// Fetch paginated records - use COALESCE to prefer block_timestamp when available
	dataQuery := fmt.Sprintf(`
		SELECT
			bt.transaction_id,
			bt.bitcoin_block_number,
			bt.dnn_block_number,
			bt.position,
			bt.bitcoin_address,
			bt.fee_rate,
			COALESCE(bt.tie_position, 0) as tie_position,
			COALESCE(bt.tie_digit, 0) as tie_digit,
			COALESCE(db.block_timestamp, bt.discovered_at) as block_time,
			bt.has_anchor_event,
			COALESCE(bt.anchor_event_id, '') as anchor_event_id,
			COALESCE(ae.pubkey, '') as anchor_pubkey,
			COALESCE(ae.d_tag, '') as anchor_d_tag,
			COALESCE(ae.name_event_ref, '') as name_event_ref
		FROM bitcoin_transactions bt
		LEFT JOIN anchor_events ae ON bt.transaction_id = ae.transaction_id
		LEFT JOIN dnn_blocks db ON bt.bitcoin_block_number = db.bitcoin_block_number
		%s
		ORDER BY bt.bitcoin_block_number DESC, bt.position ASC
		LIMIT ? OFFSET ?
	`, whereClause)

	// Add pagination args
	dataArgs := make([]interface{}, len(args))
	copy(dataArgs, args)
	dataArgs = append(dataArgs, limit, offset)

	rows, err := qb.db.db.Query(dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to get paginated transactions: %w", err)
	}
	defer rows.Close()

	var results []BitcoinTransactionRecord
	for rows.Next() {
		var r BitcoinTransactionRecord
		var discoveredAt int64
		var feeRate, tiePosition, tieDigit int
		var anchorPubkey, anchorDTag, nameEventRef string

		err := rows.Scan(
			&r.TransactionID,
			&r.BitcoinBlock,
			&r.DNNBlock,
			&r.Position,
			&r.BitcoinAddress,
			&feeRate,
			&tiePosition,
			&tieDigit,
			&discoveredAt,
			&r.HasAnchorEvent,
			&r.AnchorEventID,
			&anchorPubkey,
			&anchorDTag,
			&nameEventRef,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan transaction: %w", err)
		}

		r.DiscoveredAt = time.Unix(discoveredAt, 0)
		r.Pubkey = anchorPubkey

		// Format TX ID NS display (e.g., "433" or "433 (1-9)")
		if tiePosition > 0 {
			r.FeeRate = fmt.Sprintf("%d (%d-%d)", feeRate, tiePosition, tieDigit)
		} else {
			r.FeeRate = fmt.Sprintf("%d", feeRate)
		}

		// If there's an anchor, decode the name_event_ref to get the name
		if nameEventRef != "" {
			_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
			if err == nil {
				var primaryName string
				err = qb.db.db.QueryRow(
					"SELECT primary_name FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
					namePubkey, nameDTag,
				).Scan(&primaryName)
				if err == nil {
					r.Name = primaryName
				}
			}
		}

		// Always generate encoded name
		encoded, err := enc.Encode(r.DNNBlock, r.Position)
		if err == nil {
			r.Encoded = encoded
		}

		// Generate npub from pubkey
		if r.Pubkey != "" {
			npub, err := nip19.EncodePublicKey(r.Pubkey)
			if err == nil {
				r.Npub = npub
			}
		}

		// Generate naddr from anchor event
		if anchorPubkey != "" && anchorDTag != "" {
			naddr, err := nip19.EncodeEntity(anchorPubkey, 60600, anchorDTag, []string{})
			if err == nil {
				r.Naddr = naddr
			}
		}

		results = append(results, r)
	}

	return &PaginatedTransactionsResult{
		Results: results,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// GetUncheckedBitcoinTransactions retrieves Bitcoin transactions that have never been checked on relays
// These are transactions where relay_checked_at IS NULL and has_anchor_event = 0
func (qb *QueryBuilder) GetUncheckedBitcoinTransactions(limit int) ([]BitcoinTransactionRecord, error) {
	query := `
		SELECT
			transaction_id,
			bitcoin_block_number,
			dnn_block_number,
			position,
			bitcoin_address,
			fee_rate,
			discovered_at,
			has_anchor_event,
			COALESCE(anchor_event_id, '') as anchor_event_id
		FROM bitcoin_transactions
		WHERE relay_checked_at IS NULL AND has_anchor_event = 0
		ORDER BY discovered_at DESC
		LIMIT ?
	`

	rows, err := qb.db.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get unchecked transactions: %w", err)
	}
	defer rows.Close()

	var results []BitcoinTransactionRecord
	for rows.Next() {
		var r BitcoinTransactionRecord
		var discoveredAt int64
		err := rows.Scan(
			&r.TransactionID,
			&r.BitcoinBlock,
			&r.DNNBlock,
			&r.Position,
			&r.BitcoinAddress,
			&r.FeeRate,
			&discoveredAt,
			&r.HasAnchorEvent,
			&r.AnchorEventID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan unchecked transaction: %w", err)
		}
		r.DiscoveredAt = time.Unix(discoveredAt, 0)
		results = append(results, r)
	}

	return results, nil
}

// GetCheckedTransactionsWithoutAnchor retrieves transactions that HAVE been checked on relays
// but still don't have an anchor event. This is for re-checking when anchor events are published late.
func (qb *QueryBuilder) GetCheckedTransactionsWithoutAnchor(limit int) ([]BitcoinTransactionRecord, error) {
	query := `
		SELECT
			transaction_id,
			bitcoin_block_number,
			dnn_block_number,
			position,
			bitcoin_address,
			fee_rate,
			discovered_at,
			has_anchor_event,
			COALESCE(anchor_event_id, '') as anchor_event_id
		FROM bitcoin_transactions
		WHERE relay_checked_at IS NOT NULL AND has_anchor_event = 0
		ORDER BY discovered_at ASC
		LIMIT ?
	`

	rows, err := qb.db.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get checked transactions without anchor: %w", err)
	}
	defer rows.Close()

	var results []BitcoinTransactionRecord
	for rows.Next() {
		var r BitcoinTransactionRecord
		var discoveredAt int64
		err := rows.Scan(
			&r.TransactionID,
			&r.BitcoinBlock,
			&r.DNNBlock,
			&r.Position,
			&r.BitcoinAddress,
			&r.FeeRate,
			&discoveredAt,
			&r.HasAnchorEvent,
			&r.AnchorEventID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan checked transaction: %w", err)
		}
		r.DiscoveredAt = time.Unix(discoveredAt, 0)
		results = append(results, r)
	}

	return results, nil
}

// MarkTransactionsRelayChecked marks transactions as having been checked on relays
func (qb *QueryBuilder) MarkTransactionsRelayChecked(txIDs []string) error {
	if len(txIDs) == 0 {
		return nil
	}

	// Use a transaction for atomic update
	tx, err := qb.db.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("UPDATE bitcoin_transactions SET relay_checked_at = strftime('%s', 'now') WHERE transaction_id = ?")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, txID := range txIDs {
		if _, err := stmt.Exec(txID); err != nil {
			return fmt.Errorf("failed to mark transaction %s: %w", txID, err)
		}
	}

	return tx.Commit()
}

// GetBitcoinTransactionsByAddress retrieves Bitcoin transactions for a specific address
func (qb *QueryBuilder) GetBitcoinTransactionsByAddress(address string, limit int) ([]BitcoinTransactionRecord, error) {
	enc := encoder.NewEncoder()

	query := `
		SELECT
			bt.transaction_id,
			bt.bitcoin_block_number,
			bt.dnn_block_number,
			bt.position,
			bt.bitcoin_address,
			bt.fee_rate,
			COALESCE(db.block_timestamp, bt.discovered_at) as block_time,
			bt.has_anchor_event,
			COALESCE(bt.anchor_event_id, '') as anchor_event_id,
			COALESCE(ae.pubkey, '') as anchor_pubkey,
			COALESCE(ae.d_tag, '') as anchor_d_tag,
			COALESCE(ae.name_event_ref, '') as name_event_ref
		FROM bitcoin_transactions bt
		LEFT JOIN anchor_events ae ON bt.transaction_id = ae.transaction_id
		LEFT JOIN dnn_blocks db ON bt.bitcoin_block_number = db.bitcoin_block_number
		WHERE bt.bitcoin_address = ?
		ORDER BY bt.bitcoin_block_number DESC, bt.position ASC
		LIMIT ?
	`

	rows, err := qb.db.db.Query(query, address, limit)

	// If query fails (likely missing block_timestamp column), try fallback query
	if err != nil {
		log.Printf("Query with block_timestamp failed, using fallback: %v", err)

		query = `
			SELECT
				bt.transaction_id,
				bt.bitcoin_block_number,
				bt.dnn_block_number,
				bt.position,
				bt.bitcoin_address,
				bt.fee_rate,
				bt.discovered_at as block_time,
				bt.has_anchor_event,
				COALESCE(bt.anchor_event_id, '') as anchor_event_id,
				COALESCE(ae.pubkey, '') as anchor_pubkey,
				COALESCE(ae.d_tag, '') as anchor_d_tag,
				COALESCE(ae.name_event_ref, '') as name_event_ref
			FROM bitcoin_transactions bt
			LEFT JOIN anchor_events ae ON bt.transaction_id = ae.transaction_id
			WHERE bt.bitcoin_address = ?
			ORDER BY bt.bitcoin_block_number DESC, bt.position ASC
			LIMIT ?
		`

		rows, err = qb.db.db.Query(query, address, limit)
		if err != nil {
			return nil, fmt.Errorf("failed to get Bitcoin transactions by address (fallback): %w", err)
		}
	}
	defer rows.Close()

	var results []BitcoinTransactionRecord
	for rows.Next() {
		var r BitcoinTransactionRecord
		var discoveredAt int64
		var anchorPubkey, anchorDTag, nameEventRef string

		err := rows.Scan(
			&r.TransactionID,
			&r.BitcoinBlock,
			&r.DNNBlock,
			&r.Position,
			&r.BitcoinAddress,
			&r.FeeRate,
			&discoveredAt,
			&r.HasAnchorEvent,
			&r.AnchorEventID,
			&anchorPubkey,
			&anchorDTag,
			&nameEventRef,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan transaction: %w", err)
		}

		r.DiscoveredAt = time.Unix(discoveredAt, 0)
		r.Pubkey = anchorPubkey

		// If there's an anchor, decode the name_event_ref to get the name
		if nameEventRef != "" {
			_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
			if err == nil {
				var primaryName string
				err = qb.db.db.QueryRow(
					"SELECT primary_name FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
					namePubkey, nameDTag,
				).Scan(&primaryName)
				if err == nil {
					r.Name = primaryName
				}
			}
		}

		// Generate encoded name
		encoded, err := enc.Encode(r.DNNBlock, r.Position)
		if err == nil {
			r.Encoded = encoded
		}

		// Generate npub from pubkey if available
		if r.Pubkey != "" {
			npub, err := nip19.EncodePublicKey(r.Pubkey)
			if err == nil {
				r.Npub = npub
			}
		}

		// Generate naddr from anchor event if available
		if anchorPubkey != "" && anchorDTag != "" {
			naddr, err := nip19.EncodeEntity(anchorPubkey, 60600, anchorDTag, []string{})
			if err == nil {
				r.Naddr = naddr
			}
		}

		results = append(results, r)
	}

	return results, nil
}

// GetRecentBlocks retrieves recent DNN blocks
func (qb *QueryBuilder) GetRecentBlocks(limit int) ([]BlockInfo, error) {
	query := `
		SELECT
			b.block_number,
			b.bitcoin_block_number,
			b.bitcoin_block_hash,
			b.created_at,
			b.synced_at,
			(SELECT COUNT(*) FROM anchor_events WHERE dnn_block_number = b.block_number) as anchor_count
		FROM dnn_blocks b
		ORDER BY b.block_number DESC
		LIMIT ?
	`

	rows, err := qb.db.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent blocks: %w", err)
	}
	defer rows.Close()

	var blocks []BlockInfo
	for rows.Next() {
		var info BlockInfo
		var createdAt int64
		var syncedAt sql.NullInt64

		err := rows.Scan(
			&info.BlockNumber,
			&info.BitcoinBlockNumber,
			&info.BitcoinBlockHash,
			&createdAt,
			&syncedAt,
			&info.AnchorCount,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan block: %w", err)
		}

		info.CreatedAt = time.Unix(createdAt, 0)
		if syncedAt.Valid {
			t := time.Unix(syncedAt.Int64, 0)
			info.SyncedAt = &t
		}

		blocks = append(blocks, info)
	}

	return blocks, nil
}

// GetPendingTransactionIDs returns all transaction IDs that don't have anchor events yet
// Used for filtering relay subscriptions to only listen for relevant anchor events
func (qb *QueryBuilder) GetPendingTransactionIDs() ([]string, error) {
	query := `
		SELECT transaction_id
		FROM bitcoin_transactions
		WHERE has_anchor_event = 0
		ORDER BY discovered_at DESC
		LIMIT 100
	`

	rows, err := qb.db.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending transaction IDs: %w", err)
	}
	defer rows.Close()

	var txIDs []string
	for rows.Next() {
		var txID string
		if err := rows.Scan(&txID); err != nil {
			continue
		}
		txIDs = append(txIDs, txID)
	}

	return txIDs, nil
}

// GetKnownDNNAuthors returns all unique pubkeys from anchor events
// These are verified DNN participants (they have valid Bitcoin transactions)
// Used for filtering relay subscriptions to only listen for updates from known users
func (qb *QueryBuilder) GetKnownDNNAuthors() ([]string, error) {
	query := `
		SELECT DISTINCT pubkey
		FROM anchor_events
		ORDER BY created_at DESC
		LIMIT 500
	`

	rows, err := qb.db.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get known DNN authors: %w", err)
	}
	defer rows.Close()

	var pubkeys []string
	for rows.Next() {
		var pubkey string
		if err := rows.Scan(&pubkey); err != nil {
			continue
		}
		pubkeys = append(pubkeys, pubkey)
	}

	return pubkeys, nil
}

// BlockAnchorCount represents the transaction counts in a Bitcoin block
type BlockAnchorCount struct {
	BitcoinBlock  int `json:"bitcoin_block"`
	DNNBlock      int `json:"dnn_block"`
	CompleteCount int `json:"complete_count"`
	TotalCount    int `json:"total_count"`
}

// GetBlockAnchorCounts returns the anchor counts per bitcoin block for a given range
// This is used by the block visualizer to show accurate anchor counts
// Returns complete_count (with anchor events) and total_count (all transactions)
func (qb *QueryBuilder) GetBlockAnchorCounts(startBlock, endBlock int) ([]BlockAnchorCount, error) {
	query := `
		SELECT 
			bitcoin_block_number,
			dnn_block_number,
			SUM(CASE WHEN has_anchor_event = 1 THEN 1 ELSE 0 END) as complete_count,
			COUNT(*) as total_count
		FROM bitcoin_transactions
		WHERE bitcoin_block_number >= ? 
		  AND bitcoin_block_number <= ?
		GROUP BY bitcoin_block_number, dnn_block_number
		ORDER BY bitcoin_block_number ASC
	`

	rows, err := qb.db.db.Query(query, startBlock, endBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to get block anchor counts: %w", err)
	}
	defer rows.Close()

	var results []BlockAnchorCount
	for rows.Next() {
		var bc BlockAnchorCount
		if err := rows.Scan(&bc.BitcoinBlock, &bc.DNNBlock, &bc.CompleteCount, &bc.TotalCount); err != nil {
			continue
		}
		results = append(results, bc)
	}

	return results, nil
}

// ========== Peer Node Discovery Functions (Kind 64600) ==========

// PeerNode represents a discovered DNN peer node (address-centric)
type PeerNode struct {
	Address      string `json:"address"`
	NodeNpub     string `json:"node_npub,omitempty"`
	NodePubkey   string `json:"node_pubkey,omitempty"`
	AdminPubkey  string `json:"admin_pubkey,omitempty"`
	AnnouncedBy  string `json:"announced_by,omitempty"`
	IsVerified   bool   `json:"is_verified"`
	FailCount    int    `json:"fail_count"`
	LastChecked  *int64 `json:"last_checked,omitempty"`
	LastSeen     *int64 `json:"last_seen,omitempty"`
	DiscoveredAt int64  `json:"discovered_at"`
}

// PeerAddressExists checks if a peer address already exists in the database
func (qb *QueryBuilder) PeerAddressExists(address string) (bool, error) {
	var count int
	err := qb.db.db.QueryRow("SELECT COUNT(*) FROM peer_nodes WHERE address = ?", address).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// StorePeerNode stores a new verified peer node (address is primary key)
func (qb *QueryBuilder) StorePeerNode(node *PeerNode) error {
	query := `
		INSERT OR REPLACE INTO peer_nodes
		(address, node_npub, node_pubkey, admin_pubkey, announced_by, is_verified, fail_count, last_seen, discovered_at)
		VALUES (?, ?, ?, ?, ?, 1, 0, strftime('%s', 'now'), COALESCE((SELECT discovered_at FROM peer_nodes WHERE address = ?), strftime('%s', 'now')))
	`

	_, err := qb.db.db.Exec(query,
		node.Address,
		node.NodeNpub,
		node.NodePubkey,
		node.AdminPubkey,
		node.AnnouncedBy,
		node.Address, // for COALESCE subquery
	)

	if err != nil {
		return fmt.Errorf("failed to store peer node: %w", err)
	}

	log.Printf("[DB] Stored peer node %s (npub: %s, admin: %s)", node.Address, node.NodeNpub, node.AdminPubkey)
	return nil
}

// UpdatePeerHealthCheck updates health check results for a peer node
func (qb *QueryBuilder) UpdatePeerHealthCheck(address string, success bool, nodeNpub string, nodePubkey string, adminPubkey string) error {
	if success {
		query := `
			UPDATE peer_nodes
			SET last_checked = strftime('%s', 'now'),
				last_seen = strftime('%s', 'now'),
				fail_count = 0,
				is_verified = 1,
				node_npub = ?,
				node_pubkey = ?,
				admin_pubkey = ?
			WHERE address = ?
		`
		_, err := qb.db.db.Exec(query, nodeNpub, nodePubkey, adminPubkey, address)
		return err
	}

	query := `
		UPDATE peer_nodes
		SET last_checked = strftime('%s', 'now'),
			fail_count = fail_count + 1
		WHERE address = ?
	`
	_, err := qb.db.db.Exec(query, address)
	return err
}

// RemoveDeadPeers removes peers that have exceeded the maximum fail count
func (qb *QueryBuilder) RemoveDeadPeers(maxFailCount int) (int64, error) {
	result, err := qb.db.db.Exec("DELETE FROM peer_nodes WHERE fail_count >= ?", maxFailCount)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetAllPeerAddresses retrieves all peer addresses for health checking
func (qb *QueryBuilder) GetAllPeerAddresses() ([]PeerNode, error) {
	query := `
		SELECT address, COALESCE(node_npub, ''), COALESCE(node_pubkey, ''),
			   COALESCE(announced_by, ''), is_verified, fail_count,
			   last_checked, last_seen, discovered_at
		FROM peer_nodes
		ORDER BY discovered_at ASC
	`

	rows, err := qb.db.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get peer addresses: %w", err)
	}
	defer rows.Close()

	var nodes []PeerNode
	for rows.Next() {
		var node PeerNode
		var lastChecked, lastSeen sql.NullInt64

		err := rows.Scan(
			&node.Address,
			&node.NodeNpub,
			&node.NodePubkey,
			&node.AnnouncedBy,
			&node.IsVerified,
			&node.FailCount,
			&lastChecked,
			&lastSeen,
			&node.DiscoveredAt,
		)
		if err != nil {
			continue
		}

		if lastChecked.Valid {
			node.LastChecked = &lastChecked.Int64
		}
		if lastSeen.Valid {
			node.LastSeen = &lastSeen.Int64
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

// GetPeerNodeCount returns the count of peer nodes
func (qb *QueryBuilder) GetPeerNodeCount() (int, int, error) {
	var total, verified int
	err := qb.db.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(CASE WHEN is_verified = 1 THEN 1 ELSE 0 END), 0) FROM peer_nodes").Scan(&total, &verified)
	return total, verified, err
}

// PaginatedPeerNodesResult represents paginated peer nodes
type PaginatedPeerNodesResult struct {
	Results []PeerNode `json:"results"`
	Total   int        `json:"total"`
	Limit   int        `json:"limit"`
	Offset  int        `json:"offset"`
}

// GetPeerNodesPaginated retrieves paginated peer nodes with search
func (qb *QueryBuilder) GetPeerNodesPaginated(limit, offset int, search string) (*PaginatedPeerNodesResult, error) {
	whereClause := "WHERE 1=1"
	var args []interface{}

	if search != "" {
		whereClause += " AND (address LIKE ? OR node_npub LIKE ? OR node_pubkey LIKE ?)"
		searchPattern := "%" + search + "%"
		args = append(args, searchPattern, searchPattern, searchPattern)
	}

	countQuery := "SELECT COUNT(*) FROM peer_nodes " + whereClause
	var total int
	if err := qb.db.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	dataQuery := fmt.Sprintf(`
		SELECT address, COALESCE(node_npub, ''), COALESCE(node_pubkey, ''),
			   COALESCE(announced_by, ''), is_verified, fail_count,
			   last_checked, last_seen, discovered_at
		FROM peer_nodes
		%s
		ORDER BY last_seen DESC, discovered_at DESC
		LIMIT ? OFFSET ?
	`, whereClause)
	args = append(args, limit, offset)

	rows, err := qb.db.db.Query(dataQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []PeerNode
	for rows.Next() {
		var node PeerNode
		var lastChecked, lastSeen sql.NullInt64

		err := rows.Scan(
			&node.Address,
			&node.NodeNpub,
			&node.NodePubkey,
			&node.AnnouncedBy,
			&node.IsVerified,
			&node.FailCount,
			&lastChecked,
			&lastSeen,
			&node.DiscoveredAt,
		)
		if err != nil {
			continue
		}

		if lastChecked.Valid {
			node.LastChecked = &lastChecked.Int64
		}
		if lastSeen.Valid {
			node.LastSeen = &lastSeen.Int64
		}

		nodes = append(nodes, node)
	}

	return &PaginatedPeerNodesResult{
		Results: nodes,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// ========== User-Discovered Relays Functions ==========

// StoreDiscoveredRelay adds or updates a relay in the discovered_relays table
func (qb *QueryBuilder) StoreDiscoveredRelay(url, source, discoveredBy string) error {
	query := `
		INSERT INTO discovered_relays (url, source, discovered_by, discovered_at, last_seen, is_active)
		VALUES (?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'), 1)
		ON CONFLICT(url) DO UPDATE SET 
			last_seen = strftime('%s', 'now'),
			is_active = 1
	`
	_, err := qb.db.db.Exec(query, url, source, discoveredBy)
	return err
}

// StoreDiscoveredRelays adds multiple relays at once
func (qb *QueryBuilder) StoreDiscoveredRelays(urls []string, source, discoveredBy string) error {
	tx, err := qb.db.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO discovered_relays (url, source, discovered_by, discovered_at, last_seen, is_active)
		VALUES (?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'), 1)
		ON CONFLICT(url) DO UPDATE SET 
			last_seen = strftime('%s', 'now'),
			is_active = 1
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, url := range urls {
		if _, err := stmt.Exec(url, source, discoveredBy); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetDiscoveredRelays retrieves all active discovered relays
func (qb *QueryBuilder) GetDiscoveredRelays() ([]string, error) {
	rows, err := qb.db.db.Query("SELECT url FROM discovered_relays WHERE is_active = 1 ORDER BY last_seen DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var relays []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			continue
		}
		relays = append(relays, url)
	}
	return relays, nil
}

// DiscoveredRelayResult represents a discovered relay with metadata
type DiscoveredRelayResult struct {
	URL          string `json:"url"`
	Source       string `json:"source"`
	DiscoveredBy string `json:"discovered_by,omitempty"`
	DiscoveredAt int64  `json:"discovered_at"`
	LastSeen     int64  `json:"last_seen"`
}

// PaginatedDiscoveredRelaysResult represents paginated discovered relays
type PaginatedDiscoveredRelaysResult struct {
	Results []DiscoveredRelayResult `json:"results"`
	Total   int                     `json:"total"`
	Limit   int                     `json:"limit"`
	Offset  int                     `json:"offset"`
}

// GetDiscoveredRelaysPaginated retrieves paginated discovered relays with search
func (qb *QueryBuilder) GetDiscoveredRelaysPaginated(limit, offset int, search string) (*PaginatedDiscoveredRelaysResult, error) {
	// Build WHERE clause
	whereClause := "WHERE is_active = 1"
	var args []interface{}

	if search != "" {
		whereClause += " AND url LIKE ?"
		args = append(args, "%"+search+"%")
	}

	// Count total
	countQuery := "SELECT COUNT(*) FROM discovered_relays " + whereClause
	var total int
	if err := qb.db.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	// Fetch paginated data
	dataQuery := fmt.Sprintf(`
		SELECT url, source, COALESCE(discovered_by, ''), discovered_at, last_seen
		FROM discovered_relays
		%s
		ORDER BY last_seen DESC
		LIMIT ? OFFSET ?
	`, whereClause)
	args = append(args, limit, offset)

	rows, err := qb.db.db.Query(dataQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DiscoveredRelayResult
	for rows.Next() {
		var r DiscoveredRelayResult
		if err := rows.Scan(&r.URL, &r.Source, &r.DiscoveredBy, &r.DiscoveredAt, &r.LastSeen); err != nil {
			continue
		}
		results = append(results, r)
	}

	return &PaginatedDiscoveredRelaysResult{
		Results: results,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// GetAllDiscoveredRelays returns relays from the discovered_relays table
// This includes both user NIP-65 relays and peer node relays (excluding hardcoded)
func (qb *QueryBuilder) GetAllDiscoveredRelays() ([]string, error) {
	return qb.GetDiscoveredRelays()
}
