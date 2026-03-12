package database

import (
	"database/sql"
	"fmt"
	"log"
)

// Migration represents a database migration
type Migration struct {
	Version     int
	Description string
	SQL         string
}

// MigrationManager handles database migrations
type MigrationManager struct {
	db         *sql.DB
	migrations []Migration
}

// NewMigrationManager creates a new migration manager
func NewMigrationManager(db *sql.DB) *MigrationManager {
	return &MigrationManager{
		db:         db,
		migrations: getMigrations(),
	}
}

// Run executes all pending migrations
func (mm *MigrationManager) Run() error {
	// Create migrations table if it doesn't exist
	if err := mm.createMigrationsTable(); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get current version
	currentVersion, err := mm.getCurrentVersion()
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	log.Printf("Current database version: %d", currentVersion)

	// Run pending migrations
	for _, migration := range mm.migrations {
		if migration.Version > currentVersion {
			log.Printf("Running migration %d: %s", migration.Version, migration.Description)

			if err := mm.runMigration(migration); err != nil {
				return fmt.Errorf("migration %d failed: %w", migration.Version, err)
			}

			log.Printf("Migration %d completed successfully", migration.Version)
		}
	}

	return nil
}

// createMigrationsTable creates the migrations tracking table
func (mm *MigrationManager) createMigrationsTable() error {
	query := `
		CREATE TABLE IF NOT EXISTS migrations (
			version INTEGER PRIMARY KEY,
			description TEXT,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`
	_, err := mm.db.Exec(query)
	return err
}

// getCurrentVersion gets the current migration version
func (mm *MigrationManager) getCurrentVersion() (int, error) {
	var version int
	err := mm.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM migrations").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// runMigration executes a single migration
func (mm *MigrationManager) runMigration(migration Migration) error {
	tx, err := mm.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Execute migration SQL
	if _, err := tx.Exec(migration.SQL); err != nil {
		return err
	}

	// Record migration
	_, err = tx.Exec(
		"INSERT INTO migrations (version, description) VALUES (?, ?)",
		migration.Version, migration.Description,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// getMigrations returns all database migrations
func getMigrations() []Migration {
	return []Migration{
		{
			Version:     1,
			Description: "Initial schema",
			SQL: `
				-- DNN Blocks table (corresponds to Bitcoin blocks)
				CREATE TABLE IF NOT EXISTS dnn_blocks (
					block_number INTEGER PRIMARY KEY,
					bitcoin_block_number INTEGER NOT NULL UNIQUE,
					bitcoin_block_hash TEXT NOT NULL,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					synced_at TIMESTAMP,
					CHECK (bitcoin_block_number >= 1000000)
				);

				-- Name events (kind 61600)
				CREATE TABLE IF NOT EXISTS name_events (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					other_names TEXT,
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
					content TEXT NOT NULL,
					sig TEXT NOT NULL,
					UNIQUE(pubkey)
				);

				-- Metadata events (kind 63600)
				CREATE TABLE IF NOT EXISTS metadata_events (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					content TEXT NOT NULL,
					sig TEXT NOT NULL,
					UNIQUE(pubkey)
				);

				-- Anchor events (kind 60600)
				CREATE TABLE IF NOT EXISTS anchor_events (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					name_event_id TEXT NOT NULL,
					connection_event_id TEXT NOT NULL,
					metadata_event_id TEXT NOT NULL,
					transaction_id TEXT NOT NULL,
					bitcoin_block_number INTEGER NOT NULL,
					dnn_block_number INTEGER NOT NULL,
					position INTEGER NOT NULL,
					content TEXT,
					sig TEXT NOT NULL,
					FOREIGN KEY (name_event_id) REFERENCES name_events(id),
					FOREIGN KEY (connection_event_id) REFERENCES connection_events(id),
					FOREIGN KEY (metadata_event_id) REFERENCES metadata_events(id),
					FOREIGN KEY (dnn_block_number) REFERENCES dnn_blocks(block_number),
					UNIQUE(transaction_id)
				);

				-- Create indexes
				CREATE INDEX IF NOT EXISTS idx_anchor_events_bitcoin_block ON anchor_events(bitcoin_block_number);
				CREATE INDEX IF NOT EXISTS idx_anchor_events_dnn_block ON anchor_events(dnn_block_number);
				CREATE INDEX IF NOT EXISTS idx_anchor_events_pubkey ON anchor_events(pubkey);
				CREATE INDEX IF NOT EXISTS idx_name_events_d_tag ON name_events(d_tag);
			`,
		},
		{
			Version:     2,
			Description: "Add awareness database",
			SQL: `
				CREATE TABLE IF NOT EXISTS awareness_marks (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					anchor_event_id TEXT NOT NULL,
					marker_pubkey TEXT NOT NULL,
					mark_type TEXT NOT NULL,
					reason TEXT,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (anchor_event_id) REFERENCES anchor_events(id),
					UNIQUE(anchor_event_id, marker_pubkey)
				);

				CREATE INDEX IF NOT EXISTS idx_awareness_marks_anchor ON awareness_marks(anchor_event_id);
				CREATE INDEX IF NOT EXISTS idx_awareness_marks_type ON awareness_marks(mark_type);
			`,
		},
		{
			Version:     3,
			Description: "Add peer nodes and sync state",
			SQL: `
				CREATE TABLE IF NOT EXISTS peer_nodes (
					pubkey TEXT PRIMARY KEY,
					relay_url TEXT NOT NULL,
					last_seen TIMESTAMP,
					last_sync TIMESTAMP,
					is_active BOOLEAN DEFAULT 1,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				);

				CREATE TABLE IF NOT EXISTS sync_state (
					key TEXT PRIMARY KEY,
					value TEXT,
					updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				);

				INSERT OR IGNORE INTO sync_state (key, value) VALUES
					('last_bitcoin_block', '999999'),
					('last_dnn_block', '-1'),
					('last_sync_time', '0'),
					('last_reorg_check', '0');
			`,
		},
		{
			Version:     4,
			Description: "Add Bitcoin address tracking",
			SQL: `
				CREATE TABLE IF NOT EXISTS bitcoin_addresses (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					address TEXT NOT NULL UNIQUE,
					pubkey TEXT NOT NULL,
					derived_from TEXT,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				);

				CREATE INDEX IF NOT EXISTS idx_bitcoin_addresses_pubkey ON bitcoin_addresses(pubkey);
			`,
		},
		{
			Version:     5,
			Description: "Add reserved addresses",
			SQL: `
				CREATE TABLE IF NOT EXISTS reserved_addresses (
					address TEXT PRIMARY KEY,
					reserved_for TEXT NOT NULL,
					description TEXT,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				);

				INSERT OR IGNORE INTO reserved_addresses (address, reserved_for, description) VALUES
					('n0.0', 'DNN Node Registry', 'Lists community-published DNN node builds and versions'),
					('b1m.0', 'Bitcoin Node Registry', 'Lists Bitcoin node builds and versions'),
					('b1000000.0', 'Bitcoin Node Registry', 'Alternative notation for Bitcoin node registry');
			`,
		},
		{
			Version:     6,
			Description: "Add metrics tracking",
			SQL: `
				CREATE TABLE IF NOT EXISTS metrics (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					metric_name TEXT NOT NULL,
					metric_value REAL NOT NULL,
					metric_type TEXT NOT NULL,
					recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				);

				CREATE INDEX IF NOT EXISTS idx_metrics_name ON metrics(metric_name);
				CREATE INDEX IF NOT EXISTS idx_metrics_time ON metrics(recorded_at);

				-- Keep only last 7 days of metrics
				CREATE TRIGGER IF NOT EXISTS cleanup_old_metrics
				AFTER INSERT ON metrics
				BEGIN
					DELETE FROM metrics WHERE recorded_at < datetime('now', '-7 days');
				END;
			`,
		},
		{
			Version:     7,
			Description: "Add event cache for performance",
			SQL: `
				CREATE TABLE IF NOT EXISTS event_cache (
					event_id TEXT PRIMARY KEY,
					event_kind INTEGER NOT NULL,
					event_data TEXT NOT NULL,
					cached_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					expires_at TIMESTAMP
				);

				CREATE INDEX IF NOT EXISTS idx_event_cache_kind ON event_cache(event_kind);
				CREATE INDEX IF NOT EXISTS idx_event_cache_expires ON event_cache(expires_at);

				-- Cleanup expired cache entries
				CREATE TRIGGER IF NOT EXISTS cleanup_expired_cache
				AFTER INSERT ON event_cache
				BEGIN
					DELETE FROM event_cache WHERE expires_at < datetime('now');
				END;
			`,
		},
		{
			Version:     8,
			Description: "Add full-text search for names (simplified for SQLite compatibility)",
			SQL: `
				-- FTS5 not available in all SQLite builds
				-- Using regular indexes instead for name search

				-- Add index for name searches (already exists from initial schema)
				-- This migration is a placeholder for future full-text search enhancement

				-- When FTS5 is available, we can create:
				-- CREATE VIRTUAL TABLE name_search USING fts5(name, other_names, description);

				SELECT 1; -- No-op migration to maintain version sequence
			`,
		},
		{
			Version:     9,
			Description: "Add Bitcoin block timestamps",
			SQL: `
				-- Add block_timestamp to dnn_blocks table
				ALTER TABLE dnn_blocks ADD COLUMN block_timestamp INTEGER;

				-- Add index for timestamp queries
				CREATE INDEX IF NOT EXISTS idx_dnn_blocks_timestamp ON dnn_blocks(block_timestamp);

				-- Note: Existing blocks will have NULL timestamps until re-synced
				-- Nodes should update timestamps as they sync new blocks or re-process old ones
			`,
		},
		{
			Version:     10,
			Description: "Fix addressable replaceable events - add d_tag to connection and metadata events",
			SQL: `
				-- Create new tables with correct schema
				CREATE TABLE IF NOT EXISTS connection_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					content TEXT NOT NULL,
					sig TEXT NOT NULL,
					UNIQUE(pubkey, d_tag)
				);

				CREATE TABLE IF NOT EXISTS metadata_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					content TEXT NOT NULL,
					sig TEXT NOT NULL,
					UNIQUE(pubkey, d_tag)
				);

				-- Migrate existing data (use empty d_tag for old events)
				INSERT OR IGNORE INTO connection_events_new (id, pubkey, created_at, updated_at, d_tag, content, sig)
				SELECT id, pubkey, created_at, updated_at, '', content, sig
				FROM connection_events;

				INSERT OR IGNORE INTO metadata_events_new (id, pubkey, created_at, updated_at, d_tag, content, sig)
				SELECT id, pubkey, created_at, updated_at, '', content, sig
				FROM metadata_events;

				-- Drop old tables
				DROP TABLE connection_events;
				DROP TABLE metadata_events;

				-- Rename new tables
				ALTER TABLE connection_events_new RENAME TO connection_events;
				ALTER TABLE metadata_events_new RENAME TO metadata_events;

				-- Create indexes for d_tag
				CREATE INDEX IF NOT EXISTS idx_connection_events_d_tag ON connection_events(d_tag);
				CREATE INDEX IF NOT EXISTS idx_metadata_events_d_tag ON metadata_events(d_tag);
			`,
		},
		{
			Version:     11,
			Description: "Make anchor events addressable replaceable with naddr references (BREAKING: drops anchor data)",
			SQL: `
				-- BREAKING CHANGE: Drop and recreate anchor_events with new schema
				-- This combines the addressable replaceable change + naddr references
				-- Since project isn't live, dropping data is acceptable

				-- Drop old table completely
				DROP TABLE IF EXISTS anchor_events;

				-- Create new table with d_tag and naddr references
				CREATE TABLE anchor_events (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					d_tag TEXT NOT NULL,
					name_event_ref TEXT NOT NULL,
					connection_event_ref TEXT NOT NULL,
					metadata_event_ref TEXT NOT NULL,
					transaction_id TEXT NOT NULL,
					bitcoin_block_number INTEGER NOT NULL,
					dnn_block_number INTEGER NOT NULL,
					position INTEGER NOT NULL,
					content TEXT,
					sig TEXT NOT NULL,
					FOREIGN KEY (transaction_id) REFERENCES bitcoin_transactions(transaction_id),
					FOREIGN KEY (dnn_block_number) REFERENCES dnn_blocks(block_number),
					UNIQUE(pubkey, d_tag),
					UNIQUE(transaction_id)
				);

				-- Create indexes
				CREATE INDEX idx_anchor_events_bitcoin_block ON anchor_events(bitcoin_block_number);
				CREATE INDEX idx_anchor_events_dnn_block ON anchor_events(dnn_block_number);
				CREATE INDEX idx_anchor_events_pubkey ON anchor_events(pubkey);
				CREATE INDEX idx_anchor_events_d_tag ON anchor_events(d_tag);
				CREATE INDEX idx_anchor_events_pubkey_d_tag ON anchor_events(pubkey, d_tag);
			`,
		},
		{
			Version:     12,
			Description: "Add primary_name column to name_events for proper name resolution",
			SQL: `
				-- Add primary_name column to name_events (if it doesn't exist)
				-- SQLite doesn't support IF NOT EXISTS for ALTER TABLE ADD COLUMN
				-- So we'll check if the column exists in a different way

				-- Create new table with primary_name column
				CREATE TABLE IF NOT EXISTS name_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					primary_name TEXT DEFAULT '',
					other_names TEXT,
					content TEXT,
					sig TEXT NOT NULL,
					UNIQUE(pubkey, d_tag)
				);

				-- Copy data from old table, setting primary_name to empty string
				INSERT OR IGNORE INTO name_events_new (id, pubkey, created_at, updated_at, d_tag, primary_name, other_names, content, sig)
				SELECT id, pubkey, created_at, updated_at, d_tag, '', other_names, content, sig
				FROM name_events;

				-- Drop old table
				DROP TABLE name_events;

				-- Rename new table
				ALTER TABLE name_events_new RENAME TO name_events;

				-- Create index for primary_name lookups
				CREATE INDEX IF NOT EXISTS idx_name_events_primary_name ON name_events(primary_name);
				CREATE INDEX IF NOT EXISTS idx_name_events_d_tag ON name_events(d_tag);
			`,
		},
		{
			Version:     13,
			Description: "Add d_tag column to anchor_events for NIP-33 addressable events",
			SQL: `
				-- Check if anchor_events table has any data
				-- If empty (fresh install), just drop and recreate
				-- If has data (upgrade), copy with column renaming
				
				-- Create new table structure
				CREATE TABLE IF NOT EXISTS anchor_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					d_tag TEXT NOT NULL DEFAULT '',
					name_event_ref TEXT NOT NULL,
					connection_event_ref TEXT NOT NULL,
					metadata_event_ref TEXT NOT NULL,
					transaction_id TEXT NOT NULL,
					bitcoin_block_number INTEGER NOT NULL,
					dnn_block_number INTEGER NOT NULL,
					position INTEGER NOT NULL,
					content TEXT,
					sig TEXT NOT NULL,
					FOREIGN KEY (dnn_block_number) REFERENCES dnn_blocks(block_number),
					UNIQUE(transaction_id)
				);

				-- Drop old table (if empty, this is safe)
				DROP TABLE IF EXISTS anchor_events;

				-- Rename new table
				ALTER TABLE anchor_events_new RENAME TO anchor_events;

				-- Recreate indexes
				CREATE INDEX IF NOT EXISTS idx_anchor_events_bitcoin_block ON anchor_events(bitcoin_block_number);
				CREATE INDEX IF NOT EXISTS idx_anchor_events_dnn_block ON anchor_events(dnn_block_number);
				CREATE INDEX IF NOT EXISTS idx_anchor_events_pubkey ON anchor_events(pubkey);
				CREATE INDEX IF NOT EXISTS idx_anchor_events_d_tag ON anchor_events(d_tag);
			`,
		},
		{
			Version:     14,
			Description: "Add tags_json column to store full event tags",
			SQL: `
				-- Add tags_json column to connection_events
				ALTER TABLE connection_events ADD COLUMN tags_json TEXT DEFAULT '[]';

				-- Add tags_json column to metadata_events
				ALTER TABLE metadata_events ADD COLUMN tags_json TEXT DEFAULT '[]';

				-- Add tags_json column to name_events
				ALTER TABLE name_events ADD COLUMN tags_json TEXT DEFAULT '[]';
			`,
		},
		{
			Version:     15,
			Description: "Refactor awareness to DNN ID-based schema",
			SQL: `
				-- Drop old awareness_marks table (not in production yet)
				DROP TABLE IF EXISTS awareness_marks;

				-- Local marks (from admin npub)
				CREATE TABLE IF NOT EXISTS awareness_marks_local (
					dnn_block INTEGER NOT NULL,
					position INTEGER NOT NULL,
					mark_type TEXT NOT NULL CHECK(mark_type IN ('good', 'bad')),
					reason TEXT,
					updated_at INTEGER DEFAULT (strftime('%s', 'now')),
					PRIMARY KEY (dnn_block, position)
				);

				-- Peer marks (from other DNN nodes)
				CREATE TABLE IF NOT EXISTS awareness_marks_peers (
					dnn_block INTEGER NOT NULL,
					position INTEGER NOT NULL,
					peer_pubkey TEXT NOT NULL,
					mark_type TEXT NOT NULL CHECK(mark_type IN ('good', 'bad')),
					reason TEXT,
					received_at INTEGER DEFAULT (strftime('%s', 'now')),
					PRIMARY KEY (dnn_block, position, peer_pubkey)
				);

				CREATE INDEX IF NOT EXISTS idx_awareness_local_type ON awareness_marks_local(mark_type);
				CREATE INDEX IF NOT EXISTS idx_awareness_peers_peer ON awareness_marks_peers(peer_pubkey);
				CREATE INDEX IF NOT EXISTS idx_awareness_peers_type ON awareness_marks_peers(mark_type);
			`,
		},
		{
			Version:     16,
			Description: "Add composite indexes for event lookups by pubkey + d_tag + created_at",
			SQL: `
				-- Add composite index for connection_events lookups
				CREATE INDEX IF NOT EXISTS idx_connection_events_pubkey_dtag_created ON connection_events(pubkey, d_tag, created_at DESC);

				-- Add composite index for metadata_events lookups
				CREATE INDEX IF NOT EXISTS idx_metadata_events_pubkey_dtag_created ON metadata_events(pubkey, d_tag, created_at DESC);

				-- Add composite index for name_events lookups
				CREATE INDEX IF NOT EXISTS idx_name_events_pubkey_dtag ON name_events(pubkey, d_tag);
			`,
		},
		{
			Version:     17,
			Description: "Add relay_checked_at to bitcoin_transactions for one-time relay checks",
			SQL: `
				-- Add relay_checked_at column to track when we last checked relays for anchor
				-- NULL = never checked, non-NULL = already checked (don't re-query)
				ALTER TABLE bitcoin_transactions ADD COLUMN relay_checked_at INTEGER DEFAULT NULL;

				-- Add index for finding unchecked transactions
				CREATE INDEX IF NOT EXISTS idx_bitcoin_transactions_unchecked ON bitcoin_transactions(relay_checked_at) WHERE relay_checked_at IS NULL;
			`,
		},
		{
			Version:     18,
			Description: "Drop unused bitcoin_addresses table",
			SQL: `
				-- The bitcoin_addresses table was never used.
				-- Bitcoin addresses are stored directly in bitcoin_transactions.
				DROP TABLE IF EXISTS bitcoin_addresses;
				DROP INDEX IF EXISTS idx_bitcoin_addresses_pubkey;
			`,
		},
		{
			Version:     19,
			Description: "Add updated_at column to anchor_events for conflict resolution",
			SQL: `
				-- Add updated_at column to anchor_events
				ALTER TABLE anchor_events ADD COLUMN updated_at INTEGER;
				
				-- Set updated_at to created_at for existing rows
				UPDATE anchor_events SET updated_at = created_at WHERE updated_at IS NULL;
			`,
		},
		{
			Version:     20,
			Description: "Enhance peer_nodes for Kind 64600 node discovery",
			SQL: `
				-- Recreate peer_nodes with enhanced schema for 64600 events
				CREATE TABLE IF NOT EXISTS peer_nodes_new (
					pubkey TEXT PRIMARY KEY,
					event_id TEXT,
					event_created_at INTEGER,
					event_updated_at INTEGER,
					-- Address fields (JSON arrays)
					dns_addresses_json TEXT DEFAULT '[]',
					tor_json TEXT DEFAULT '[]',
					relays_json TEXT DEFAULT '[]',
					npub_json TEXT DEFAULT '[]',
					custom_transports_json TEXT DEFAULT '{}',
					-- Verification state
					is_verified BOOLEAN DEFAULT 0,
					last_ping_at INTEGER,
					last_ping_success BOOLEAN,
					ping_latency_ms INTEGER,
					-- Status
					is_active BOOLEAN DEFAULT 1,
					-- Timestamps
					discovered_at INTEGER DEFAULT (strftime('%s', 'now')),
					last_seen INTEGER
				);

				-- Migrate existing data (if any)
				INSERT OR IGNORE INTO peer_nodes_new (pubkey, relays_json, last_seen, is_active, discovered_at)
				SELECT pubkey, 
					CASE WHEN relay_url IS NOT NULL AND relay_url != '' 
						THEN '["' || relay_url || '"]' 
						ELSE '[]' 
					END,
					CAST(strftime('%s', last_seen) AS INTEGER),
					is_active,
					CAST(strftime('%s', created_at) AS INTEGER)
				FROM peer_nodes WHERE pubkey IS NOT NULL;

				-- Drop old table
				DROP TABLE IF EXISTS peer_nodes;

				-- Rename new table
				ALTER TABLE peer_nodes_new RENAME TO peer_nodes;

				-- Create indexes
				CREATE INDEX IF NOT EXISTS idx_peer_nodes_verified ON peer_nodes(is_verified);
				CREATE INDEX IF NOT EXISTS idx_peer_nodes_active ON peer_nodes(is_active);
			`,
		},
		{
			Version:     21,
			Description: "Clean up old anchor events with invalid tag format (names/connection/metadata/transaction instead of n/c/m/x)",
			SQL: `
				-- Delete all anchor events to force re-sync with validated tags
				-- The syncer will only accept anchors with new n/c/m/x tag format
				DELETE FROM anchor_events;

				-- Reset bitcoin_transactions link to anchors so syncer can re-link
				UPDATE bitcoin_transactions SET has_anchor_event = 0, anchor_event_id = NULL;

				-- Reset relay_checked_at so syncer will re-query relays for anchors
				UPDATE bitcoin_transactions SET relay_checked_at = NULL;

				-- Also clean up orphaned name/connection/metadata events that may have old format
				-- These will be re-fetched when anchors are re-synced
				DELETE FROM name_events;
				DELETE FROM connection_events;
				DELETE FROM metadata_events;
			`,
		},
		{
			Version:     22,
			Description: "Add tags_json column to anchor_events for storing original event tags",
			SQL: `
				-- Add tags_json column to store original event tags as JSON
				-- This preserves the exact tags from the original event
				ALTER TABLE anchor_events ADD COLUMN tags_json TEXT DEFAULT '[]';
			`,
		},
		{
			Version:     23,
			Description: "Create discovered_relays table for persisting user NIP-65 relays",
			SQL: `
				CREATE TABLE IF NOT EXISTS discovered_relays (
					url TEXT PRIMARY KEY,
					source TEXT NOT NULL DEFAULT 'nip65',
					discovered_by TEXT,
					discovered_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
					last_seen INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
					is_active INTEGER NOT NULL DEFAULT 1
				);
				CREATE INDEX IF NOT EXISTS idx_discovered_relays_source ON discovered_relays(source);
				CREATE INDEX IF NOT EXISTS idx_discovered_relays_active ON discovered_relays(is_active);
			`,
		},
		{
			Version:     24,
			Description: "Allow multiple versions per event (remove UNIQUE(pubkey, d_tag) constraints)",
			SQL: `
				-- Recreate name_events without UNIQUE(pubkey, d_tag)
				CREATE TABLE IF NOT EXISTS name_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					primary_name TEXT DEFAULT '',
					other_names TEXT,
					content TEXT,
					sig TEXT NOT NULL,
					tags_json TEXT DEFAULT '[]'
				);

				INSERT INTO name_events_new SELECT * FROM name_events;
				DROP TABLE name_events;
				ALTER TABLE name_events_new RENAME TO name_events;
				CREATE INDEX IF NOT EXISTS idx_name_events_pubkey_dtag ON name_events(pubkey, d_tag);
				CREATE INDEX IF NOT EXISTS idx_name_events_primary_name ON name_events(primary_name);
				CREATE INDEX IF NOT EXISTS idx_name_events_d_tag ON name_events(d_tag);
				CREATE INDEX IF NOT EXISTS idx_name_events_created_at ON name_events(created_at DESC);

				-- Recreate connection_events without UNIQUE(pubkey, d_tag)
				CREATE TABLE IF NOT EXISTS connection_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					content TEXT NOT NULL,
					sig TEXT NOT NULL,
					tags_json TEXT DEFAULT '[]'
				);

				INSERT INTO connection_events_new SELECT * FROM connection_events;
				DROP TABLE connection_events;
				ALTER TABLE connection_events_new RENAME TO connection_events;
				CREATE INDEX IF NOT EXISTS idx_connection_events_pubkey_dtag ON connection_events(pubkey, d_tag);
				CREATE INDEX IF NOT EXISTS idx_connection_events_d_tag ON connection_events(d_tag);
				CREATE INDEX IF NOT EXISTS idx_connection_events_pubkey_dtag_created ON connection_events(pubkey, d_tag, created_at DESC);

				-- Recreate metadata_events without UNIQUE(pubkey, d_tag)
				CREATE TABLE IF NOT EXISTS metadata_events_new (
					id TEXT PRIMARY KEY,
					pubkey TEXT NOT NULL,
					created_at INTEGER NOT NULL,
					updated_at INTEGER,
					d_tag TEXT NOT NULL,
					content TEXT NOT NULL,
					sig TEXT NOT NULL,
					tags_json TEXT DEFAULT '[]'
				);

				INSERT INTO metadata_events_new SELECT * FROM metadata_events;
				DROP TABLE metadata_events;
				ALTER TABLE metadata_events_new RENAME TO metadata_events;
				CREATE INDEX IF NOT EXISTS idx_metadata_events_pubkey_dtag ON metadata_events(pubkey, d_tag);
				CREATE INDEX IF NOT EXISTS idx_metadata_events_d_tag ON metadata_events(d_tag);
				CREATE INDEX IF NOT EXISTS idx_metadata_events_pubkey_dtag_created ON metadata_events(pubkey, d_tag, created_at DESC);
			`,
		},
		{
			Version:     25,
			Description: "Drop tx_block_index table - P2WPKH inputs get addresses from witness data",
			SQL: `
				-- The tx_block_index table was used to map transaction IDs to block hashes
				-- for resolving input addresses from previous transactions.
				-- Since DNN only accepts P2WPKH (bc1q) addresses, input addresses
				-- can be derived directly from witness data (33-byte compressed pubkey).
				-- This saves ~50% of database storage.
				DROP TABLE IF EXISTS tx_block_index;
				DROP INDEX IF EXISTS idx_tx_block_index_hash;
			`,
		},
		{
			Version:     26,
			Description: "Enhanced awareness: allow/neutral/block marks, categories, name targeting",
			SQL: `
				-- Drop old awareness tables (admin re-syncs after upgrade)
				DROP TABLE IF EXISTS awareness_marks_local;
				DROP TABLE IF EXISTS awareness_marks_peers;

				-- Local marks (from admin npub) with category + name targeting
				CREATE TABLE awareness_marks_local (
					dnn_block INTEGER NOT NULL,
					position INTEGER NOT NULL,
					name TEXT NOT NULL DEFAULT '',
					mark TEXT NOT NULL CHECK(mark IN ('allow', 'neutral', 'block')),
					category TEXT NOT NULL DEFAULT '',
					reason TEXT,
					updated_at INTEGER DEFAULT (strftime('%s', 'now')),
					PRIMARY KEY (dnn_block, position, name)
				);

				-- Peer marks (from other DNN nodes)
				CREATE TABLE awareness_marks_peers (
					dnn_block INTEGER NOT NULL,
					position INTEGER NOT NULL,
					name TEXT NOT NULL DEFAULT '',
					peer_pubkey TEXT NOT NULL,
					mark TEXT NOT NULL CHECK(mark IN ('allow', 'neutral', 'block')),
					category TEXT NOT NULL DEFAULT '',
					reason TEXT,
					received_at INTEGER DEFAULT (strftime('%s', 'now')),
					PRIMARY KEY (dnn_block, position, name, peer_pubkey)
				);

				CREATE INDEX IF NOT EXISTS idx_awareness_local_mark ON awareness_marks_local(mark);
				CREATE INDEX IF NOT EXISTS idx_awareness_local_category ON awareness_marks_local(category);
				CREATE INDEX IF NOT EXISTS idx_awareness_local_name ON awareness_marks_local(name);
				CREATE INDEX IF NOT EXISTS idx_awareness_peers_peer ON awareness_marks_peers(peer_pubkey);
				CREATE INDEX IF NOT EXISTS idx_awareness_peers_mark ON awareness_marks_peers(mark);
				CREATE INDEX IF NOT EXISTS idx_awareness_peers_category ON awareness_marks_peers(category);
			`,
		},
		{
			Version:     27,
			Description: "Redesign peer_nodes: address-centric with verification",
			SQL: `
				-- Drop old pubkey-centric peer_nodes table
				DROP TABLE IF EXISTS peer_nodes;

				-- Create new address-centric peer_nodes table
				CREATE TABLE peer_nodes (
					address TEXT PRIMARY KEY,
					node_npub TEXT,
					node_pubkey TEXT,
					announced_by TEXT,
					is_verified BOOLEAN DEFAULT 1,
					fail_count INTEGER DEFAULT 0,
					last_checked INTEGER,
					last_seen INTEGER,
					discovered_at INTEGER DEFAULT (strftime('%s', 'now'))
				);

				CREATE INDEX IF NOT EXISTS idx_peer_nodes_pubkey ON peer_nodes(node_pubkey);
				CREATE INDEX IF NOT EXISTS idx_peer_nodes_fail ON peer_nodes(fail_count);
			`,
		},
		{
			Version:     28,
			Description: "Add admin_pubkey to peer_nodes for awareness sync",
			SQL: `
				-- The awareness list (kind:30000 d:dnn-awareness) is published by the admin npub,
				-- not the node pubkey. We need to store and query against admin pubkeys.
				ALTER TABLE peer_nodes ADD COLUMN admin_pubkey TEXT DEFAULT '';
			`,
		},
		{
			Version:     29,
			Description: "NIP-33 cleanup: deduplicate existing events, keep only latest per pubkey+d_tag",
			SQL: `
				-- Clean up name_events: keep only the latest per pubkey+d_tag
				DELETE FROM name_events WHERE id NOT IN (
					SELECT id FROM (
						SELECT id, ROW_NUMBER() OVER (PARTITION BY pubkey, d_tag ORDER BY created_at DESC) as rn
						FROM name_events
					) WHERE rn = 1
				);

				-- Clean up connection_events: keep only the latest per pubkey+d_tag
				DELETE FROM connection_events WHERE id NOT IN (
					SELECT id FROM (
						SELECT id, ROW_NUMBER() OVER (PARTITION BY pubkey, d_tag ORDER BY created_at DESC) as rn
						FROM connection_events
					) WHERE rn = 1
				);

				-- Clean up metadata_events: keep only the latest per pubkey+d_tag
				DELETE FROM metadata_events WHERE id NOT IN (
					SELECT id FROM (
						SELECT id, ROW_NUMBER() OVER (PARTITION BY pubkey, d_tag ORDER BY created_at DESC) as rn
						FROM metadata_events
					) WHERE rn = 1
				);
			`,
		},
	}
}

// Rollback rolls back to a specific version (for testing/recovery)
func (mm *MigrationManager) Rollback(targetVersion int) error {
	// This is a simplified rollback - in production you'd want down migrations
	log.Printf("WARNING: Rolling back to version %d - this will delete data!", targetVersion)

	// Delete migrations after target version
	_, err := mm.db.Exec("DELETE FROM migrations WHERE version > ?", targetVersion)
	if err != nil {
		return fmt.Errorf("failed to rollback migrations: %w", err)
	}

	log.Printf("Rolled back to version %d", targetVersion)
	return nil
}

// GetVersion returns the current database version
func (mm *MigrationManager) GetVersion() (int, error) {
	return mm.getCurrentVersion()
}

// GetPendingMigrations returns the list of pending migrations
func (mm *MigrationManager) GetPendingMigrations() ([]Migration, error) {
	currentVersion, err := mm.getCurrentVersion()
	if err != nil {
		return nil, err
	}

	var pending []Migration
	for _, m := range mm.migrations {
		if m.Version > currentVersion {
			pending = append(pending, m)
		}
	}

	return pending, nil
}
