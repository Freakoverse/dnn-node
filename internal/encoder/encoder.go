package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"dnn-node/internal/constants"
)

// Re-export encoder constants
const (
	DefaultBlocksPerBatch = constants.DefaultBlocksPerBatch
	DefaultTxMax          = constants.DefaultTxMax
)

// Encoder handles DNN name encoding and decoding
// V2 format: n + word1 + word2 + [cycle] + positionLetters
// Example: nwinterzooa (block X, position 1)
type Encoder struct {
	blocksPerBatch int
	txMax          int
	genesisBlock   int64 // Dynamically set based on network mode
	wordlist       []string
	prefixes       []string // kept for legacy reference
	bip39Base      int
	cycleSize      int // bip39Base * bip39Base
}

// NewEncoder creates a new encoder with testnet defaults
func NewEncoder() *Encoder {
	return NewEncoderWithNetwork(constants.NetworkTestnet)
}

// NewEncoderWithNetwork creates an encoder for a specific network
func NewEncoderWithNetwork(networkMode string) *Encoder {
	genesisBlock := constants.GetGenesisBlock(networkMode)
	return NewEncoderWithParams(DefaultBlocksPerBatch, DefaultTxMax, genesisBlock)
}

// NewEncoderWithParams creates an encoder with custom parameters
func NewEncoderWithParams(blocksPerBatch, txMax int, genesisBlock int64) *Encoder {
	prefixes := generatePrefixes()
	wordlist := BIP39EnglishWordlist
	bip39Base := len(wordlist)

	return &Encoder{
		blocksPerBatch: blocksPerBatch,
		txMax:          txMax,
		genesisBlock:   genesisBlock,
		wordlist:       wordlist,
		prefixes:       prefixes,
		bip39Base:      bip39Base,
		cycleSize:      bip39Base * bip39Base,
	}
}

// GetGenesisBlock returns the genesis block for this encoder
func (e *Encoder) GetGenesisBlock() int64 {
	return e.genesisBlock
}

// generatePrefixes generates all 3-letter permutations (excluding same letters)
// Kept for potential future use
func generatePrefixes() []string {
	var prefixes []string
	alph := "abcdefghijklmnopqrstuvwxyz"

	for i := 0; i < len(alph); i++ {
		for j := 0; j < len(alph); j++ {
			if j == i {
				continue
			}
			for k := 0; k < len(alph); k++ {
				if k == i || k == j {
					continue
				}
				prefixes = append(prefixes, string([]byte{alph[i], alph[j], alph[k]}))
			}
		}
	}
	return prefixes
}

// Encode encodes a block number and transaction position into a human-readable string.
// V2 format: n{word1}{word2}[{cycle}]{posLetters}
// - word1 and word2 encode the block number within the cycle (base 1999)
// - cycle is omitted for the first ~76 years (cycle 0), then 1, 2, etc.
// - posLetters encodes the tx position in bijective base-26 (1=a, 26=z, 27=aa)
//
// Examples:
//
//	nwinterzooa       → block X, position 1
//	nwinterzookd      → block X, position Y
//	nwinterzoo1a      → block X (in cycle 1, ~76+ years), position 1
func (e *Encoder) Encode(blockNumber int64, transactionPosition int) (string, error) {
	if blockNumber < 0 {
		return "", fmt.Errorf("block number cannot be negative")
	}
	if transactionPosition < 1 {
		return "", fmt.Errorf("transaction position must be >= 1")
	}

	// Calculate cycle and block within cycle
	cycle := int(blockNumber) / e.cycleSize
	blockInCycle := int(blockNumber) % e.cycleSize

	// Encode block as two BIP39 words
	word1Index := blockInCycle / e.bip39Base
	word2Index := blockInCycle % e.bip39Base

	word1 := e.wordlist[word1Index]
	word2 := e.wordlist[word2Index]

	// Encode transaction position as bijective base-26 letters
	posStr := encodePosition(transactionPosition)

	// Build result
	if cycle > 0 {
		return fmt.Sprintf("n%s%s%d%s", word1, word2, cycle, posStr), nil
	}
	return fmt.Sprintf("n%s%s%s", word1, word2, posStr), nil
}

// encodePosition encodes a position (1-indexed) into bijective base-26 letters.
// 1→a, 2→b, ..., 26→z, 27→aa, 28→ab, ..., 702→zz, 703→aaa
func encodePosition(pos int) string {
	if pos < 1 {
		pos = 1
	}
	result := ""
	n := pos
	for n > 0 {
		n-- // convert to 0-indexed for this digit
		result = string(rune('a'+n%26)) + result
		n /= 26
	}
	return result
}

// decodePosition decodes bijective base-26 letters back to a position number.
// a→1, b→2, ..., z→26, aa→27, ab→28, ..., zz→702, aaa→703
func decodePosition(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty position string")
	}
	result := 0
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return 0, fmt.Errorf("invalid position character: %c", c)
		}
		result = result*26 + int(c-'a') + 1
	}
	return result, nil
}

// Decode decodes a V2 encoded string back to block number and transaction position.
// Input format: n{word1}{word2}[{cycle}]{posLetters}
func (e *Encoder) Decode(codeStr string) (int64, int, error) {
	if len(codeStr) < 2 || codeStr[0] != 'n' {
		return 0, 0, fmt.Errorf("invalid code: must start with 'n'")
	}

	// Strip 'n' prefix
	rest := codeStr[1:]

	// Match first BIP39 word (greedy longest match)
	word1, rest, err := e.matchBIP39Word(rest)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to match first word: %w", err)
	}

	// Match second BIP39 word (greedy longest match)
	word2, rest, err := e.matchBIP39Word(rest)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to match second word: %w", err)
	}

	// Parse optional cycle digits
	digitCount := 0
	for digitCount < len(rest) && rest[digitCount] >= '0' && rest[digitCount] <= '9' {
		digitCount++
	}

	cycle := 0
	if digitCount > 0 {
		cycle, err = strconv.Atoi(rest[:digitCount])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid cycle number: %w", err)
		}
		rest = rest[digitCount:]
	}

	// Remaining is position letters
	if rest == "" {
		return 0, 0, fmt.Errorf("no position suffix found")
	}

	position, err := decodePosition(rest)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid position: %w", err)
	}

	// Find word indices
	word1Index := e.findWordIndex(word1)
	word2Index := e.findWordIndex(word2)
	if word1Index == -1 {
		return 0, 0, fmt.Errorf("word '%s' not found in wordlist", word1)
	}
	if word2Index == -1 {
		return 0, 0, fmt.Errorf("word '%s' not found in wordlist", word2)
	}

	// Calculate block number
	blockInCycle := word1Index*e.bip39Base + word2Index
	blockNumber := int64(cycle)*int64(e.cycleSize) + int64(blockInCycle)

	return blockNumber, position, nil
}

// matchBIP39Word finds the longest BIP39 word that matches at the start of the string.
func (e *Encoder) matchBIP39Word(s string) (string, string, error) {
	if s == "" {
		return "", "", fmt.Errorf("empty string")
	}

	bestMatch := ""
	for _, w := range e.wordlist {
		if strings.HasPrefix(s, w) && len(w) > len(bestMatch) {
			bestMatch = w
		}
	}

	if bestMatch == "" {
		return "", "", fmt.Errorf("no BIP39 word found at start of '%s'", s)
	}

	return bestMatch, s[len(bestMatch):], nil
}

// findWordIndex returns the index of a word in the wordlist, or -1 if not found.
func (e *Encoder) findWordIndex(word string) int {
	for i, w := range e.wordlist {
		if w == word {
			return i
		}
	}
	return -1
}

// ParseNotation parses various DNN name notations
func (e *Encoder) ParseNotation(notation string) (name string, blockNum int64, position int, err error) {
	parts := strings.Split(notation, ".")
	if len(parts) < 2 {
		return "", 0, 0, fmt.Errorf("invalid notation: %s", notation)
	}

	name = parts[0]

	// Check for different notation types
	remaining := strings.Join(parts[1:], ".")

	// Check for V2 encoded format (starts with 'n' followed by BIP39 words)
	if len(remaining) > 1 && remaining[0] == 'n' {
		// Try V2 decode first
		block, pos, decErr := e.Decode(remaining)
		if decErr == nil {
			return name, block, pos, nil
		}

		// Fall back to block notation (n{number})
		return e.parseBlockNotation(name, remaining)
	}

	// Check for b{block} format
	if strings.HasPrefix(remaining, "b") {
		return e.parseBlockNotation(name, remaining)
	}

	return "", 0, 0, fmt.Errorf("unrecognized notation format: %s", notation)
}

// parseBlockNotation parses n{block}[.{position}] or b{block}[.{position}] notation
func (e *Encoder) parseBlockNotation(name, notation string) (string, int64, int, error) {
	prefix := notation[0:1]
	rest := notation[1:]

	// Split by dot to get block and optional position
	parts := strings.Split(rest, ".")
	blockStr := parts[0]
	position := 1 // default position

	if len(parts) > 1 {
		pos, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid position: %s", parts[1])
		}
		position = pos
	}

	// Expand shorthand notation
	blockNum := e.expandShorthand(blockStr)

	// Convert Bitcoin block to DNN block if needed
	if prefix == "b" {
		blockNum = blockNum - e.genesisBlock
		if blockNum < 0 {
			return "", 0, 0, fmt.Errorf("Bitcoin block %d is before DNN genesis (%d)", blockNum+e.genesisBlock, e.genesisBlock)
		}
	}

	return name, blockNum, position, nil
}

// expandShorthand expands shorthand notation (h, k, m, b, t, etc.)
func (e *Encoder) expandShorthand(s string) int64 {
	multipliers := map[string]int64{
		"h":  100,
		"k":  1000,
		"m":  1000000,
		"b":  1000000000,
		"t":  1000000000000,
		"qd": 1000000000000000,
		"qt": 1000000000000000000,
	}

	// Check for special large multipliers first
	if strings.HasSuffix(s, "sp") {
		return 9223372036854775807 // math.MaxInt64
	}

	if strings.HasSuffix(s, "o") {
		numStr := strings.TrimSuffix(s, "o")
		if numStr == "" {
			numStr = "1"
		}
		return 9223372036854775807 // math.MaxInt64
	}

	// Check for regular multiplier suffixes
	for suffix, mult := range multipliers {
		if strings.HasSuffix(s, suffix) {
			numStr := strings.TrimSuffix(s, suffix)
			if numStr == "" {
				numStr = "1"
			}
			num, err := strconv.ParseInt(numStr, 10, 64)
			if err != nil {
				return 0
			}
			return num * mult
		}
	}

	// No suffix, parse as regular number
	num, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return num
}

// FormatDNNBlock formats a DNN block number with optional position
func (e *Encoder) FormatDNNBlock(blockNum int64, position int) string {
	if position == 1 {
		return fmt.Sprintf("n%d", blockNum)
	}
	return fmt.Sprintf("n%d.%d", blockNum, position)
}

// FormatBitcoinBlock formats a Bitcoin block number with optional position
func (e *Encoder) FormatBitcoinBlock(blockNum int64, position int) string {
	bitcoinBlock := blockNum + e.genesisBlock

	if position == 1 {
		return fmt.Sprintf("b%d", bitcoinBlock)
	}
	return fmt.Sprintf("b%d.%d", bitcoinBlock, position)
}
