package bitcoin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client represents a Bitcoin RPC client
type Client struct {
	url      string
	user     string
	password string
	client   *http.Client
}

// Block represents a Bitcoin block
type Block struct {
	Hash         string        `json:"hash"`
	Height       int64         `json:"height"`
	Time         int64         `json:"time"`
	Transactions []Transaction `json:"tx"`
}

// Transaction represents a Bitcoin transaction
type Transaction struct {
	TxID        string     `json:"txid"`
	Inputs      []TxInput  `json:"vin"`
	Outputs     []TxOutput `json:"vout"`
	Fee         float64    `json:"fee,omitempty"`
	Size        int        `json:"size,omitempty"`   // Transaction size in bytes
	Weight      int        `json:"weight,omitempty"` // Transaction weight units
	VSize       int        `json:"vsize,omitempty"`  // Virtual size in vBytes
	BlockHeight int64      `json:"block_height,omitempty"`
	BlockHash   string     `json:"block_hash,omitempty"`
	Confirmed   bool       `json:"confirmed,omitempty"`
}

// TxInput represents a transaction input
type TxInput struct {
	TxID      string `json:"txid"`
	Vout      uint32 `json:"vout"`
	ScriptSig Script `json:"scriptSig"`
	Sequence  uint32 `json:"sequence"`
	Address   string `json:"address,omitempty"` // Input address (from prevout)
	Value     int64  `json:"value,omitempty"`   // Input value in satoshis (from prevout)
}

// TxOutput represents a transaction output
type TxOutput struct {
	Value        float64      `json:"value"`
	N            uint32       `json:"n"`
	ScriptPubKey ScriptPubKey `json:"scriptPubKey"`
}

// Script represents a script
type Script struct {
	Asm string `json:"asm"`
	Hex string `json:"hex"`
}

// ScriptPubKey represents a script public key
type ScriptPubKey struct {
	Asm       string   `json:"asm"`
	Hex       string   `json:"hex"`
	Type      string   `json:"type"`
	Addresses []string `json:"addresses,omitempty"`
	Address   string   `json:"address,omitempty"`
}

// NewClient creates a new Bitcoin RPC client
func NewClient(host string, port int, user, password string, useSSL bool) (*Client, error) {
	protocol := "http"
	if useSSL {
		protocol = "https"
	}

	url := fmt.Sprintf("%s://%s:%d", protocol, host, port)

	return &Client{
		url:      url,
		user:     user,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// rpcRequest represents a JSON-RPC request
type rpcRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// rpcResponse represents a JSON-RPC response
type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	ID     string          `json:"id"`
}

// rpcError represents a JSON-RPC error
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call makes an RPC call to the Bitcoin node
func (c *Client) call(method string, params ...interface{}) (json.RawMessage, error) {
	// Create request
	req := rpcRequest{
		Jsonrpc: "1.0",
		ID:      "godnn",
		Method:  method,
		Params:  params,
	}

	// Marshal request
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", c.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.SetBasicAuth(c.user, c.password)

	// Send request
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var rpcResp rpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for error
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// GetBlockCount returns the current block height
func (c *Client) GetBlockCount() (int64, error) {
	result, err := c.call("getblockcount")
	if err != nil {
		return 0, err
	}

	var count int64
	if err := json.Unmarshal(result, &count); err != nil {
		return 0, fmt.Errorf("failed to parse block count: %w", err)
	}

	return count, nil
}

// GetBlockHash returns the block hash for a given height
func (c *Client) GetBlockHash(height int64) (string, error) {
	result, err := c.call("getblockhash", height)
	if err != nil {
		return "", err
	}

	var hash string
	if err := json.Unmarshal(result, &hash); err != nil {
		return "", fmt.Errorf("failed to parse block hash: %w", err)
	}

	return hash, nil
}

// GetBlock returns block data
func (c *Client) GetBlock(hash string) (*Block, error) {
	// Get block with verbosity level 2 (includes transaction data)
	result, err := c.call("getblock", hash, 2)
	if err != nil {
		return nil, err
	}

	var block Block
	if err := json.Unmarshal(result, &block); err != nil {
		return nil, fmt.Errorf("failed to parse block: %w", err)
	}

	return &block, nil
}

// GetTransaction returns transaction data
func (c *Client) GetTransaction(txid string) (*Transaction, error) {
	result, err := c.call("getrawtransaction", txid, true)
	if err != nil {
		return nil, err
	}

	var tx Transaction
	if err := json.Unmarshal(result, &tx); err != nil {
		return nil, fmt.Errorf("failed to parse transaction: %w", err)
	}

	return &tx, nil
}

// ValidateDNNTransaction validates if a transaction meets DNN criteria
func (c *Client) ValidateDNNTransaction(tx *Transaction) (bool, error) {
	// Check if it's a self-transfer
	if !c.isSelfTransfer(tx) {
		return false, nil
	}

	// Check fee rate (minimum 1 sat/vB)
	// This is simplified - actual implementation would calculate vBytes
	if tx.Fee < 0.00000005 {
		return false, nil
	}

	return true, nil
}

// isSelfTransfer checks if a transaction is a self-transfer
func (c *Client) isSelfTransfer(tx *Transaction) bool {
	// Note: This is simplified. Full implementation would need to:
	// 1. Look up previous outputs for inputs
	// 2. Properly handle different script types
	// 3. Verify that sender = receiver

	// For now, check that there's exactly one unique address
	allAddrs := make(map[string]bool)
	for _, output := range tx.Outputs {
		if len(output.ScriptPubKey.Addresses) > 0 {
			for _, addr := range output.ScriptPubKey.Addresses {
				allAddrs[addr] = true
			}
		} else if output.ScriptPubKey.Address != "" {
			allAddrs[output.ScriptPubKey.Address] = true
		}
	}

	// Exactly one address means self-transfer
	return len(allAddrs) == 1
}
