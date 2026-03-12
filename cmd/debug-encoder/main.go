package main

import (
	"fmt"
	"dnn-node/internal/encoder"
	"dnn-node/internal/constants"
)

func main() {
	enc := encoder.NewEncoderWithNetwork(constants.NetworkTestnet)

	// Decode what the user typed
	block1, pos1, err1 := enc.Decode("nabandonaread")
	fmt.Printf("nabandonaread -> block=%d, pos=%d, err=%v\n", block1, pos1, err1)

	// Decode what the node re-encoded
	block2, pos2, err2 := enc.Decode("ndespairhoodd")
	fmt.Printf("ndespairhoodd -> block=%d, pos=%d, err=%v\n", block2, pos2, err2)

	// Encode block 85, position 4 to see what the current encoder produces
	encoded, err3 := enc.Encode(85, 4)
	fmt.Printf("Encode(85, 4)  -> %s, err=%v\n", encoded, err3)

	// Also try encoding whatever nabandonaread decodes to
	if err1 == nil {
		reencoded, _ := enc.Encode(block1, pos1)
		fmt.Printf("Encode(%d, %d) -> %s\n", block1, pos1, reencoded)
	}
}
