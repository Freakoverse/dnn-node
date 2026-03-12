package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type CLI struct {
	nodeURL string
	client  *http.Client
}

func NewCLI(nodeURL string) *CLI {
	if !strings.HasPrefix(nodeURL, "http://") && !strings.HasPrefix(nodeURL, "https://") {
		nodeURL = "http://" + nodeURL
	}
	
	return &CLI{
		nodeURL: nodeURL,
		client:  &http.Client{},
	}
}

func (c *CLI) Resolve(name string) error {
	url := fmt.Sprintf("%s/dnn/resolve/%s", c.nodeURL, name)
	
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to resolve: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("resolution failed: %s", body)
	}
	
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	
	// Pretty print the result
	output, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(output))
	
	return nil
}

func (c *CLI) Status() error {
	url := fmt.Sprintf("%s/dnn/status", c.nodeURL)
	
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	
	var status map[string]interface{}
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	
	fmt.Println("DNN Node Status")
	fmt.Println("===============")
	fmt.Printf("Node Pubkey: %s\n", status["node_pubkey"])
	fmt.Printf("Node Npub: %s\n", status["node_npub"])
	fmt.Printf("Connected Peers: %.0f\n", status["connected_peers"])
	fmt.Printf("Syncing: %v\n", status["syncing"])
	fmt.Printf("Version: %s\n", status["dnn_version"])
	fmt.Printf("Uptime: %.0f seconds\n", status["uptime"])
	
	return nil
}

func (c *CLI) Peers() error {
	url := fmt.Sprintf("%s/dnn/peers", c.nodeURL)
	
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to get peers: %w", err)
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	
	var peers []map[string]interface{}
	if err := json.Unmarshal(body, &peers); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	
	fmt.Println("Connected Peers")
	fmt.Println("===============")
	
	if len(peers) == 0 {
		fmt.Println("No connected peers")
		return nil
	}
	
	for i, peer := range peers {
		fmt.Printf("\nPeer #%d:\n", i+1)
		fmt.Printf("  Pubkey: %s\n", peer["pubkey"])
		fmt.Printf("  Relay URL: %s\n", peer["relay_url"])
		fmt.Printf("  Last Seen: %s\n", peer["last_seen"])
		fmt.Printf("  Active: %v\n", peer["is_active"])
	}
	
	return nil
}

func (c *CLI) Health() error {
	url := fmt.Sprintf("%s/health", c.nodeURL)
	
	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("node is not responding: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode == http.StatusOK {
		fmt.Println("✓ Node is healthy")
	} else {
		fmt.Println("✗ Node is unhealthy")
		return fmt.Errorf("health check failed with status %d", resp.StatusCode)
	}
	
	return nil
}

func main() {
	var (
		nodeURL = flag.String("node", "localhost:8080", "DNN node URL")
		resolve = flag.String("resolve", "", "Resolve a DNN name")
		status  = flag.Bool("status", false, "Get node status")
		peers   = flag.Bool("peers", false, "List connected peers")
		health  = flag.Bool("health", false, "Check node health")
	)
	
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: dnn-cli [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  dnn-cli -resolve alice\n")
		fmt.Fprintf(os.Stderr, "  dnn-cli -resolve alice.n50.3\n")
		fmt.Fprintf(os.Stderr, "  dnn-cli -status\n")
		fmt.Fprintf(os.Stderr, "  dnn-cli -peers\n")
		fmt.Fprintf(os.Stderr, "  dnn-cli -health\n")
	}
	
	flag.Parse()
	
	cli := NewCLI(*nodeURL)
	
	// Execute requested command
	if *resolve != "" {
		if err := cli.Resolve(*resolve); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else if *status {
		if err := cli.Status(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else if *peers {
		if err := cli.Peers(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else if *health {
		if err := cli.Health(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		flag.Usage()
		os.Exit(1)
	}
}