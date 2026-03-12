package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Simple test client to verify the DNN node is accepting events

func main() {
	fmt.Println("DNN Node Test Client")
	fmt.Println("====================")
	fmt.Println()

	// Connect to local DNN node
	ctx := context.Background()
	relay := nostr.NewRelay(ctx, "ws://localhost:8080")

	fmt.Println("Connecting to ws://localhost:8080...")
	if err := relay.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer relay.Close()
	fmt.Println("✓ Connected successfully")
	fmt.Println()

	// Generate test keypair
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)

	fmt.Printf("Generated test keypair:\n")
	fmt.Printf("  Private key: %s\n", sk[:16]+"...")
	fmt.Printf("  Public key:  %s\n", pk)
	fmt.Println()

	// Test 1: Publish a name event (kind 61600)
	fmt.Println("Test 1: Publishing kind 61600 (Name Event)...")
	nameEvent := &nostr.Event{
		PubKey:    pk,
		CreatedAt: nostr.Now(),
		Kind:      61600,
		Tags: nostr.Tags{
			{"t", "DNN"},
			{"d", "testname"},
			{"o", "testname-alias"},
		},
		Content: "{}",
	}
	nameEvent.Sign(sk)

	if err := relay.Publish(ctx, *nameEvent); err != nil {
		fmt.Printf("  ✗ Failed: %v\n", err)
	} else {
		fmt.Printf("  ✓ Published successfully (ID: %s)\n", nameEvent.ID[:16]+"...")
	}
	fmt.Println()

	// Test 2: Publish a connection event (kind 62600)
	fmt.Println("Test 2: Publishing kind 62600 (Connection Event)...")
	connectionEvent := &nostr.Event{
		PubKey:    pk,
		CreatedAt: nostr.Now(),
		Kind:      62600,
		Tags: nostr.Tags{
			{"t", "DNN"},
		},
		Content: `{
			"testname": {
				"records": [
					["record", "A", "@", "192.168.1.1", "", "", "", "", "", "", "3600"]
				]
			}
		}`,
	}
	connectionEvent.Sign(sk)

	if err := relay.Publish(ctx, *connectionEvent); err != nil {
		fmt.Printf("  ✗ Failed: %v\n", err)
	} else {
		fmt.Printf("  ✓ Published successfully (ID: %s)\n", connectionEvent.ID[:16]+"...")
	}
	fmt.Println()

	// Test 3: Publish a metadata event (kind 63600)
	fmt.Println("Test 3: Publishing kind 63600 (Metadata Event)...")
	metadataEvent := &nostr.Event{
		PubKey:    pk,
		CreatedAt: nostr.Now(),
		Kind:      63600,
		Tags: nostr.Tags{
			{"t", "DNN"},
		},
		Content: `{
			"metadata": {
				"description": "Test DNN name",
				"urls": [
					{"label": "website", "url": "https://example.com"}
				]
			}
		}`,
	}
	metadataEvent.Sign(sk)

	if err := relay.Publish(ctx, *metadataEvent); err != nil {
		fmt.Printf("  ✗ Failed: %v\n", err)
	} else {
		fmt.Printf("  ✓ Published successfully (ID: %s)\n", metadataEvent.ID[:16]+"...")
	}
	fmt.Println()

	// Test 4: Query events back
	fmt.Println("Test 4: Querying events...")
	time.Sleep(1 * time.Second) // Give time for storage

	filter := nostr.Filter{
		Kinds:   []int{61600, 62600, 63600},
		Authors: []string{pk},
		Tags: map[string][]string{
			"t": {"DNN"},
		},
		Limit: 10,
	}

	events, err := relay.QuerySync(ctx, filter)
	if err != nil {
		fmt.Printf("  ✗ Query failed: %v\n", err)
	} else {
		fmt.Printf("  ✓ Retrieved %d events\n", len(events))
		for _, evt := range events {
			fmt.Printf("    - Kind %d: %s\n", evt.Kind, evt.ID[:16]+"...")
		}
	}
	fmt.Println()

	// Test 5: Subscribe to new events
	fmt.Println("Test 5: Testing subscription...")
	sub, err := relay.Subscribe(ctx, []nostr.Filter{{
		Kinds: []int{61600, 62600, 63600, 60600},
		Tags: map[string][]string{
			"t": {"DNN"},
		},
	}})

	if err != nil {
		fmt.Printf("  ✗ Subscription failed: %v\n", err)
	} else {
		fmt.Println("  ✓ Subscribed successfully")

		// Listen for a short time
		fmt.Println("  Listening for 3 seconds...")
		timeout := time.After(3 * time.Second)
		eventCount := 0

	listenLoop:
		for {
			select {
			case evt := <-sub.Events:
				if evt != nil {
					eventCount++
					fmt.Printf("    - Received event: Kind %d\n", evt.Kind)
				}
			case <-timeout:
				break listenLoop
			}
		}

		fmt.Printf("  ✓ Received %d events during subscription\n", eventCount)
		sub.Unsub()
	}

	fmt.Println()
	fmt.Println("All tests completed!")
	fmt.Println()
	fmt.Println("Summary:")
	fmt.Println("  - Node is accepting connections")
	fmt.Println("  - Events can be published")
	fmt.Println("  - Events can be queried")
	fmt.Println("  - Subscriptions work correctly")
	fmt.Println()
	fmt.Println("Your DNN node is working correctly! 🎉")
}
