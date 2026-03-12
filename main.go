package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"dnn-node/internal/config"
	"dnn-node/internal/database"
	"dnn-node/internal/dns"
	"dnn-node/internal/node"
)

func main() {
	var (
		configPath = flag.String("config", "config.json", "Path to configuration file")
		port       = flag.Int("port", 8080, "Port to run the DNN node on")
		dataDir    = flag.String("data", "./data", "Directory for storing node data")
		initMode   = flag.Bool("init", false, "Initialize a new node configuration")
	)
	flag.Parse()

	// Load or create configuration
	cfg, err := config.Load(*configPath, *port, *dataDir, *initMode)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Validate configuration and log warnings
	cfg.Validate()

	// If init mode, exit after creating configuration
	if *initMode {
		fmt.Println("Initialization complete. Run without --init to start the node.")
		return
	}

	// Initialize database
	db, err := database.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Create and start the DNN node
	dnnNode, err := node.New(cfg, db)
	if err != nil {
		log.Fatalf("Failed to create DNN node: %v", err)
	}

	// Start the node
	if err := dnnNode.Start(); err != nil {
		log.Fatalf("Failed to start DNN node: %v", err)
	}

	fmt.Printf("DNN Node started on port %d\n", cfg.Port)
	fmt.Printf("Data directory: %s\n", cfg.DataDir)
	fmt.Printf("Node pubkey: %s\n", cfg.NodePubkey)

	// Start DNS server if enabled
	var dnsServer *dns.Server
	if cfg.DNS.Enabled {
		dnsServer, err = dns.NewServer(cfg, db)
		if err != nil {
			log.Printf("Warning: Failed to create DNS server: %v", err)
		} else {
			if dnsServer.Start() {
				fmt.Printf("DNS Server started on port %d\n", cfg.DNS.Port)
			}
			// If Start returns false, it already logged the warning
		}
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
	if dnsServer != nil {
		if err := dnsServer.Stop(); err != nil {
			log.Printf("Error stopping DNS server: %v", err)
		}
	}
	if err := dnnNode.Stop(); err != nil {
		log.Printf("Error stopping node: %v", err)
	}
}
