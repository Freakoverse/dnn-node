# DNN Node - Decentralized Naming Network

A Go implementation of a DNN (Decentralized Naming Network) node that serves as both a Nostr relay and a Bitcoin-anchored naming system resolver.

![DNN Logo](https://image.nostr.build/915080fe93de0a6772c3301606894938c8d93e9db98bdf5e9ba4da1c7711ea60.jpg)

---

## 🚀 Quick Start

### Linux/Mac Users

```bash
# Initialize configuration
go run . --init

# Run the node
go run .

# Or build and run
go build -o dnn-node .
./dnn-node --init
./dnn-node
```

### Windows Users

```cmd
# Initialize and run (no build needed)
go run . --init
go run .
```

## 📊 Access Your Node

Once running, access your node at:

- **🌐 Web Dashboard**: http://localhost:8080
- **📡 API Status**: http://localhost:8080/dnn/status
- **🔌 WebSocket (Nostr)**: ws://localhost:8080

### Web Dashboard Features

The embedded web dashboard provides:
- Real-time Bitcoin and DNN block monitoring
- Nostr login (NIP-07 browser extension + NIP-46 remote signer)
- Complete DNN event publishing workflow
- Transaction status tracking
- Event management and editing
- **Awareness Database** — manage local marks and view peer consensus
- **Explorer** — browse and search the DNN namespace
- **Node Management** — peers, relays, and discovery

> **Note:** The dashboard is embedded in the Go binary — no separate frontend build required!

---

## 📁 Project Structure

```
node/
├── main.go                 # Entry point
├── internal/               # Core Go packages
│   ├── node/              # Main server + embedded dashboard
│   ├── sync/              # Bitcoin and peer sync
│   ├── database/          # SQLite database layer
│   ├── encoder/           # Name encoding/decoding (BIP-39 wordlist)
│   ├── bitcoin/           # Bitcoin P2P client
│   ├── validation/        # Event validation
│   ├── policy/            # DNN protocol policy
│   ├── config/            # Configuration management
│   ├── constants/         # Network constants (genesis blocks)
│   └── reorg/             # Blockchain reorganization handler
├── cmd/                   # Command-line tools
│   ├── dnn-cli/          # CLI tool
│   └── test-client/      # Test client
├── docs/                  # Documentation
├── scripts/               # Setup scripts
└── test/                  # Test files
```

---

## 🌐 What is DNN?

The **Decentralized Naming Network (DNN)** is a protocol that enables users to register human-readable names anchored to the Bitcoin blockchain. It implements the **NIP-DN** standard for Nostr-based decentralized names.

### Key Features

- **🔗 Bitcoin-Anchored**: Names secured by Bitcoin transactions
- **🌍 Decentralized**: No central authority controls the namespace
- **⚡ Nostr-Compatible**: Works seamlessly with the Nostr protocol
- **📝 Human-Readable**: BIP-39 wordlist encoding for memorable DNN IDs
- **🔒 Spam-Resistant**: Requires Bitcoin self-transfer for registration
- **🔄 Reorg Protection**: Automatic chain reorganization handling
- **🛡️ Awareness Database**: Peer-consensus content filtering (malware, phishing, adult)
- **🌐 DNS Integration**: Serves DNN names over standard DNS (port 53)

### Name Formats

DNN IDs are identified by block number and position. Multiple formats are supported:

- **Encoded** (primary): `nabandonzooa` — human-readable BIP-39 encoding
- **DNN Block**: `n50` or `n50.1` (block 50, position 1)
- **Bitcoin Block**: `b1000050` or `b1000050.1`
- **Shorthand**: `n5h` (h=hundred), `b1m50` (m=million)

Names are defined *under* a DNN ID: `alice.nabandonzooa` or `alice@nabandonzooa`

---

## 🛠️ Configuration

### Initial Setup

On first run with `--init`, the node creates `config.json` with:
- Unique node keypair (Nostr identity)
- Default relay connections
- Bitcoin P2P settings

### Bitcoin Connection

```json
{
  "bitcoin_rpc": {
    "use_p2p": true,
    "p2p_peers": [],
    "_comment": "P2P Mode (fully decentralized, no API dependencies)"
  }
}
```

P2P mode connects directly to the Bitcoin network — no rate limits, no third-party APIs, fully decentralized. Peers are auto-discovered.

### Configuration File Example

```json
{
  "node_private_key": "GENERATED_ON_INIT",
  "node_pubkey": "GENERATED_ON_INIT",
  "node_npub": "GENERATED_ON_INIT",
  "network": "testnet",
  "port": 8080,
  "data_dir": "./data",
  "relay_urls": [
    "wss://relay.damus.io",
    "wss://relay.nostr.band",
    "wss://nos.lol",
    "wss://relay.primal.net"
  ],
  "peer_nodes": [],
  "bitcoin_rpc": {
    "use_p2p": true,
    "p2p_peers": []
  },
  "dns": {
    "enabled": true,
    "port": 53,
    "upstream_dns": ["8.8.8.8:53", "1.1.1.1:53"]
  },
  "sync_interval": 60,
  "block_batch_size": 5,
  "max_connections": 1000,
  "enable_awareness": true,
  "admin_npub": ""
}
```

### Awareness Database

The node's awareness system allows operators to `block` specific TLDs or names. Category-based filtering (malware, phishing, scam, adult) is handled **client-side** by DNN-aware browsers using aggregated peer consensus data returned by the node.

---

## 📖 Documentation

### Core Documentation
- **[DNN Overview](../docs/DNN_OVERVIEW.md)** - Comprehensive DNN protocol explanation
- **[NIP-DN Specification](../docs/NIP-DN.md)** - Technical specification
- **[Node Policy](../docs/node_policy.md)** - DNN node rules and requirements

### Deployment & Operations
- **[Deployment Guide](../docs/DEPLOYMENT.md)** - Production deployment instructions

---

## 🧪 Testing

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test ./internal/encoder/
go test ./internal/validation/
```

---

## 📝 DNN Protocol Summary

### Networks
- **Mainnet**: Genesis at Bitcoin block 1,000,000
- **Testnet**: Genesis at Bitcoin block 932,300
- **Dev**: Genesis at Bitcoin block 900,000

### Registration Requirements
- Self-transfer Bitcoin transaction (sender = receiver, P2WPKH only)
- Minimum fee rate: 1 sat/vB
- Four Nostr events: `kind:60600` (anchor), `kind:61600` (name), `kind:62600` (connection), `kind:63600` (metadata)
- Valid event signatures and DNN tags

### Event Size Limits
- Kind 60600 (Anchor): 10 KB
- Kind 61600 (Name): 10 KB
- Kind 62600 (Connection): 10 KB
- Kind 63600 (Metadata): 50 KB

### DNN Event Kinds
| Kind | Purpose |
|------|---------|
| 60600 | Anchor — links Bitcoin TX to Nostr events |
| 61600 | Name — declares the name(s) |
| 62600 | Connection — DNS records, IP addresses, certs |
| 63600 | Metadata — display name, avatar, bio |
| 64600 | Node Discovery — peer node announcements |

---

## 🔒 Security

- **🔑 Private Key**: Keep your `node_private_key` in `config.json` secure
- **👤 Admin Npub**: Set `admin_npub` to a separate key from the node key for awareness management
- **💾 Backups**: Regularly backup `config.json` and `data/` directory
- **🔥 Firewall**: Only expose necessary ports (8080 for HTTP/WebSocket, 53 for DNS)
- **🔐 SSL/TLS**: Use reverse proxy with HTTPS in production

---

## 🤝 Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Submit a pull request

---

## 🆘 Support

- **Nostr**: Join the discussion on Nostr
- **Documentation**: Check the [docs/](../docs/) folder

---

**Built with ❤️ for the Decentralized Web**