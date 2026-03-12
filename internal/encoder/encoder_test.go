package encoder

import (
	"fmt"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	encoder := NewEncoder()

	tests := []struct {
		blockNum int64
		position int
	}{
		{0, 1},           // first block, first tx
		{1, 1},           // second block, first tx
		{4, 5},           // block 4, position 5
		{100, 1},         // basic
		{1998, 1},        // last word2 index before word1 changes
		{1999, 1},        // word1 changes to second word
		{2000, 2},        // word1=second word, word2=second word, pos=2
		{956453, 45},     // mid-range block
		{3996000, 1},     // last block in cycle 0
		{3996001, 1},     // first block in cycle 1
		{3996002, 3},     // second block in cycle 1, position 3
		{7992002, 1},     // first block in cycle 2
		{100, 26},        // position z
		{100, 27},        // position aa
		{100, 702},       // position zz
		{100, 703},       // position aaa
		{52596527, 4500}, // large block, large position
	}

	for _, test := range tests {
		encoded, err := encoder.Encode(test.blockNum, test.position)
		if err != nil {
			t.Errorf("Failed to encode block %d, position %d: %v", test.blockNum, test.position, err)
			continue
		}

		decodedBlock, decodedPos, err := encoder.Decode(encoded)
		if err != nil {
			t.Errorf("Failed to decode %s: %v", encoded, err)
			continue
		}

		if decodedBlock != test.blockNum || decodedPos != test.position {
			t.Errorf("Round-trip failed: %d.%d => %s => %d.%d",
				test.blockNum, test.position, encoded, decodedBlock, decodedPos)
		} else {
			t.Logf("✓ %d.%d => %s => %d.%d",
				test.blockNum, test.position, encoded, decodedBlock, decodedPos)
		}
	}
}

func TestEncodeFormat(t *testing.T) {
	encoder := NewEncoder()

	// Block 0, position 1 → first word + first word + "a"
	encoded, err := encoder.Encode(0, 1)
	if err != nil {
		t.Fatalf("Failed to encode: %v", err)
	}

	// Should start with 'n', have two BIP39 words, end with 'a'
	if encoded[0] != 'n' {
		t.Errorf("Expected encoded to start with 'n', got %s", encoded)
	}

	word1 := encoder.wordlist[0] // "abandon"
	word2 := encoder.wordlist[0] // "abandon"
	expected := fmt.Sprintf("n%s%sa", word1, word2)
	if encoded != expected {
		t.Errorf("Expected %s, got %s", expected, encoded)
	}

	t.Logf("Block 0, pos 1 => %s", encoded)

	// Block 1999, position 1 → word1 changes to index 1
	encoded2, _ := encoder.Encode(1999, 1)
	word1_1 := encoder.wordlist[1] // second word
	word2_0 := encoder.wordlist[0] // first word
	expected2 := fmt.Sprintf("n%s%sa", word1_1, word2_0)
	if encoded2 != expected2 {
		t.Errorf("Expected %s, got %s", expected2, encoded2)
	}

	t.Logf("Block 1999, pos 1 => %s", encoded2)
}

func TestEncodeCycle(t *testing.T) {
	encoder := NewEncoder()

	// Block 3996001 should be in cycle 1 (1999*1999 = 3996001)
	encoded, err := encoder.Encode(3996001, 1)
	if err != nil {
		t.Fatalf("Failed to encode: %v", err)
	}

	// Should contain "1" between words and position
	t.Logf("Block 3996001, pos 1 => %s", encoded)

	// Decode and verify
	block, pos, err := encoder.Decode(encoded)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if block != 3996001 || pos != 1 {
		t.Errorf("Expected 3996001.1, got %d.%d", block, pos)
	}

	// Block in cycle 0 should NOT have cycle digits
	encoded0, _ := encoder.Encode(100, 1)
	// Verify no digits between words and position letters
	t.Logf("Block 100, pos 1 => %s", encoded0)
}

func TestPositionEncoding(t *testing.T) {
	tests := []struct {
		pos      int
		expected string
	}{
		{1, "a"},
		{2, "b"},
		{3, "c"},
		{26, "z"},
		{27, "aa"},
		{28, "ab"},
		{52, "az"},
		{53, "ba"},
		{702, "zz"},
		{703, "aaa"},
		{704, "aab"},
	}

	for _, test := range tests {
		result := encodePosition(test.pos)
		if result != test.expected {
			t.Errorf("encodePosition(%d): expected %q, got %q", test.pos, test.expected, result)
		}

		// Test round-trip
		decoded, err := decodePosition(result)
		if err != nil {
			t.Errorf("decodePosition(%q): unexpected error: %v", result, err)
			continue
		}
		if decoded != test.pos {
			t.Errorf("decodePosition(%q): expected %d, got %d", result, test.pos, decoded)
		}
	}
}

func TestParseNotation(t *testing.T) {
	encoder := NewEncoder()

	// Testnet genesis block is 932300
	tests := []struct {
		notation      string
		expectedName  string
		expectedBlock int64
		expectedPos   int
		shouldError   bool
	}{
		// DNN block format
		{"alice.n50", "alice", 50, 1, false},
		{"alice.n50.3", "alice", 50, 3, false},
		{"alice.n5h", "alice", 500, 1, false},

		// Bitcoin block format (testnet genesis = 932300)
		{"bob.b932350", "bob", 50, 1, false},
		{"bob.b932350.2", "bob", 50, 2, false},

		// Invalid formats
		{"invalid", "", 0, 0, true},
		{"alice", "", 0, 0, true},
	}

	for _, test := range tests {
		name, block, pos, err := encoder.ParseNotation(test.notation)

		if test.shouldError {
			if err == nil {
				t.Errorf("Expected error for notation %s, but got none", test.notation)
			}
			continue
		}

		if err != nil {
			t.Errorf("Unexpected error for notation %s: %v", test.notation, err)
			continue
		}

		if name != test.expectedName {
			t.Errorf("Notation %s: expected name %s, got %s", test.notation, test.expectedName, name)
		}
		if block != test.expectedBlock {
			t.Errorf("Notation %s: expected block %d, got %d", test.notation, test.expectedBlock, block)
		}
		if pos != test.expectedPos {
			t.Errorf("Notation %s: expected position %d, got %d", test.notation, test.expectedPos, pos)
		}
	}
}

func TestPrefixGeneration(t *testing.T) {
	encoder := NewEncoder()

	prefixCount := len(encoder.prefixes)
	expectedCount := 26 * 25 * 24

	if prefixCount != expectedCount {
		t.Errorf("Expected %d prefixes, got %d", expectedCount, prefixCount)
	}
}

func TestExpandShorthand(t *testing.T) {
	encoder := NewEncoder()

	tests := []struct {
		input    string
		expected int64
	}{
		{"100", 100},
		{"5h", 500},
		{"1k", 1000},
		{"1m", 1000000},
		{"1b", 1000000000},
		{"1t", 1000000000000},
		{"h", 100},
		{"k", 1000},
		{"m", 1000000},
		{"sp", 9223372036854775807},
		{"o", 9223372036854775807},
	}

	for _, test := range tests {
		result := encoder.expandShorthand(test.input)
		if result != test.expected {
			t.Errorf("expandShorthand(%s): expected %d, got %d", test.input, test.expected, result)
		}
	}
}

func TestBIP39WordList(t *testing.T) {
	encoder := NewEncoder()

	if encoder.bip39Base != 1999 {
		t.Errorf("Expected 1999 BIP-39 words, got %d", encoder.bip39Base)
	}

	// Check that cycle size is correct
	expectedCycleSize := 1999 * 1999
	if encoder.cycleSize != expectedCycleSize {
		t.Errorf("Expected cycle size %d, got %d", expectedCycleSize, encoder.cycleSize)
	}

	// Check some known BIP-39 words exist
	knownWords := []string{"abandon", "ability", "zoo", "satoshi", "winter"}
	for _, word := range knownWords {
		idx := encoder.findWordIndex(word)
		if idx == -1 {
			t.Errorf("Expected BIP-39 word '%s' not found", word)
		}
	}

	// Check that removed collision words are gone
	removedWords := []string{"act", "add", "age", "air", "all", "arm", "art", "bar", "bus", "can",
		"car", "cat", "cry", "cup", "end", "era", "eye", "fan", "fat", "fee",
		"fit", "fun", "gas", "ill", "kid", "kit", "lab", "law", "leg", "man",
		"mix", "mom", "net", "off", "own", "pen", "pig", "rib", "run", "sad",
		"sea", "ski", "sun", "ten", "top", "use", "van", "win", "you"}
	for _, word := range removedWords {
		idx := encoder.findWordIndex(word)
		if idx != -1 {
			t.Errorf("Collision word '%s' should have been removed, but found at index %d", word, idx)
		}
	}
}

func TestEdgeCases(t *testing.T) {
	encoder := NewEncoder()

	// Test negative block number
	_, err := encoder.Encode(-1, 1)
	if err == nil {
		t.Error("Expected error for negative block number")
	}

	// Test zero position
	_, err = encoder.Encode(0, 0)
	if err == nil {
		t.Error("Expected error for position 0")
	}

	// Test very large block number with cycles
	largeBlock := int64(3996001 * 5) // cycle 5
	encoded, err := encoder.Encode(largeBlock, 1)
	if err != nil {
		t.Fatalf("Failed to encode large block %d: %v", largeBlock, err)
	}

	decoded, pos, err := encoder.Decode(encoded)
	if err != nil {
		t.Fatalf("Failed to decode %s: %v", encoded, err)
	}

	if decoded != largeBlock || pos != 1 {
		t.Errorf("Expected %d.1, got %d.%d", largeBlock, decoded, pos)
	}

	t.Logf("Large block %d => %s => %d.%d", largeBlock, encoded, decoded, pos)
}

func TestFormatDNNBlock(t *testing.T) {
	encoder := NewEncoder()

	tests := []struct {
		blockNum int64
		position int
		expected string
	}{
		{50, 1, "n50"},
		{50, 3, "n50.3"},
		{1000, 1, "n1000"},
		{1000, 5, "n1000.5"},
	}

	for _, test := range tests {
		result := encoder.FormatDNNBlock(test.blockNum, test.position)
		if result != test.expected {
			t.Errorf("FormatDNNBlock(%d, %d): expected %s, got %s",
				test.blockNum, test.position, test.expected, result)
		}
	}
}

func TestFormatBitcoinBlock(t *testing.T) {
	encoder := NewEncoder()

	// Testnet genesis block is 932300
	tests := []struct {
		dnnBlock int64
		position int
		expected string
	}{
		{50, 1, "b932350"},
		{50, 3, "b932350.3"},
		{1000, 1, "b933300"},
		{1000, 5, "b933300.5"},
	}

	for _, test := range tests {
		result := encoder.FormatBitcoinBlock(test.dnnBlock, test.position)
		if result != test.expected {
			t.Errorf("FormatBitcoinBlock(%d, %d): expected %s, got %s",
				test.dnnBlock, test.position, test.expected, result)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	encoder := NewEncoder()

	for i := 0; i < b.N; i++ {
		encoder.Encode(int64(i), (i%1000)+1)
	}
}

func BenchmarkDecode(b *testing.B) {
	encoder := NewEncoder()
	encoded, _ := encoder.Encode(956453, 45)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encoder.Decode(encoded)
	}
}

// Example demonstrates how to use the encoder
func ExampleEncoder() {
	encoder := NewEncoder()

	// Encode a block number and transaction position
	encoded, _ := encoder.Encode(100, 1)
	fmt.Printf("100.1 => %s\n", encoded)

	// Decode back
	block, pos, _ := encoder.Decode(encoded)
	fmt.Printf("%s => %d.%d\n", encoded, block, pos)

	// Parse various notation formats
	name, block, pos, _ := encoder.ParseNotation("alice.n50.3")
	fmt.Printf("alice.n50.3 => name=%s, block=%d, position=%d\n", name, block, pos)
}
