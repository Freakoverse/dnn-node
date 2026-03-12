package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Config represents the DNN node configuration
type Config struct {
	// Node identity
	NodePrivateKey string `json:"node_private_key"`
	NodePubkey     string `json:"node_pubkey"`
	NodeNpub       string `json:"node_npub"`

	// Network settings
	Network   string   `json:"network"` // "mainnet", "testnet", or "dev"
	Port      int      `json:"port"`
	DataDir   string   `json:"data_dir"`
	RelayURLs []string `json:"relay_urls"`
	PeerNodes []string `json:"peer_nodes"`

	// Bitcoin P2P settings
	BitcoinRPC struct {
		UseP2P   bool     `json:"use_p2p"`   // Use Bitcoin P2P protocol for block data
		P2PPeers []string `json:"p2p_peers"` // Bitcoin P2P node addresses (host:port)
	} `json:"bitcoin_rpc"`

	// Sync settings
	SyncInterval   int `json:"sync_interval"`    // in seconds
	BlockBatchSize int `json:"block_batch_size"` // number of blocks to process at once
	MaxConnections int `json:"max_connections"`  // max WebSocket connections

	// DNS Server settings
	DNS struct {
		Enabled      bool     `json:"enabled"`       // enable DNS server
		Port         int      `json:"port"`          // DNS server port (default 53)
		UpstreamDNS  []string `json:"upstream_dns"`  // upstream DNS servers
		CacheTTL     int      `json:"cache_ttl"`     // cache TTL in seconds
		QueryTimeout int      `json:"query_timeout"` // query timeout in seconds
	} `json:"dns"`

	// Awareness DB settings
	EnableAwareness bool `json:"enable_awareness"` // enable the awareness database feature

	AdminNpub         string   `json:"admin_npub"`         // admin npub for awareness management
	AnnounceAddresses []string `json:"announce_addresses"` // DNS/Tor addresses to announce for peer discovery
}

// Load loads or creates a configuration
func Load(configPath string, port int, dataDir string, initMode bool) (*Config, error) {
	cfg := &Config{
		Network:         "testnet", // Default to testnet for safety
		Port:            port,
		DataDir:         dataDir,
		SyncInterval:    60, // 1 minute (faster sync)
		BlockBatchSize:  5,  // Process 5 blocks per sync = 300 blocks/hour (600 requests/hour)
		MaxConnections:  1000,
		EnableAwareness: true,

		AdminNpub: "",
		RelayURLs: []string{
			"wss://relay.damus.io",
			"wss://relay.nostr.band",
			"wss://nos.lol",
			"wss://relay.primal.net",
		},
		PeerNodes: []string{
			// Add default peer nodes when available
		},
		AnnounceAddresses: []string{},
	}

	// Set Bitcoin P2P defaults (100% decentralized mode)
	cfg.BitcoinRPC.UseP2P = true         // Enable P2P mode by default
	cfg.BitcoinRPC.P2PPeers = []string{} // Use default DNS seeds

	// Set DNS server defaults (enabled by default on port 53)
	cfg.DNS.Enabled = true
	cfg.DNS.Port = 53
	cfg.DNS.UpstreamDNS = []string{"8.8.8.8:53", "1.1.1.1:53"}
	cfg.DNS.CacheTTL = 300
	cfg.DNS.QueryTimeout = 5

	// Create data directory if it doesn't exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// If init mode or config doesn't exist, create new keypair
	if initMode || !fileExists(configPath) {
		// Generate new keypair
		sk := nostr.GeneratePrivateKey()
		pubkey, err := nostr.GetPublicKey(sk)
		if err != nil {
			return nil, fmt.Errorf("failed to generate public key: %w", err)
		}

		cfg.NodePrivateKey = sk
		cfg.NodePubkey = pubkey

		// Generate npub for display
		npub, err := nip19.EncodePublicKey(pubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to encode npub: %w", err)
		}
		cfg.NodeNpub = npub

		// Save configuration
		if err := cfg.Save(configPath); err != nil {
			return nil, fmt.Errorf("failed to save configuration: %w", err)
		}

		fmt.Printf("Created new node configuration at %s\n", configPath)
		fmt.Printf("Node npub: %s\n", cfg.NodeNpub)
	} else {
		// Load existing configuration
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read configuration: %w", err)
		}

		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse configuration: %w", err)
		}

		// Override with command line flags if provided
		if port != 8080 {
			cfg.Port = port
		}
		if dataDir != "./data" {
			cfg.DataDir = dataDir
		}
	}

	return cfg, nil
}

// Save saves the configuration to a file
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal configuration: %w", err)
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write configuration: %w", err)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// Validate validates the configuration and logs warnings
func (c *Config) Validate() {
	// Validate network
	validNetworks := map[string]bool{"mainnet": true, "testnet": true, "dev": true}
	if !validNetworks[c.Network] {
		log.Printf("⚠️  Invalid network '%s', defaulting to 'testnet'", c.Network)
		c.Network = "testnet"
	}

	// Validate port
	if c.Port < 1 || c.Port > 65535 {
		log.Printf("⚠️  Invalid port %d, defaulting to 8080", c.Port)
		c.Port = 8080
	}

	// Validate relay URLs
	if len(c.RelayURLs) == 0 {
		log.Println("⚠️  No relay URLs configured. DNN events may not be discoverable.")
	}

	// Log DNS status
	if c.DNS.Enabled {
		log.Printf("✓ DNS server will start on port %d", c.DNS.Port)
		if c.DNS.Port == 53 {
			log.Println("  Note: Port 53 requires administrator/root privileges")
		}
	} else {
		log.Println("ℹ️  DNS server is disabled. DNN names can still be resolved via HTTP API at /dnn/resolve/{name}")
	}
}
