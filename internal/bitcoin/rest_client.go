package bitcoin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RestClient provides Bitcoin data via REST APIs (like Blockstream, Mempool.space)
type RestClient struct {
	baseURL string
	client  *http.Client
}

// NewRestClient creates a new REST API client
func NewRestClient(baseURL string) *RestClient {
	return &RestClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BlockstreamClient creates a client for Blockstream.info API
func NewBlockstreamClient() *RestClient {
	return NewRestClient("https://blockstream.info/api")
}

// MempoolSpaceClient creates a client for Mempool.space API
func NewMempoolSpaceClient() *RestClient {
	return NewRestClient("https://mempool.space/api")
}

// GetBlockCount returns the current block height
func (rc *RestClient) GetBlockCount() (int64, error) {
	url := rc.baseURL + "/blocks/tip/height"

	resp, err := rc.client.Get(url)
	if err != nil {
		return 0, fmt.Errorf("failed to get block height: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	var height int64
	if err := json.Unmarshal(body, &height); err != nil {
		return 0, fmt.Errorf("failed to parse height: %w", err)
	}

	return height, nil
}

// GetBlockHash returns the block hash for a given height
func (rc *RestClient) GetBlockHash(height int64) (string, error) {
	url := fmt.Sprintf("%s/block-height/%d", rc.baseURL, height)

	resp, err := rc.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to get block hash: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Response is just the block hash as a string
	hash := string(body)
	return hash, nil
}

// GetBlock returns block data
func (rc *RestClient) GetBlock(hash string) (*Block, error) {
	// First, get block metadata
	url := fmt.Sprintf("%s/block/%s", rc.baseURL, hash)

	resp, err := rc.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var blockData struct {
		ID            string   `json:"id"`
		Height        int64    `json:"height"`
		Timestamp     int64    `json:"timestamp"`
		TxCount       int      `json:"tx_count"`
		Size          int      `json:"size"`
		Weight        int      `json:"weight"`
		MerkleRoot    string   `json:"merkle_root"`
		PreviousHash  string   `json:"previousblockhash"`
		Nonce         int64    `json:"nonce"`
		Bits          int64    `json:"bits"`
		Difficulty    float64  `json:"difficulty"`
	}

	if err := json.Unmarshal(body, &blockData); err != nil {
		return nil, fmt.Errorf("failed to parse block: %w", err)
	}

	// Get transaction IDs separately (Blockstream requires a separate call)
	txIDsURL := fmt.Sprintf("%s/block/%s/txids", rc.baseURL, hash)
	txIDsResp, err := rc.client.Get(txIDsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction IDs: %w", err)
	}
	defer txIDsResp.Body.Close()

	if txIDsResp.StatusCode != 200 {
		body, _ := io.ReadAll(txIDsResp.Body)
		return nil, fmt.Errorf("API error getting txids (status %d): %s", txIDsResp.StatusCode, string(body))
	}

	txIDsBody, err := io.ReadAll(txIDsResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read txids response: %w", err)
	}

	var txIDs []string
	if err := json.Unmarshal(txIDsBody, &txIDs); err != nil {
		return nil, fmt.Errorf("failed to parse transaction IDs: %w", err)
	}

	fmt.Printf("Block %s has %d transactions (tx_count: %d)\n", hash, len(txIDs), blockData.TxCount)

	// For large blocks, we need to be selective about which transactions to fetch
	// Strategy: Scan ALL transaction IDs first (cheap), then only fetch the ones we need
	transactions := make([]Transaction, 0)

	// We'll fetch transactions in chunks, checking each one
	// This is more efficient than fetching all transactions blindly
	maxTxsToFetch := 500 // Check up to 500 transactions per block
	txsToFetch := txIDs
	if len(txIDs) > maxTxsToFetch {
		fmt.Printf("Block has %d transactions, will check first %d\n", len(txIDs), maxTxsToFetch)
		txsToFetch = txIDs[:maxTxsToFetch]
	}

	for i, txID := range txsToFetch {
		// Add delay to avoid rate limiting
		if i > 0 && i%10 == 0 {
			time.Sleep(500 * time.Millisecond)
		}

		tx, err := rc.GetTransaction(txID)
		if err != nil {
			fmt.Printf("Failed to get transaction %s: %v\n", txID, err)
			continue
		}
		transactions = append(transactions, *tx)
	}

	block := &Block{
		Hash:         blockData.ID,
		Height:       blockData.Height,
		Time:         blockData.Timestamp,
		Transactions: transactions,
	}

	return block, nil
}

// GetBlockTransactionPosition finds a transaction's position in a block
func (rc *RestClient) GetBlockTransactionPosition(blockHash, txID string) (int, error) {
	// Get all transaction IDs from the block
	txIDsURL := fmt.Sprintf("%s/block/%s/txids", rc.baseURL, blockHash)
	resp, err := rc.client.Get(txIDsURL)
	if err != nil {
		return -1, fmt.Errorf("failed to get transaction IDs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return -1, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, fmt.Errorf("failed to read response: %w", err)
	}

	var txIDs []string
	if err := json.Unmarshal(body, &txIDs); err != nil {
		return -1, fmt.Errorf("failed to parse transaction IDs: %w", err)
	}

	// Find the transaction in the list
	for i, id := range txIDs {
		if id == txID {
			return i, nil
		}
	}

	return -1, fmt.Errorf("transaction not found in block")
}

// GetTransaction returns transaction data
func (rc *RestClient) GetTransaction(txid string) (*Transaction, error) {
	url := fmt.Sprintf("%s/tx/%s", rc.baseURL, txid)

	resp, err := rc.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var txData struct {
		TxID   string `json:"txid"`
		Size   int    `json:"size"`
		Weight int    `json:"weight"`
		Fee    int64  `json:"fee"`
		Status struct {
			Confirmed   bool   `json:"confirmed"`
			BlockHeight int64  `json:"block_height"`
			BlockHash   string `json:"block_hash"`
			BlockTime   int64  `json:"block_time"`
		} `json:"status"`
		Vin    []struct {
			TxID    string `json:"txid"`
			Vout    uint32 `json:"vout"`
			Prevout struct {
				ScriptPubKeyAddress string `json:"scriptpubkey_address"`
				Value               int64  `json:"value"`
			} `json:"prevout"`
		} `json:"vin"`
		Vout []struct {
			ScriptPubKey        string `json:"scriptpubkey"`
			ScriptPubKeyAsm     string `json:"scriptpubkey_asm"`
			ScriptPubKeyType    string `json:"scriptpubkey_type"`
			ScriptPubKeyAddress string `json:"scriptpubkey_address"`
			Value               int64  `json:"value"`
		} `json:"vout"`
	}

	if err := json.Unmarshal(body, &txData); err != nil {
		return nil, fmt.Errorf("failed to parse transaction: %w", err)
	}

	// Convert to our Transaction format
	tx := &Transaction{
		TxID:        txData.TxID,
		Fee:         float64(txData.Fee) / 100000000.0, // Convert satoshis to BTC
		BlockHeight: txData.Status.BlockHeight,
		BlockHash:   txData.Status.BlockHash,
		Confirmed:   txData.Status.Confirmed,
	}

	// Extract input addresses from prevout data
	for _, vin := range txData.Vin {
		tx.Inputs = append(tx.Inputs, TxInput{
			TxID:    vin.TxID,
			Vout:    vin.Vout,
			Address: vin.Prevout.ScriptPubKeyAddress,
			Value:   vin.Prevout.Value,
		})
	}

	// Extract output addresses and build scriptpubkey structures
	for i, vout := range txData.Vout {
		output := TxOutput{
			Value: float64(vout.Value) / 100000000.0, // Convert satoshis to BTC
			N:     uint32(i),
			ScriptPubKey: ScriptPubKey{
				Asm:     vout.ScriptPubKeyAsm,
				Type:    vout.ScriptPubKeyType,
				Address: vout.ScriptPubKeyAddress,
			},
		}

		if vout.ScriptPubKeyAddress != "" {
			output.ScriptPubKey.Addresses = []string{vout.ScriptPubKeyAddress}
		}

		tx.Outputs = append(tx.Outputs, output)
	}

	return tx, nil
}

// ValidateDNNTransaction validates if a transaction meets DNN criteria
// Returns: isSelfTransfer, inputAddresses, outputAddresses, error
func (rc *RestClient) ValidateDNNTransaction(tx *Transaction) (bool, []string, []string, error) {
	// Collect all unique addresses
	inputAddrs := make(map[string]bool)
	outputAddrs := make(map[string]bool)

	// Get input addresses (from previous outputs)
	for _, output := range tx.Outputs {
		if len(output.ScriptPubKey.Addresses) > 0 {
			for _, addr := range output.ScriptPubKey.Addresses {
				outputAddrs[addr] = true
			}
		} else if output.ScriptPubKey.Address != "" {
			outputAddrs[output.ScriptPubKey.Address] = true
		}
	}

	// For self-transfer, input and output addresses must be identical
	// And there must be exactly one unique address

	allAddrs := make(map[string]bool)
	for addr := range inputAddrs {
		allAddrs[addr] = true
	}
	for addr := range outputAddrs {
		allAddrs[addr] = true
	}

	isSelfTransfer := len(allAddrs) == 1 && len(allAddrs) > 0

	inputList := make([]string, 0, len(inputAddrs))
	for addr := range inputAddrs {
		inputList = append(inputList, addr)
	}

	outputList := make([]string, 0, len(outputAddrs))
	for addr := range outputAddrs {
		outputList = append(outputList, addr)
	}

	return isSelfTransfer, inputList, outputList, nil
}