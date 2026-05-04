package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Database represents the DNN node database
type Database struct {
	db          *sql.DB
	hookManager *HookManager
}

// SetHookManager sets the hook manager for event notifications
func (d *Database) SetHookManager(hm *HookManager) {
	d.hookManager = hm
}

// HasValidTransactionForAddresses checks if any Bitcoin transaction exists for any of the given addresses
// Used to validate that a npub owner can publish DNN events
func (d *Database) HasValidTransactionForAddresses(addresses []string) (bool, error) {
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
	err := d.db.QueryRow(query, args...).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check transactions: %w", err)
	}
	return count > 0, nil
}

// pruneEventVersions keeps only the newest maxVersions of an event for a given pubkey + d_tag
func (d *Database) pruneEventVersions(tableName string, pubkey string, dTag string, maxVersions int) {
	// Count current versions
	var count int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE pubkey = ? AND d_tag = ?", tableName)
	err := d.db.QueryRow(countQuery, pubkey, dTag).Scan(&count)
	if err != nil || count <= maxVersions {
		return
	}

	// Delete oldest versions beyond the limit
	toDelete := count - maxVersions
	deleteQuery := fmt.Sprintf(`
		DELETE FROM %s WHERE id IN (
			SELECT id FROM %s 
			WHERE pubkey = ? AND d_tag = ?
			ORDER BY created_at ASC
			LIMIT ?
		)
	`, tableName, tableName)

	result, err := d.db.Exec(deleteQuery, pubkey, dTag, toDelete)
	if err != nil {
		log.Printf("[DB] Warning: failed to prune versions for %s/%s: %v", pubkey[:8], dTag[:min(8, len(dTag))], err)
		return
	}

	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		log.Printf("[DB] Pruned %d old versions for pubkey %s d_tag %s (max %d)", deleted, pubkey[:8], dTag[:min(8, len(dTag))], maxVersions)
	}
}

// pruneDTags keeps only the newest maxDTags for a given pubkey and kind
// Protects d-tags that are referenced by anchor events
func (d *Database) pruneDTags(tableName string, kind int, pubkey string, maxDTags int) {
	// Count distinct d-tags for this pubkey
	var count int
	countQuery := fmt.Sprintf("SELECT COUNT(DISTINCT d_tag) FROM %s WHERE pubkey = ?", tableName)
	err := d.db.QueryRow(countQuery, pubkey).Scan(&count)
	if err != nil || count <= maxDTags {
		return
	}

	// Get protected d-tags (referenced by anchors)
	protectedDTags := d.getAnchorReferencedDTags(kind, pubkey)

	// Find oldest d-tags (by oldest event in each d-tag group)
	dtagQuery := fmt.Sprintf(`
		SELECT d_tag, MIN(created_at) as oldest
		FROM %s WHERE pubkey = ?
		GROUP BY d_tag
		ORDER BY oldest ASC
	`, tableName)

	rows, err := d.db.Query(dtagQuery, pubkey)
	if err != nil {
		return
	}
	defer rows.Close()

	toDelete := count - maxDTags
	deleted := 0

	for rows.Next() && deleted < toDelete {
		var dTag string
		var oldest int64
		if err := rows.Scan(&dTag, &oldest); err != nil {
			continue
		}

		// Skip protected d-tags
		if protectedDTags[dTag] {
			log.Printf("[DB] Skipping protected d-tag %s (anchor referenced)", dTag[:min(8, len(dTag))])
			continue
		}

		// Delete all events for this d-tag
		deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE pubkey = ? AND d_tag = ?", tableName)
		result, err := d.db.Exec(deleteQuery, pubkey, dTag)
		if err != nil {
			continue
		}

		affected, _ := result.RowsAffected()
		if affected > 0 {
			log.Printf("[DB] Pruned d-tag %s (%d events) for pubkey %s (max %d d-tags)", dTag[:min(8, len(dTag))], affected, pubkey[:8], maxDTags)
			deleted++
		}
	}
}

// RunEventCleanup performs retroactive validation and pruning on all existing events.
// This should be called on startup and periodically to enforce limits.
// It requires an addressChecker function to validate pubkeys against valid Bitcoin transactions.
func (d *Database) RunEventCleanup(addressChecker func(pubkey string) (bool, error)) error {
	log.Println("[DB CLEANUP] Starting event cleanup...")

	// Tables to process
	tables := []struct {
		name string
		kind int
	}{
		{"name_events", 61600},
		{"connection_events", 62600},
		{"metadata_events", 63600},
	}

	totalDeleted := 0
	totalPruned := 0

	for _, table := range tables {
		log.Printf("[DB CLEANUP] Processing %s...", table.name)

		// Step 1: Remove events from pubkeys without valid transactions
		if addressChecker != nil {
			deleted, err := d.cleanupInvalidPubkeys(table.name, addressChecker)
			if err != nil {
				log.Printf("[DB CLEANUP] Error cleaning %s: %v", table.name, err)
			} else {
				totalDeleted += deleted
			}
		}

		// Step 2: Enforce version limit (10 per d-tag per pubkey)
		pruned, err := d.enforceVersionLimits(table.name, 10)
		if err != nil {
			log.Printf("[DB CLEANUP] Error pruning versions in %s: %v", table.name, err)
		} else {
			totalPruned += pruned
		}

		// Step 3: Enforce d-tag limit (5 per kind per pubkey)
		prunedDtags, err := d.enforceDTagLimits(table.name, table.kind, 5)
		if err != nil {
			log.Printf("[DB CLEANUP] Error pruning d-tags in %s: %v", table.name, err)
		} else {
			totalPruned += prunedDtags
		}
	}

	log.Printf("[DB CLEANUP] Complete: %d invalid events removed, %d events pruned for limits", totalDeleted, totalPruned)
	return nil
}

// cleanupInvalidPubkeys removes events from pubkeys without valid Bitcoin transactions
func (d *Database) cleanupInvalidPubkeys(tableName string, addressChecker func(pubkey string) (bool, error)) (int, error) {
	// Get all distinct pubkeys
	query := fmt.Sprintf("SELECT DISTINCT pubkey FROM %s", tableName)
	rows, err := d.db.Query(query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var pubkeysToDelete []string
	for rows.Next() {
		var pubkey string
		if err := rows.Scan(&pubkey); err != nil {
			continue
		}

		// Check if this pubkey has valid transactions
		hasValid, err := addressChecker(pubkey)
		if err != nil {
			log.Printf("[DB CLEANUP] Error checking pubkey %s: %v", pubkey[:8], err)
			continue
		}

		if !hasValid {
			pubkeysToDelete = append(pubkeysToDelete, pubkey)
		}
	}

	// Delete events from invalid pubkeys
	totalDeleted := 0
	for _, pubkey := range pubkeysToDelete {
		// Check if any events from this pubkey are referenced by anchors
		protectedDTags := d.getAnchorReferencedDTags(getKindForTable(tableName), pubkey)

		if len(protectedDTags) > 0 {
			// Only delete non-protected events
			for dTag := range protectedDTags {
				log.Printf("[DB CLEANUP] Skipping protected d-tag %s for pubkey %s", dTag[:min(8, len(dTag))], pubkey[:8])
			}
			// Delete events not in protected d-tags
			deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE pubkey = ? AND d_tag NOT IN (%s)",
				tableName, placeholdersForMap(protectedDTags))
			args := []interface{}{pubkey}
			for dTag := range protectedDTags {
				args = append(args, dTag)
			}
			result, _ := d.db.Exec(deleteQuery, args...)
			if result != nil {
				deleted, _ := result.RowsAffected()
				totalDeleted += int(deleted)
			}
		} else {
			// Delete all events for this pubkey
			deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE pubkey = ?", tableName)
			result, err := d.db.Exec(deleteQuery, pubkey)
			if err == nil {
				deleted, _ := result.RowsAffected()
				totalDeleted += int(deleted)
				if deleted > 0 {
					log.Printf("[DB CLEANUP] Removed %d events from invalid pubkey %s in %s", deleted, pubkey[:8], tableName)
				}
			}
		}
	}

	return totalDeleted, nil
}

// enforceVersionLimits ensures max N versions per d-tag per pubkey
func (d *Database) enforceVersionLimits(tableName string, maxVersions int) (int, error) {
	// Find all pubkey+d_tag combinations with too many versions
	query := fmt.Sprintf(`
		SELECT pubkey, d_tag, COUNT(*) as cnt
		FROM %s
		GROUP BY pubkey, d_tag
		HAVING cnt > ?
	`, tableName)

	rows, err := d.db.Query(query, maxVersions)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	totalPruned := 0
	for rows.Next() {
		var pubkey, dTag string
		var count int
		if err := rows.Scan(&pubkey, &dTag, &count); err != nil {
			continue
		}

		// Delete oldest versions beyond the limit
		toDelete := count - maxVersions
		deleteQuery := fmt.Sprintf(`
			DELETE FROM %s WHERE id IN (
				SELECT id FROM %s 
				WHERE pubkey = ? AND d_tag = ?
				ORDER BY created_at ASC
				LIMIT ?
			)
		`, tableName, tableName)

		result, err := d.db.Exec(deleteQuery, pubkey, dTag, toDelete)
		if err == nil {
			deleted, _ := result.RowsAffected()
			totalPruned += int(deleted)
			if deleted > 0 {
				log.Printf("[DB CLEANUP] Pruned %d old versions for %s/%s (max %d)", deleted, pubkey[:8], dTag[:min(8, len(dTag))], maxVersions)
			}
		}
	}

	return totalPruned, nil
}

// enforceDTagLimits ensures max N d-tags per kind per pubkey
func (d *Database) enforceDTagLimits(tableName string, kind int, maxDTags int) (int, error) {
	// Find all pubkeys with too many d-tags
	query := fmt.Sprintf(`
		SELECT pubkey, COUNT(DISTINCT d_tag) as cnt
		FROM %s
		GROUP BY pubkey
		HAVING cnt > ?
	`, tableName)

	rows, err := d.db.Query(query, maxDTags)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	totalPruned := 0
	for rows.Next() {
		var pubkey string
		var count int
		if err := rows.Scan(&pubkey, &count); err != nil {
			continue
		}

		// Get protected d-tags
		protectedDTags := d.getAnchorReferencedDTags(kind, pubkey)

		// Find oldest d-tags to delete
		dtagQuery := fmt.Sprintf(`
			SELECT d_tag, MIN(created_at) as oldest
			FROM %s WHERE pubkey = ?
			GROUP BY d_tag
			ORDER BY oldest ASC
		`, tableName)

		dtagRows, err := d.db.Query(dtagQuery, pubkey)
		if err != nil {
			continue
		}

		toDelete := count - maxDTags
		deleted := 0

		for dtagRows.Next() && deleted < toDelete {
			var dTag string
			var oldest int64
			if err := dtagRows.Scan(&dTag, &oldest); err != nil {
				continue
			}

			// Skip protected d-tags
			if protectedDTags[dTag] {
				continue
			}

			// Delete all events for this d-tag
			deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE pubkey = ? AND d_tag = ?", tableName)
			result, err := d.db.Exec(deleteQuery, pubkey, dTag)
			if err == nil {
				affected, _ := result.RowsAffected()
				totalPruned += int(affected)
				deleted++
				if affected > 0 {
					log.Printf("[DB CLEANUP] Pruned d-tag %s for pubkey %s (max %d d-tags)", dTag[:min(8, len(dTag))], pubkey[:8], maxDTags)
				}
			}
		}
		dtagRows.Close()
	}

	return totalPruned, nil
}

// Helper function to get kind from table name
func getKindForTable(tableName string) int {
	switch tableName {
	case "name_events":
		return 61600
	case "connection_events":
		return 62600
	case "metadata_events":
		return 63600
	default:
		return 0
	}
}

// Helper to create placeholders for IN clause
func placeholdersForMap(m map[string]bool) string {
	if len(m) == 0 {
		return "''"
	}
	placeholders := make([]string, 0, len(m))
	for range m {
		placeholders = append(placeholders, "?")
	}
	return strings.Join(placeholders, ",")
}

// getAnchorReferencedDTags returns d-tags that are referenced by anchor events
func (d *Database) getAnchorReferencedDTags(kind int, pubkey string) map[string]bool {
	protected := make(map[string]bool)

	// Get the appropriate naddr reference column based on kind
	var refColumn string
	switch kind {
	case 61600:
		refColumn = "name_event_ref"
	case 62600:
		refColumn = "connection_event_ref"
	case 63600:
		refColumn = "metadata_event_ref"
	default:
		return protected
	}

	// Query anchors for this pubkey
	query := fmt.Sprintf("SELECT %s FROM anchor_events WHERE pubkey = ?", refColumn)
	rows, err := d.db.Query(query, pubkey)
	if err != nil {
		return protected
	}
	defer rows.Close()

	for rows.Next() {
		var naddr string
		if err := rows.Scan(&naddr); err != nil {
			continue
		}

		// Extract d-tag from naddr
		_, _, _, dTag, err := extractEventIDFromNaddr(naddr)
		if err == nil && dTag != "" {
			protected[dTag] = true
		}
	}

	return protected
}

// New creates a new database connection
func New(dataDir string) (*Database, error) {
	dbPath := filepath.Join(dataDir, "dnn.db")

	db, err := sql.Open("sqlite3", dbPath+"?cache=shared&mode=rwc&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	d := &Database{db: db}

	// Initialize schema
	if err := d.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Run database migrations
	migrationMgr := NewMigrationManager(db)
	if err := migrationMgr.Run(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return d, nil
}

// extractEventIDFromNaddr decodes an naddr and extracts the event ID
// naddr contains kind, pubkey, d-tag, and optional relay hints
// We need to reconstruct the event ID from these components by querying the database
func extractEventIDFromNaddr(naddr string) (eventID string, kind int, pubkey string, dTag string, err error) {
	// Decode the naddr
	prefix, data, err := nip19.Decode(naddr)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("failed to decode naddr: %w", err)
	}

	if prefix != "naddr" {
		return "", 0, "", "", fmt.Errorf("not an naddr: got %s", prefix)
	}

	// Extract the EntityPointer data
	ep, ok := data.(nostr.EntityPointer)
	if !ok {
		return "", 0, "", "", fmt.Errorf("invalid naddr data type")
	}

	return ep.Identifier, ep.Kind, ep.PublicKey, ep.Identifier, nil
}

// findEventByCoordinates looks up an event by kind, pubkey, and d-tag
// This is used to resolve naddr references to local event IDs
func (d *Database) findEventByCoordinates(kind int, pubkey string, dTag string) (eventID string, err error) {
	var query string

	switch kind {
	case 61600:
		query = "SELECT id FROM name_events WHERE pubkey = ? AND d_tag = ? LIMIT 1"
	case 62600:
		query = "SELECT id FROM connection_events WHERE pubkey = ? LIMIT 1"
	case 63600:
		query = "SELECT id FROM metadata_events WHERE pubkey = ? LIMIT 1"
	default:
		return "", fmt.Errorf("unsupported event kind: %d", kind)
	}

	err = d.db.QueryRow(query, pubkey, dTag).Scan(&eventID)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("event not found for kind=%d pubkey=%s d=%s", kind, pubkey[:8], dTag)
	}
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	return eventID, nil
}

// initSchema creates the database schema
func (d *Database) initSchema() error {
	schema := `
	-- DNN Blocks table (corresponds to Bitcoin blocks)
	-- Genesis: DNN block 0 anchored to Bitcoin block (network-dependent)
	--   Mainnet: 1,000,000 (future)
	--   Testnet: 922,660 (current testing)
	--   Dev: 900,000 (development)
	CREATE TABLE IF NOT EXISTS dnn_blocks (
		block_number INTEGER PRIMARY KEY,
		bitcoin_block_number INTEGER NOT NULL UNIQUE,
		bitcoin_block_hash TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		synced_at TIMESTAMP
	);

	-- Name events (kind 61600)
	CREATE TABLE IF NOT EXISTS name_events (
		id TEXT PRIMARY KEY,
		pubkey TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER,
		d_tag TEXT NOT NULL, -- unique identifier hash
		primary_name TEXT, -- actual primary name from 'n' tag
		other_names TEXT, -- JSON array of other names
		content TEXT,
		sig TEXT NOT NULL,
		UNIQUE(pubkey, d_tag)
	);

	-- Connection events (kind 62600)
	CREATE TABLE IF NOT EXISTS connection_events (
		id TEXT PRIMARY KEY,
		pubkey TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER,
		content TEXT NOT NULL, -- JSON connection data
		sig TEXT NOT NULL,
		UNIQUE(pubkey)
	);

	-- Metadata events (kind 63600)
	CREATE TABLE IF NOT EXISTS metadata_events (
		id TEXT PRIMARY KEY,
		pubkey TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER,
		content TEXT NOT NULL, -- JSON metadata
		sig TEXT NOT NULL,
		UNIQUE(pubkey)
	);

	-- Bitcoin transactions (valid self-transfers for DNN)
	CREATE TABLE IF NOT EXISTS bitcoin_transactions (
		transaction_id TEXT PRIMARY KEY,
		bitcoin_block_number INTEGER NOT NULL,
		dnn_block_number INTEGER NOT NULL,
		position INTEGER NOT NULL,
		bitcoin_address TEXT NOT NULL,
		fee_rate INTEGER NOT NULL, -- TX ID Number Sum (digit sum for ordering)
		tie_position INTEGER DEFAULT 0, -- 1-indexed position of first differing digit (0 if no tie)
		tie_digit INTEGER DEFAULT 0, -- value of winning digit at tie_position
		discovered_at INTEGER NOT NULL,
		has_anchor_event BOOLEAN DEFAULT 0,
		anchor_event_id TEXT,
		FOREIGN KEY (dnn_block_number) REFERENCES dnn_blocks(block_number),
		UNIQUE(bitcoin_block_number, position)
	);

	-- Anchor events (kind 60600) - Addressable Replaceable Event
	CREATE TABLE IF NOT EXISTS anchor_events (
		id TEXT PRIMARY KEY,
		pubkey TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		d_tag TEXT NOT NULL,
		name_event_ref TEXT NOT NULL, -- naddr reference to name event
		connection_event_ref TEXT NOT NULL, -- naddr reference to connection event
		metadata_event_ref TEXT NOT NULL, -- naddr reference to metadata event
		transaction_id TEXT NOT NULL,
		bitcoin_block_number INTEGER NOT NULL,
		dnn_block_number INTEGER NOT NULL,
		position INTEGER NOT NULL, -- position in the block
		content TEXT,
		sig TEXT NOT NULL,
		FOREIGN KEY (transaction_id) REFERENCES bitcoin_transactions(transaction_id),
		FOREIGN KEY (dnn_block_number) REFERENCES dnn_blocks(block_number),
		UNIQUE(pubkey, d_tag),
		UNIQUE(transaction_id)
	);

	-- Awareness database (opinions about names)
	CREATE TABLE IF NOT EXISTS awareness_marks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		anchor_event_id TEXT NOT NULL,
		marker_pubkey TEXT NOT NULL, -- who marked it
		mark_type TEXT NOT NULL, -- 'good' or 'bad'
		reason TEXT, -- reason for marking
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (anchor_event_id) REFERENCES anchor_events(id),
		UNIQUE(anchor_event_id, marker_pubkey)
	);

	-- Peer nodes table
	CREATE TABLE IF NOT EXISTS peer_nodes (
		pubkey TEXT PRIMARY KEY,
		relay_url TEXT NOT NULL,
		last_seen TIMESTAMP,
		last_sync TIMESTAMP,
		is_active BOOLEAN DEFAULT 1,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	-- Sync state table
	CREATE TABLE IF NOT EXISTS sync_state (
		key TEXT PRIMARY KEY,
		value TEXT,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	-- Transaction to block index (for input address resolution)
	-- Maps transaction IDs to their containing block hashes
	CREATE TABLE IF NOT EXISTS tx_block_index (
		txid TEXT PRIMARY KEY,
		block_hash TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tx_block_index_hash ON tx_block_index(block_hash);

	-- Create indexes
	CREATE INDEX IF NOT EXISTS idx_bitcoin_transactions_block ON bitcoin_transactions(bitcoin_block_number);
	CREATE INDEX IF NOT EXISTS idx_bitcoin_transactions_dnn_block ON bitcoin_transactions(dnn_block_number);
	CREATE INDEX IF NOT EXISTS idx_bitcoin_transactions_address ON bitcoin_transactions(bitcoin_address);
	CREATE INDEX IF NOT EXISTS idx_anchor_events_bitcoin_block ON anchor_events(bitcoin_block_number);
	CREATE INDEX IF NOT EXISTS idx_anchor_events_dnn_block ON anchor_events(dnn_block_number);
	CREATE INDEX IF NOT EXISTS idx_anchor_events_pubkey ON anchor_events(pubkey);
	CREATE INDEX IF NOT EXISTS idx_name_events_d_tag ON name_events(d_tag);
	CREATE INDEX IF NOT EXISTS idx_awareness_marks_anchor ON awareness_marks(anchor_event_id);
	CREATE INDEX IF NOT EXISTS idx_awareness_marks_type ON awareness_marks(mark_type);

	-- Reserved addresses table
	CREATE TABLE IF NOT EXISTS reserved_addresses (
		address TEXT PRIMARY KEY,
		reserved_for TEXT NOT NULL,
		description TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	-- Insert default reserved addresses
	INSERT OR IGNORE INTO reserved_addresses (address, reserved_for, description) VALUES
		('n0.0', 'DNN Node Registry', 'Lists community-published DNN node builds and versions'),
		('b1m.0', 'Bitcoin Node Registry', 'Lists Bitcoin node builds and versions'),
		('b1000000.0', 'Bitcoin Node Registry', 'Alternative notation for Bitcoin node registry');

	-- Insert default sync state if not exists
	INSERT OR IGNORE INTO sync_state (key, value) VALUES
		('last_bitcoin_block', '930129'),  -- Start from one block before genesis (930130)
		('last_dnn_block', '-1'),          -- No DNN blocks yet
		('last_sync_time', '0'),
		('last_reorg_check', '0');
	`

	if _, err := d.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	return nil
}

// GetConnectionContentByBlockAndPosition retrieves connection content for a DNN name
// This is used by the DNS server to resolve names directly from the database
func (d *Database) GetConnectionContentByBlockAndPosition(block int64, position int) (string, error) {
	// First, get the connection_event_ref from anchor_events
	var connEventRef string
	err := d.db.QueryRow(`
		SELECT connection_event_ref
		FROM anchor_events
		WHERE dnn_block_number = ? AND position = ?
		LIMIT 1
	`, block, position).Scan(&connEventRef)

	if err != nil {
		return "", fmt.Errorf("anchor not found for block=%d, pos=%d: %w", block, position, err)
	}

	// Extract pubkey AND d_tag from the naddr reference
	_, _, connPubkey, connDTag, err := extractEventIDFromNaddr(connEventRef)
	if err != nil {
		return "", fmt.Errorf("failed to decode connection_event_ref: %w", err)
	}

	// Fetch connection content by pubkey AND d_tag (get latest version)
	var content string
	if connDTag != "" {
		err = d.db.QueryRow(
			"SELECT content FROM connection_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
			connPubkey, connDTag,
		).Scan(&content)
	} else {
		err = d.db.QueryRow(
			"SELECT content FROM connection_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
			connPubkey,
		).Scan(&content)
	}

	if err != nil {
		return "", fmt.Errorf("connection not found for pubkey %s: %w", connPubkey, err)
	}

	return content, nil
}

// GetConnectionContentByPubkey retrieves connection event content by pubkey
// Used for resolving delegated connection events
func (d *Database) GetConnectionContentByPubkey(pubkey string) (string, error) {
	var content string
	err := d.db.QueryRow(
		"SELECT content FROM connection_events WHERE pubkey = ? LIMIT 1",
		pubkey,
	).Scan(&content)

	if err != nil {
		return "", fmt.Errorf("connection not found for pubkey %s: %w", pubkey, err)
	}

	return content, nil
}

// GetConnectionContentByPubkeyAndDTag retrieves connection event content by pubkey AND d-tag
// This is used for delegation resolution where we need the specific connection event
func (d *Database) GetConnectionContentByPubkeyAndDTag(pubkey string, dTag string) (string, error) {
	var content string
	err := d.db.QueryRow(
		"SELECT content FROM connection_events WHERE pubkey = ? AND d_tag = ? LIMIT 1",
		pubkey, dTag,
	).Scan(&content)

	if err != nil {
		return "", fmt.Errorf("connection not found for pubkey %s d_tag %s: %w", pubkey, dTag, err)
	}

	return content, nil
}

// GetNameContentByBlockAndPosition retrieves name event content by block and position
func (d *Database) GetNameContentByBlockAndPosition(block int64, position int) (string, error) {
	// First, get the name_event_ref from anchor_events
	var nameEventRef string
	err := d.db.QueryRow(`
		SELECT name_event_ref
		FROM anchor_events
		WHERE dnn_block_number = ? AND position = ?
		LIMIT 1
	`, block, position).Scan(&nameEventRef)

	if err != nil {
		return "", fmt.Errorf("anchor not found for block=%d, pos=%d: %w", block, position, err)
	}

	// Extract pubkey AND d_tag from the naddr reference
	_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
	if err != nil {
		return "", fmt.Errorf("failed to decode name_event_ref: %w", err)
	}

	// Fetch name content by pubkey AND d_tag (get latest version)
	var content string
	if nameDTag != "" {
		err = d.db.QueryRow(
			"SELECT content FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
			namePubkey, nameDTag,
		).Scan(&content)
	} else {
		err = d.db.QueryRow(
			"SELECT content FROM name_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
			namePubkey,
		).Scan(&content)
	}

	if err != nil {
		return "", fmt.Errorf("name event not found for pubkey %s: %w", namePubkey, err)
	}

	return content, nil
}

// GetPrimaryNameByBlockAndPosition retrieves the primary name (n tag) by block and position
func (d *Database) GetPrimaryNameByBlockAndPosition(block int64, position int) (string, error) {
	// First, get the name_event_ref from anchor_events
	var nameEventRef string
	err := d.db.QueryRow(`
		SELECT name_event_ref
		FROM anchor_events
		WHERE dnn_block_number = ? AND position = ?
		LIMIT 1
	`, block, position).Scan(&nameEventRef)

	if err != nil {
		return "", fmt.Errorf("anchor not found for block=%d, pos=%d: %w", block, position, err)
	}

	// Extract pubkey AND d_tag from the naddr reference
	_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
	if err != nil {
		return "", fmt.Errorf("failed to decode name_event_ref: %w", err)
	}

	// Fetch primary name by pubkey AND d_tag (get latest version)
	var primaryName string
	if nameDTag != "" {
		err = d.db.QueryRow(
			"SELECT primary_name FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
			namePubkey, nameDTag,
		).Scan(&primaryName)
	} else {
		err = d.db.QueryRow(
			"SELECT primary_name FROM name_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
			namePubkey,
		).Scan(&primaryName)
	}

	if err != nil {
		return "", fmt.Errorf("name event not found for pubkey %s: %w", namePubkey, err)
	}

	return primaryName, nil
}

// StoreNameEvent stores a kind 61600 event
// Implements NIP-33 addressable event replacement: only keeps the latest version per pubkey+d_tag
func (d *Database) StoreNameEvent(event *nostr.Event) error {
	// Extract d tag, primary name (n tag), and other names (o tags)
	var dTag, primaryName string
	var otherNames []string

	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			if tag[0] == "d" {
				dTag = tag[1]
			} else if tag[0] == "n" {
				// Normalize to lowercase (DNS is case-insensitive)
				primaryName = strings.ToLower(tag[1])
			} else if tag[0] == "o" {
				// Normalize to lowercase (DNS is case-insensitive)
				otherNames = append(otherNames, strings.ToLower(tag[1]))
			}
		}
	}

	// Check if this exact event already exists (by ID)
	var existingID string
	err := d.db.QueryRow("SELECT id FROM name_events WHERE id = ?", event.ID).Scan(&existingID)
	if err == nil {
		// Already have this exact event
		log.Printf("[DB] Name event %s already exists, skipping", event.ID[:8])
		return nil
	}

	// NIP-33: Check if we have a newer event with the same pubkey+d_tag
	if dTag != "" {
		var newerCreatedAt int64
		err := d.db.QueryRow(
			"SELECT created_at FROM name_events WHERE pubkey = ? AND d_tag = ? AND created_at > ? LIMIT 1",
			event.PubKey, dTag, int64(event.CreatedAt),
		).Scan(&newerCreatedAt)
		if err == nil {
			// A newer event already exists — discard this older one
			log.Printf("[DB] Name event %s is older than existing (d_tag=%s), skipping", event.ID[:8], dTag[:min(len(dTag), 8)])
			return nil
		}

		// Delete older versions with the same pubkey+d_tag (NIP-33 replacement)
		result, err := d.db.Exec(
			"DELETE FROM name_events WHERE pubkey = ? AND d_tag = ? AND created_at < ?",
			event.PubKey, dTag, int64(event.CreatedAt),
		)
		if err == nil {
			if count, _ := result.RowsAffected(); count > 0 {
				log.Printf("[DB] NIP-33: Replaced %d older name event(s) for d_tag=%s", count, dTag[:min(len(dTag), 8)])
			}
		}
	}
	query := `
		INSERT INTO name_events
		(id, pubkey, created_at, updated_at, d_tag, primary_name, other_names, content, sig)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	otherNamesJSON := "[]"
	if len(otherNames) > 0 {
		// Simple JSON array construction
		otherNamesJSON = `["` + otherNames[0] + `"`
		for i := 1; i < len(otherNames); i++ {
			otherNamesJSON += `,"` + otherNames[i] + `"`
		}
		otherNamesJSON += "]"
	}

	_, err = d.db.Exec(query,
		event.ID,
		event.PubKey,
		int64(event.CreatedAt),
		int64(event.CreatedAt), // use created_at as updated_at for now
		dTag,
		primaryName,
		otherNamesJSON,
		event.Content,
		event.Sig,
	)

	if err == nil {
		log.Printf("[DB] Stored name event %s (d_tag=%s, primary_name=%s)", event.ID[:8], dTag[:min(len(dTag), 8)], primaryName)
	}

	return err
}

// StoreConnectionEvent stores a kind 62600 event
// Implements NIP-33 addressable event replacement: only keeps the latest version per pubkey+d_tag
func (d *Database) StoreConnectionEvent(event *nostr.Event) error {
	// Extract d tag for addressable event
	var dTag string
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			dTag = tag[1]
			break
		}
	}

	// Check if this exact event already exists (by ID)
	var existingID string
	err := d.db.QueryRow("SELECT id FROM connection_events WHERE id = ?", event.ID).Scan(&existingID)
	if err == nil {
		// Already have this exact event
		log.Printf("[DB] Connection event %s already exists, skipping", event.ID[:8])
		return nil
	}

	// NIP-33: Check if we have a newer event with the same pubkey+d_tag
	if dTag != "" {
		var newerCreatedAt int64
		err := d.db.QueryRow(
			"SELECT created_at FROM connection_events WHERE pubkey = ? AND d_tag = ? AND created_at > ? LIMIT 1",
			event.PubKey, dTag, int64(event.CreatedAt),
		).Scan(&newerCreatedAt)
		if err == nil {
			// A newer event already exists — discard this older one
			log.Printf("[DB] Connection event %s is older than existing (d_tag=%s), skipping", event.ID[:8], dTag[:min(len(dTag), 8)])
			return nil
		}

		// Delete older versions with the same pubkey+d_tag (NIP-33 replacement)
		result, err := d.db.Exec(
			"DELETE FROM connection_events WHERE pubkey = ? AND d_tag = ? AND created_at < ?",
			event.PubKey, dTag, int64(event.CreatedAt),
		)
		if err == nil {
			if count, _ := result.RowsAffected(); count > 0 {
				log.Printf("[DB] NIP-33: Replaced %d older connection event(s) for d_tag=%s", count, dTag[:min(len(dTag), 8)])
			}
		}
	}

	// Serialize all tags to JSON
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		tagsJSON = []byte("[]")
	}

	// Store the event
	query := `
		INSERT INTO connection_events
		(id, pubkey, created_at, updated_at, d_tag, content, sig, tags_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = d.db.Exec(query,
		event.ID,
		event.PubKey,
		int64(event.CreatedAt),
		int64(event.CreatedAt),
		dTag,
		event.Content,
		event.Sig,
		string(tagsJSON),
	)

	if err == nil {
		log.Printf("[DB] Stored connection event %s (d_tag=%s, created_at=%d)", event.ID[:8], dTag[:min(len(dTag), 8)], event.CreatedAt)
	}

	return err
}

// StoreMetadataEvent stores a kind 63600 event
// Implements NIP-33 addressable event replacement: only keeps the latest version per pubkey+d_tag
func (d *Database) StoreMetadataEvent(event *nostr.Event) error {
	// Extract d tag for addressable event
	var dTag string
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			dTag = tag[1]
			break
		}
	}

	// Check if this exact event already exists (by ID)
	var existingID string
	err := d.db.QueryRow("SELECT id FROM metadata_events WHERE id = ?", event.ID).Scan(&existingID)
	if err == nil {
		// Already have this exact event
		log.Printf("[DB] Metadata event %s already exists, skipping", event.ID[:8])
		return nil
	}

	// NIP-33: Check if we have a newer event with the same pubkey+d_tag
	if dTag != "" {
		var newerCreatedAt int64
		err := d.db.QueryRow(
			"SELECT created_at FROM metadata_events WHERE pubkey = ? AND d_tag = ? AND created_at > ? LIMIT 1",
			event.PubKey, dTag, int64(event.CreatedAt),
		).Scan(&newerCreatedAt)
		if err == nil {
			// A newer event already exists — discard this older one
			log.Printf("[DB] Metadata event %s is older than existing (d_tag=%s), skipping", event.ID[:8], dTag[:min(len(dTag), 8)])
			return nil
		}

		// Delete older versions with the same pubkey+d_tag (NIP-33 replacement)
		result, err := d.db.Exec(
			"DELETE FROM metadata_events WHERE pubkey = ? AND d_tag = ? AND created_at < ?",
			event.PubKey, dTag, int64(event.CreatedAt),
		)
		if err == nil {
			if count, _ := result.RowsAffected(); count > 0 {
				log.Printf("[DB] NIP-33: Replaced %d older metadata event(s) for d_tag=%s", count, dTag[:min(len(dTag), 8)])
			}
		}
	}
	query := `
		INSERT INTO metadata_events
		(id, pubkey, created_at, updated_at, d_tag, content, sig)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err = d.db.Exec(query,
		event.ID,
		event.PubKey,
		int64(event.CreatedAt),
		int64(event.CreatedAt),
		dTag,
		event.Content,
		event.Sig,
	)

	if err == nil {
		log.Printf("[DB] Stored metadata event %s (d_tag=%s, created_at=%d)", event.ID[:8], dTag[:min(len(dTag), 8)], event.CreatedAt)
	}

	return err
}

// StoreAnchorEvent stores a kind 60600 event with validation
// Kind 60600 is an addressable replaceable event (30000-39999 behavior)
func (d *Database) StoreAnchorEvent(event *nostr.Event, bitcoinBlock int64, dnnBlock int64, position int) error {
	// Log all tags for debugging
	var tagNames []string
	for _, tag := range event.Tags {
		if len(tag) >= 1 {
			tagNames = append(tagNames, tag[0])
		}
	}
	log.Printf("[DB] StoreAnchorEvent %s: tags present = %v", event.ID[:8], tagNames)

	// Extract d tag (unique identifier for addressable event), referenced event naddrs, and transaction ID
	var dTag, nameEventRef, connectionEventRef, metadataEventRef, transactionID string
	var hasOldTags bool

	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "d":
				dTag = tag[1] // d tag for addressable event (unique identifier)
			case "n":
				nameEventRef = tag[1] // naddr reference (n = names)
			case "c":
				connectionEventRef = tag[1] // naddr reference (c = connection)
			case "m":
				metadataEventRef = tag[1] // naddr reference (m = metadata)
			case "x":
				transactionID = tag[1] // transaction ID (x = transaction)
			// Detect OLD format tags - these should be rejected
			case "names", "connection", "metadata", "transaction":
				hasOldTags = true
				log.Printf("[DB] WARNING: Anchor %s has old-format tag '%s', will reject", event.ID[:8], tag[0])
			}
		}
	}

	// Reject anchors with old-format tags
	if hasOldTags {
		log.Printf("[DB] Rejecting anchor %s: has old-format tags (names/connection/metadata/transaction instead of n/c/m/x)", event.ID[:8])
		return fmt.Errorf("anchor event uses deprecated tag format")
	}

	// Validate that all required tags exist
	if dTag == "" || nameEventRef == "" || connectionEventRef == "" || metadataEventRef == "" || transactionID == "" {
		log.Printf("[DB] Rejecting anchor %s: missing required tags (d=%v, n=%v, c=%v, m=%v, x=%v)",
			event.ID[:8], dTag != "", nameEventRef != "", connectionEventRef != "", metadataEventRef != "", transactionID != "")
		return fmt.Errorf("anchor event missing required tags (d, n, c, m, x)")
	}

	// Parse updated_at from content JSON (if present)
	var updatedAt int64 = int64(event.CreatedAt) // Default to created_at
	if event.Content != "" {
		var contentData struct {
			UpdatedAt int64 `json:"updated_at"`
		}
		if err := json.Unmarshal([]byte(event.Content), &contentData); err == nil && contentData.UpdatedAt > 0 {
			updatedAt = contentData.UpdatedAt
		}
	}

	// For addressable replaceable events, check if we should update or reject
	existingQuery := `
		SELECT id, created_at, COALESCE(updated_at, created_at) as updated_at, name_event_ref, connection_event_ref, metadata_event_ref FROM anchor_events
		WHERE pubkey = ? AND d_tag = ?
		ORDER BY COALESCE(updated_at, created_at) DESC LIMIT 1
	`
	var existingID string
	var existingCreatedAt, existingUpdatedAt int64
	var existingNameEventRef, existingConnectionEventRef, existingMetadataEventRef string
	err := d.db.QueryRow(existingQuery, event.PubKey, dTag).Scan(&existingID, &existingCreatedAt, &existingUpdatedAt, &existingNameEventRef, &existingConnectionEventRef, &existingMetadataEventRef)

	if err == nil {
		// Found existing anchor - compare using updated_at for proper conflict resolution
		log.Printf("[DB] Found existing anchor id=%s updated_at=%d, new event id=%s updated_at=%d",
			existingID[:8], existingUpdatedAt, event.ID[:8], updatedAt)

		if existingUpdatedAt > updatedAt {
			// Newer event exists, don't store this older one
			log.Printf("[DB] Rejecting anchor %s: newer event exists (existing_updated=%d > new_updated=%d)",
				event.ID[:8], existingUpdatedAt, updatedAt)
			return fmt.Errorf("newer anchor event already exists for this pubkey+d_tag combination")
		}

		// Check if any references have changed
		refsChanged := existingNameEventRef != nameEventRef ||
			existingConnectionEventRef != connectionEventRef ||
			existingMetadataEventRef != metadataEventRef

		if existingUpdatedAt == updatedAt {
			if existingID == event.ID && !refsChanged {
				// Exact same event with same references, already stored
				log.Printf("[DB] Anchor event already stored (same ID + refs): %s", event.ID[:8])
				return nil
			}
			// Different event OR refs changed with same timestamp
			// For addressable replaceable events, the one with newer event ID "wins" when timestamps tie
			// But we also need to allow ref updates, so we proceed if refs changed
			if refsChanged {
				log.Printf("[DB] Anchor %s: references changed (same timestamp), updating", event.ID[:8])
				log.Printf("[DB] Old connection: %s", existingConnectionEventRef[:min(len(existingConnectionEventRef), 30)])
				log.Printf("[DB] New connection: %s", connectionEventRef[:min(len(connectionEventRef), 30)])
				// Proceed with update
			} else {
				log.Printf("[DB] Anchor %s: same timestamp, different ID, no ref changes - allowing update", event.ID[:8])
				// Still allow the update - the new event might have other changes
			}
		} else {
			// New event has newer timestamp, proceed with storage
			log.Printf("[DB] Anchor %s: newer updated_at, storing", event.ID[:8])
		}
	}

	// CRITICAL: Also check for existing anchor by transaction_id (different d_tag, same tx)
	// This handles the case where user published a NEW anchor (different d_tag) for the same Bitcoin transaction
	// We should keep the newer one (by created_at)
	txConflictQuery := `
		SELECT id, d_tag, created_at FROM anchor_events
		WHERE transaction_id = ? AND pubkey = ?
		LIMIT 1
	`
	var conflictID, conflictDTag string
	var conflictCreatedAt int64
	txErr := d.db.QueryRow(txConflictQuery, transactionID, event.PubKey).Scan(&conflictID, &conflictDTag, &conflictCreatedAt)

	log.Printf("[DB] Anchor conflict check: txErr=%v, conflictID=%s, conflictDTag=%s, newDTag=%s",
		txErr, func() string {
			if conflictID != "" {
				return conflictID[:min(8, len(conflictID))]
			}
			return "N/A"
		}(),
		func() string {
			if conflictDTag != "" {
				return conflictDTag[:min(16, len(conflictDTag))]
			}
			return "N/A"
		}(),
		dTag[:min(16, len(dTag))])

	if txErr == nil && conflictDTag != dTag {
		// Found existing anchor with DIFFERENT d_tag but same transaction_id
		// This is a conflict - "LATEST WINS" (newer created_at wins)
		// This allows updating/replacing anchors with newer versions
		newCreatedAt := int64(event.CreatedAt)

		log.Printf("[DB] Transaction conflict: existing anchor id=%s d_tag=%s created_at=%d vs new id=%s d_tag=%s created_at=%d",
			conflictID[:8], conflictDTag[:16], conflictCreatedAt, event.ID[:8], dTag[:16], newCreatedAt)

		if conflictCreatedAt > newCreatedAt {
			// Existing anchor is NEWER - reject this older one
			log.Printf("[DB] Rejecting anchor %s: existing anchor %s has newer created_at (%d > %d) - LATEST WINS",
				event.ID[:8], conflictID[:8], conflictCreatedAt, newCreatedAt)
			return fmt.Errorf("newer anchor event already exists for this transaction (latest wins)")
		}

		if conflictCreatedAt == newCreatedAt {
			// Same created_at - compare event IDs lexicographically (deterministic tie-breaker)
			// Higher ID wins (ensures deterministic winner)
			if conflictID > event.ID {
				log.Printf("[DB] Rejecting anchor %s: existing anchor %s wins tie-breaker (same created_at, higher ID)",
					event.ID[:8], conflictID[:8])
				return fmt.Errorf("anchor event lost tie-breaker for this transaction")
			}
		}

		// New anchor is NEWER (latest wins) or wins tie-breaker - delete the older conflicting anchor
		log.Printf("[DB] Replacing older anchor %s (d_tag=%s) with newer anchor %s (d_tag=%s) for tx %s - LATEST WINS",
			conflictID[:8], conflictDTag[:16], event.ID[:8], dTag[:16], transactionID[:16])

		_, delErr := d.db.Exec("DELETE FROM anchor_events WHERE id = ?", conflictID)
		if delErr != nil {
			log.Printf("[DB] Warning: Failed to delete older conflicting anchor: %v", delErr)
		}
	}

	// Serialize original tags to JSON for storage
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		log.Printf("[DB] Warning: Failed to marshal tags for anchor %s: %v", event.ID[:8], err)
		tagsJSON = []byte("[]")
	}

	query := `
		INSERT OR REPLACE INTO anchor_events
		(id, pubkey, created_at, updated_at, d_tag, name_event_ref, connection_event_ref, metadata_event_ref,
		 transaction_id, bitcoin_block_number, dnn_block_number, position, content, sig, tags_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = d.db.Exec(query,
		event.ID,
		event.PubKey,
		int64(event.CreatedAt),
		updatedAt,
		dTag,
		nameEventRef,
		connectionEventRef,
		metadataEventRef,
		transactionID,
		bitcoinBlock,
		dnnBlock,
		position,
		event.Content,
		event.Sig,
		string(tagsJSON),
	)

	if err != nil {
		return err
	}

	// Update the Bitcoin transaction to mark it as having an anchor event
	err = d.UpdateBitcoinTransactionWithAnchor(transactionID, event.ID)
	if err != nil {
		return err
	}

	// Trigger hooks after successful anchor storage
	if d.hookManager != nil {
		d.hookManager.TriggerOnEventStored(event, 60600)
	}

	return nil
}

// GetAnchorByName retrieves anchor events by name and optional block/position
func (d *Database) GetAnchorByName(name string, blockNum *int64, position *int) (*AnchorRecord, error) {
	// Step 1: Find the anchor event
	var anchorID, anchorPubkey string
	var anchorCreatedAt int64
	var bitcoinBlock, dnnBlock int64
	var anchorPosition int
	var nameEventRef, connEventRef, metaEventRef string

	// Build query - we need to decode naddr and join with name_events if filtering by name
	var query string
	var args []interface{}

	if name != "" {
		// When filtering by name, we need to:
		// 1. Get all anchors matching block/position criteria
		// 2. Decode their name_event_ref naddr values
		// 3. Look up the corresponding name_event
		// 4. Filter by the primary_name field

		// For now, use a simpler approach: get the naddr references and resolve in application code
		// This is less efficient but more straightforward
		query = `
			SELECT
				id, pubkey, created_at, bitcoin_block_number, dnn_block_number, position,
				name_event_ref, connection_event_ref, metadata_event_ref
			FROM anchor_events
			WHERE 1=1
		`

		if blockNum != nil {
			query += " AND dnn_block_number = ?"
			args = append(args, *blockNum)
		}

		if position != nil {
			query += " AND position = ?"
			args = append(args, *position)
		}

		query += " ORDER BY dnn_block_number DESC, position ASC"

		log.Printf("[DB] GetAnchorByName: searching for name='%s', blockNum=%v, position=%v", name, blockNum, position)

		rows, err := d.db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		// Iterate through results and find matching name
		found := false
		for rows.Next() {
			err := rows.Scan(
				&anchorID, &anchorPubkey, &anchorCreatedAt,
				&bitcoinBlock, &dnnBlock, &anchorPosition,
				&nameEventRef, &connEventRef, &metaEventRef,
			)
			if err != nil {
				continue
			}

			// Decode name_event_ref to get coordinates
			_, _, pubkey, dTag, err := extractEventIDFromNaddr(nameEventRef)
			if err != nil {
				log.Printf("[DB] Failed to decode name_event_ref: %v", err)
				continue
			}

			// Look up the name event by coordinates
			var primaryName string
			err = d.db.QueryRow("SELECT primary_name FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				pubkey, dTag).Scan(&primaryName)
			if err != nil {
				log.Printf("[DB] Name event not found for pubkey=%s d_tag=%s", pubkey[:8], dTag)
				continue
			}

			// Check if this is the name we're looking for (case-insensitive)
			if strings.EqualFold(primaryName, name) {
				found = true
				log.Printf("[DB] Found matching anchor: name=%s, block=%d, pos=%d", primaryName, dnnBlock, anchorPosition)
				break
			}
		}

		if !found {
			log.Printf("[DB] No anchor found with name='%s'", name)
			return nil, sql.ErrNoRows
		}
	} else {
		// No name filter - just get by block/position
		query = `
			SELECT
				id, pubkey, created_at, bitcoin_block_number, dnn_block_number, position,
				name_event_ref, connection_event_ref, metadata_event_ref
			FROM anchor_events
			WHERE 1=1
		`

		if blockNum != nil {
			query += " AND dnn_block_number = ?"
			args = append(args, *blockNum)
		}

		if position != nil {
			query += " AND position = ?"
			args = append(args, *position)
		}

		query += " ORDER BY dnn_block_number DESC, position ASC LIMIT 1"

		log.Printf("[DB] GetAnchorByName: block=%v, pos=%v", blockNum, position)

		err := d.db.QueryRow(query, args...).Scan(
			&anchorID, &anchorPubkey, &anchorCreatedAt,
			&bitcoinBlock, &dnnBlock, &anchorPosition,
			&nameEventRef, &connEventRef, &metaEventRef,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				log.Printf("[DB] No anchor found at block=%v pos=%v", blockNum, position)
			}
			return nil, err
		}
	}

	// Step 2: Decode naddr references and fetch referenced events
	var nameContent, connectionContent, metadataContent string
	var primaryName, otherNames string

	// Fetch name event
	_, _, namePubkey, nameDTag, err := extractEventIDFromNaddr(nameEventRef)
	if err == nil {
		err = d.db.QueryRow(
			"SELECT primary_name, other_names, content FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
			namePubkey, nameDTag,
		).Scan(&primaryName, &otherNames, &nameContent)
		if err != nil {
			log.Printf("[DB] Warning: Name event not found for naddr %s: %v", nameEventRef, err)
		}
	} else {
		log.Printf("[DB] Warning: Failed to decode name_event_ref: %v", err)
	}

	// Fetch connection event - use both pubkey AND d_tag for correct version
	// Also fetch raw event fields (id, sig, created_at, tags_json) for signature verification
	var connEventID, connEventPubkey, connEventSig, connEventTagsJSON string
	var connEventCreatedAt int64
	_, _, connPubkey, connDTag, err := extractEventIDFromNaddr(connEventRef)
	if err == nil {
		if connDTag != "" {
			err = d.db.QueryRow(
				"SELECT COALESCE(id,''), COALESCE(pubkey,''), COALESCE(sig,''), COALESCE(created_at,0), COALESCE(tags_json,'[]'), COALESCE(content,'') FROM connection_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				connPubkey, connDTag,
			).Scan(&connEventID, &connEventPubkey, &connEventSig, &connEventCreatedAt, &connEventTagsJSON, &connectionContent)
		} else {
			err = d.db.QueryRow(
				"SELECT COALESCE(id,''), COALESCE(pubkey,''), COALESCE(sig,''), COALESCE(created_at,0), COALESCE(tags_json,'[]'), COALESCE(content,'') FROM connection_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
				connPubkey,
			).Scan(&connEventID, &connEventPubkey, &connEventSig, &connEventCreatedAt, &connEventTagsJSON, &connectionContent)
		}
		if err != nil {
			log.Printf("[DB] Warning: Connection event not found for naddr %s: %v", connEventRef, err)
		}
	} else {
		log.Printf("[DB] Warning: Failed to decode connection_event_ref: %v", err)
	}

	// Fetch metadata event - use both pubkey AND d_tag, get latest version
	_, _, metaPubkey, metaDTag, err := extractEventIDFromNaddr(metaEventRef)
	if err == nil {
		if metaDTag != "" {
			err = d.db.QueryRow(
				"SELECT content FROM metadata_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				metaPubkey, metaDTag,
			).Scan(&metadataContent)
		} else {
			err = d.db.QueryRow(
				"SELECT content FROM metadata_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
				metaPubkey,
			).Scan(&metadataContent)
		}
		if err != nil {
			log.Printf("[DB] Warning: Metadata event not found for naddr %s: %v", metaEventRef, err)
		}
	} else {
		log.Printf("[DB] Warning: Failed to decode metadata_event_ref: %v", err)
	}

	// Step 3: Build and return the record
	record := &AnchorRecord{
		ID:                anchorID,
		Pubkey:            anchorPubkey,
		CreatedAt:         anchorCreatedAt,
		BitcoinBlock:      bitcoinBlock,
		DNNBlock:          dnnBlock,
		Position:          anchorPosition,
		Name:              primaryName,
		OtherNames:        otherNames,
		NameContent:       nameContent,
		ConnectionContent: connectionContent,
		MetadataContent:   metadataContent,
		ConnEventID:        connEventID,
		ConnEventPubkey:    connEventPubkey,
		ConnEventSig:       connEventSig,
		ConnEventCreatedAt: connEventCreatedAt,
		ConnEventTagsJSON:  connEventTagsJSON,
	}

	return record, nil
}

// GetNamesByPubkey retrieves all anchor records for a given pubkey
func (d *Database) GetNamesByPubkey(pubkey string) ([]*AnchorRecord, error) {
	query := `
		SELECT
			id, pubkey, created_at, bitcoin_block_number, dnn_block_number, position,
			name_event_ref, connection_event_ref, metadata_event_ref
		FROM anchor_events
		WHERE pubkey = ?
		ORDER BY dnn_block_number DESC, position ASC
	`

	rows, err := d.db.Query(query, pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*AnchorRecord
	for rows.Next() {
		var id, pk string
		var createdAt int64
		var bitcoinBlock, dnnBlock int64
		var position int
		var nameEventRef, connEventRef, metaEventRef string

		err := rows.Scan(
			&id, &pk, &createdAt,
			&bitcoinBlock, &dnnBlock, &position,
			&nameEventRef, &connEventRef, &metaEventRef,
		)
		if err != nil {
			log.Printf("[DB] Error scanning anchor row: %v", err)
			continue
		}

		// Decode naddr references and fetch referenced events
		var nameContent, connectionContent, metadataContent string
		var primaryName, otherNames string

		// Fetch name event
		_, _, namePubkey, nameDTag, naddrErr := extractEventIDFromNaddr(nameEventRef)
		if naddrErr != nil {
			log.Printf("[DB] GetNamesByPubkey: Failed to decode name_event_ref naddr: %v (ref=%s)", naddrErr, nameEventRef)
		} else {
			nameErr := d.db.QueryRow(
				"SELECT primary_name, other_names, content FROM name_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				namePubkey, nameDTag,
			).Scan(&primaryName, &otherNames, &nameContent)
			if nameErr != nil {
				log.Printf("[DB] GetNamesByPubkey: Name event not found for pubkey=%s d_tag=%s: %v", namePubkey[:min(8, len(namePubkey))], nameDTag, nameErr)
			} else {
				log.Printf("[DB] GetNamesByPubkey: Found name=%s for pubkey=%s d_tag=%s", primaryName, namePubkey[:min(8, len(namePubkey))], nameDTag)
			}
		}

		// Fetch connection event - use both pubkey AND d_tag
		_, _, connPubkey, connDTag, err := extractEventIDFromNaddr(connEventRef)
		if err == nil {
			_ = d.db.QueryRow(
				"SELECT content FROM connection_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
				connPubkey, connDTag,
			).Scan(&connectionContent)
		}

		// Fetch metadata event - use both pubkey AND d_tag, get latest version
		_, _, metaPubkey, metaDTag, err := extractEventIDFromNaddr(metaEventRef)
		if err == nil {
			if metaDTag != "" {
				_ = d.db.QueryRow(
					"SELECT content FROM metadata_events WHERE pubkey = ? AND d_tag = ? ORDER BY created_at DESC LIMIT 1",
					metaPubkey, metaDTag,
				).Scan(&metadataContent)
			} else {
				_ = d.db.QueryRow(
					"SELECT content FROM metadata_events WHERE pubkey = ? ORDER BY created_at DESC LIMIT 1",
					metaPubkey,
				).Scan(&metadataContent)
			}
		}

		record := &AnchorRecord{
			ID:                id,
			Pubkey:            pk,
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

		records = append(records, record)
	}

	return records, nil
}

// Transaction represents a database transaction
type Transaction struct {
	tx *sql.Tx
}

// BeginTransaction starts a new database transaction
func (d *Database) BeginTransaction() (*Transaction, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	return &Transaction{tx: tx}, nil
}

// Commit commits the transaction
func (t *Transaction) Commit() error {
	return t.tx.Commit()
}

// Rollback rolls back the transaction
func (t *Transaction) Rollback() error {
	return t.tx.Rollback()
}

// GetBitcoinBlockHash retrieves the stored hash for a Bitcoin block
func (d *Database) GetBitcoinBlockHash(blockNumber int64) (string, error) {
	var hash string
	err := d.db.QueryRow(
		"SELECT bitcoin_block_hash FROM dnn_blocks WHERE bitcoin_block_number = ?",
		blockNumber,
	).Scan(&hash)

	if err == sql.ErrNoRows {
		return "", fmt.Errorf("block not found")
	}
	return hash, err
}

// UpdateReorgCheckTime updates the last reorg check time
func (d *Database) UpdateReorgCheckTime(checkTime time.Time) error {
	query := `
		INSERT OR REPLACE INTO sync_state (key, value, updated_at)
		VALUES ('last_reorg_check', ?, CURRENT_TIMESTAMP)
	`
	_, err := d.db.Exec(query, checkTime.Format(time.RFC3339))
	return err
}

// InvalidateDNNBlocksAfter marks DNN blocks after a certain height as invalid
func (d *Database) InvalidateDNNBlocksAfter(tx *Transaction, dnnBlockNum int64) error {
	query := "DELETE FROM dnn_blocks WHERE block_number > ?"
	_, err := tx.tx.Exec(query, dnnBlockNum)
	return err
}

// RemoveAnchorsAfterBlock removes anchor events after a certain DNN block
func (d *Database) RemoveAnchorsAfterBlock(tx *Transaction, dnnBlockNum int64) error {
	query := "DELETE FROM anchor_events WHERE dnn_block_number > ?"
	_, err := tx.tx.Exec(query, dnnBlockNum)
	return err
}

// UpdateDNNBlock updates or inserts a DNN block
func (d *Database) UpdateDNNBlock(tx *Transaction, dnnBlockNum, bitcoinBlockNum int64, blockHash string, blockTimestamp int64) error {
	query := `
		INSERT OR REPLACE INTO dnn_blocks
		(block_number, bitcoin_block_number, bitcoin_block_hash, block_timestamp, synced_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
	`
	_, err := tx.tx.Exec(query, dnnBlockNum, bitcoinBlockNum, blockHash, blockTimestamp)
	return err
}

// GetAnchorByTransactionID retrieves an anchor event by Bitcoin transaction ID
func (d *Database) GetAnchorByTransactionID(tx *Transaction, txID string) (*AnchorRecord, error) {
	query := `
		SELECT
			a.id, a.pubkey, a.created_at,
			a.bitcoin_block_number, a.dnn_block_number, a.position,
			n.primary_name, n.other_names, n.content as name_content,
			c.content as connection_content,
			m.content as metadata_content
		FROM anchor_events a
		JOIN name_events n ON a.name_event_id = n.id
		JOIN connection_events c ON a.connection_event_id = c.id
		JOIN metadata_events m ON a.metadata_event_id = m.id
		WHERE a.transaction_id = ?
	`

	var record AnchorRecord
	err := tx.tx.QueryRow(query, txID).Scan(
		&record.ID,
		&record.Pubkey,
		&record.CreatedAt,
		&record.BitcoinBlock,
		&record.DNNBlock,
		&record.Position,
		&record.Name,
		&record.OtherNames,
		&record.NameContent,
		&record.ConnectionContent,
		&record.MetadataContent,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	return &record, err
}

// UpdateAnchorPosition updates an anchor's block and position after reorg
func (d *Database) UpdateAnchorPosition(tx *Transaction, anchorID string, dnnBlock, bitcoinBlock int64, position int) error {
	query := `
		UPDATE anchor_events
		SET dnn_block_number = ?, bitcoin_block_number = ?, position = ?
		WHERE id = ?
	`
	_, err := tx.tx.Exec(query, dnnBlock, bitcoinBlock, position, anchorID)
	return err
}

// GetAnchorBlockPosition retrieves the bitcoin block, dnn block, and position for an anchor by pubkey and d-tag
func (d *Database) GetAnchorBlockPosition(pubkey, dTag string) (bitcoinBlock, dnnBlock int64, position int, err error) {
	err = d.db.QueryRow(
		"SELECT bitcoin_block_number, dnn_block_number, position FROM anchor_events WHERE pubkey = ? AND d_tag = ? LIMIT 1",
		pubkey, dTag,
	).Scan(&bitcoinBlock, &dnnBlock, &position)
	return
}

// GetSyncState retrieves a sync state value
func (d *Database) GetSyncState(key string) (string, error) {
	var value string
	err := d.db.QueryRow("SELECT value FROM sync_state WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// CreateOrUpdateDNNBlock creates or updates a DNN block entry
func (d *Database) CreateOrUpdateDNNBlock(dnnBlockNum, bitcoinBlockNum int64, blockHash string, blockTimestamp int64) error {
	query := `
		INSERT OR REPLACE INTO dnn_blocks
		(block_number, bitcoin_block_number, bitcoin_block_hash, block_timestamp, created_at, synced_at)
		VALUES (?, ?, ?, ?, COALESCE((SELECT created_at FROM dnn_blocks WHERE block_number = ?), ?), ?)
	`

	now := time.Now().Unix()
	_, err := d.db.Exec(query, dnnBlockNum, bitcoinBlockNum, blockHash, blockTimestamp, dnnBlockNum, now, now)
	return err
}

// StoreBitcoinTransaction stores a valid Bitcoin self-transfer transaction
func (d *Database) StoreBitcoinTransaction(txID string, bitcoinBlock, dnnBlock int64, position int, address string, feeRate, tiePosition, tieDigit int) error {
	query := `
		INSERT OR IGNORE INTO bitcoin_transactions
		(transaction_id, bitcoin_block_number, dnn_block_number, position, bitcoin_address, fee_rate, tie_position, tie_digit, discovered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	now := time.Now().Unix()
	_, err := d.db.Exec(query, txID, bitcoinBlock, dnnBlock, position, address, feeRate, tiePosition, tieDigit, now)
	return err
}

// GetBitcoinTransaction retrieves a Bitcoin transaction by its ID
func (d *Database) GetBitcoinTransaction(txID string) (*BitcoinTransactionRecord, error) {
	query := `
		SELECT transaction_id, bitcoin_block_number, dnn_block_number, position, bitcoin_address, fee_rate
		FROM bitcoin_transactions
		WHERE transaction_id = ?
	`

	var tx BitcoinTransactionRecord
	err := d.db.QueryRow(query, txID).Scan(
		&tx.TransactionID,
		&tx.BitcoinBlock,
		&tx.DNNBlock,
		&tx.Position,
		&tx.BitcoinAddress,
		&tx.FeeRate,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	return &tx, err
}

// UpdateBitcoinTransactionWithAnchor updates a Bitcoin transaction when its anchor event is found
func (d *Database) UpdateBitcoinTransactionWithAnchor(txID, anchorEventID string) error {
	query := `
		UPDATE bitcoin_transactions
		SET has_anchor_event = 1, anchor_event_id = ?
		WHERE transaction_id = ?
	`

	_, err := d.db.Exec(query, anchorEventID, txID)
	return err
}

// Close closes the database connection
func (d *Database) Close() error {
	return d.db.Close()
}

// RawQuery executes a raw SQL query and returns the rows
func (d *Database) RawQuery(query string, args ...interface{}) (*sql.Rows, error) {
	return d.db.Query(query, args...)
}

// IsBlocked checks if a DNN ID (or specific name) has an explicit "block" mark.
// This delegates to QueryBuilder.IsBlocked for the actual logic.
func (d *Database) IsBlocked(dnnBlock int64, position int, name string) (bool, error) {
	qb := &QueryBuilder{db: d}
	return qb.IsBlocked(dnnBlock, position, name)
}

// IsBadMarked checks if a DNN ID has a 'block' mark (backward-compatible wrapper).
func (d *Database) IsBadMarked(dnnBlock int64, position int) (bool, error) {
	return d.IsBlocked(dnnBlock, position, "")
}

// AnchorRecord represents a complete DNN name record
type AnchorRecord struct {
	ID                string
	Pubkey            string
	CreatedAt         int64
	BitcoinBlock      int64
	DNNBlock          int64
	Position          int
	Name              string
	OtherNames        string // JSON array
	NameContent       string
	ConnectionContent string // JSON
	MetadataContent   string // JSON

	// Raw connection event fields for signature verification
	ConnEventID        string
	ConnEventPubkey    string
	ConnEventSig       string
	ConnEventCreatedAt int64
	ConnEventTagsJSON  string
}

// ========== Transaction Block Index Functions ==========

// StoreTxBlockIndexBatch stores a batch of transaction-to-block mappings
// This is used to persist the txToBlockHash index for input address resolution
func (d *Database) StoreTxBlockIndexBatch(txids []string, blockHash string) error {
	if len(txids) == 0 {
		return nil
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR REPLACE INTO tx_block_index (txid, block_hash) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, txid := range txids {
		if _, err := stmt.Exec(txid, blockHash); err != nil {
			return fmt.Errorf("failed to insert tx %s: %w", txid[:16], err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetBlockHashForTx retrieves the block hash containing a given transaction
func (d *Database) GetBlockHashForTx(txid string) (string, error) {
	var blockHash string
	err := d.db.QueryRow("SELECT block_hash FROM tx_block_index WHERE txid = ?", txid).Scan(&blockHash)
	if err == sql.ErrNoRows {
		return "", nil // Not found, return empty string
	}
	if err != nil {
		return "", fmt.Errorf("failed to query tx_block_index: %w", err)
	}
	return blockHash, nil
}

// GetTxBlockIndexCount returns the number of indexed transactions
func (d *Database) GetTxBlockIndexCount() (int64, error) {
	var count int64
	err := d.db.QueryRow("SELECT COUNT(*) FROM tx_block_index").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count tx_block_index: %w", err)
	}
	return count, nil
}
