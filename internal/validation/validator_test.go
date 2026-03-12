package validation

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestValidateName(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name        string
		input       string
		shouldError bool
		errorMsg    string
	}{
		// Valid names
		{"valid short name", "ab", false, ""},
		{"valid medium name", "alice", false, ""},
		{"valid with hyphen", "alice-smith", false, ""},
		{"valid with numbers", "alice123", false, ""},
		{"valid max length", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz", false, ""},

		// Invalid names
		{"too short", "a", true, "too short"},
		{"too long", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0", true, "too long"},
		{"starts with number", "1alice", true, "invalid name format"},
		{"starts with hyphen", "-alice", true, "invalid name format"},
		{"ends with hyphen", "alice-", true, "invalid name format"},
		{"consecutive hyphens", "alice--smith", true, "consecutive hyphens"},
		{"uppercase letters", "Alice", true, "invalid name format"},
		{"special characters", "alice@smith", true, "invalid name format"},
		{"reserved name admin", "admin", true, "reserved"},
		{"reserved name root", "root", true, "reserved"},
		{"reserved name system", "system", true, "reserved"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.ValidateName(test.input)
			if test.shouldError && err == nil {
				t.Errorf("Expected error for %s, but got none", test.input)
			}
			if !test.shouldError && err != nil {
				t.Errorf("Unexpected error for %s: %v", test.input, err)
			}
		})
	}
}

func TestValidateNameEvent(t *testing.T) {
	validator := NewValidator()

	// Create a valid name event
	validEvent := &nostr.Event{
		Kind:      61600,
		CreatedAt: nostr.Timestamp(1234567890),
		Tags: nostr.Tags{
			{"t", "DNN"},
			{"d", "uuid-goes-here"},
			{"n", "alice"}, // Primary name (n tag)
			{"o", "alice-smith"},
		},
		Content: "{}",
	}

	// Generate ID and sign (mock for testing)
	validEvent.ID = "validid"
	validEvent.PubKey = "validpubkey"
	validEvent.Sig = "validsig"

	tests := []struct {
		name        string
		event       *nostr.Event
		shouldError bool
		errorMsg    string
	}{
		{
			name:        "valid event",
			event:       validEvent,
			shouldError: false,
		},
		{
			name: "wrong kind",
			event: &nostr.Event{
				Kind: 1,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "alice"},
				},
			},
			shouldError: true,
			errorMsg:    "invalid kind",
		},
		{
			name: "missing DNN tag",
			event: &nostr.Event{
				Kind: 61600,
				Tags: nostr.Tags{
					{"d", "uuid"},
					{"n", "alice"},
				},
			},
			shouldError: true,
			errorMsg:    "missing required DNN tag",
		},
		{
			name: "missing d tag",
			event: &nostr.Event{
				Kind: 61600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"n", "alice"},
				},
			},
			shouldError: true,
			errorMsg:    "missing required d tag",
		},
		{
			name: "invalid primary name",
			event: &nostr.Event{
				Kind: 61600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "uuid"},
					{"n", "A"}, // too short
				},
			},
			shouldError: true,
			errorMsg:    "invalid primary name",
		},
		{
			name: "duplicate names",
			event: &nostr.Event{
				Kind: 61600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "uuid"},
					{"n", "alice"},
					{"o", "alice"}, // duplicate
				},
			},
			shouldError: true,
			errorMsg:    "duplicate name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.ValidateNameEvent(test.event)
			if test.shouldError && err == nil {
				t.Errorf("Expected error, but got none")
			}
			if !test.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateConnectionEvent(t *testing.T) {
	validator := NewValidator()

	validContent := `{
		"myname": {
			"records": [
				["record", "A", "@", "192.168.1.1", "", "", "", "", "", "", "3600"]
			],
			"cert": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----"
		}
	}`

	tests := []struct {
		name        string
		event       *nostr.Event
		shouldError bool
	}{
		{
			name: "valid event",
			event: &nostr.Event{
				Kind: 62600,
				Tags: nostr.Tags{
					{"t", "DNN"},
				},
				Content: validContent,
			},
			shouldError: false,
		},
		{
			name: "wrong kind",
			event: &nostr.Event{
				Kind: 61600,
				Tags: nostr.Tags{
					{"t", "DNN"},
				},
				Content: validContent,
			},
			shouldError: true,
		},
		{
			name: "missing DNN tag",
			event: &nostr.Event{
				Kind:    62600,
				Tags:    nostr.Tags{},
				Content: validContent,
			},
			shouldError: true,
		},
		{
			name: "empty content",
			event: &nostr.Event{
				Kind: 62600,
				Tags: nostr.Tags{
					{"t", "DNN"},
				},
				Content: "",
			},
			shouldError: true,
		},
		{
			name: "invalid JSON",
			event: &nostr.Event{
				Kind: 62600,
				Tags: nostr.Tags{
					{"t", "DNN"},
				},
				Content: "not json",
			},
			shouldError: true,
		},
		{
			name: "valid with named key",
			event: &nostr.Event{
				Kind: 62600,
				Tags: nostr.Tags{
					{"t", "DNN"},
				},
				Content: `{"other": {}}`,
			},
			shouldError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.ValidateConnectionEvent(test.event)
			if test.shouldError && err == nil {
				t.Errorf("Expected error, but got none")
			}
			if !test.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestValidateAnchorEvent(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name        string
		event       *nostr.Event
		shouldError bool
	}{
		{
			name: "valid event",
			event: &nostr.Event{
				Kind: 60600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "eventid123"},
					{"n", "eventid456"},
					{"c", "eventid456"},
					{"m", "eventid789"},
					{"x", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
			shouldError: false,
		},
		{
			name: "wrong kind",
			event: &nostr.Event{
				Kind: 61600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "eventid123"},
					{"n", "eventid456"},
					{"c", "eventid456"},
					{"m", "eventid789"},
					{"x", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
			shouldError: true,
		},
		{
			name: "missing DNN tag",
			event: &nostr.Event{
				Kind: 60600,
				Tags: nostr.Tags{
					{"d", "eventid123"},
					{"n", "eventid456"},
					{"c", "eventid456"},
					{"m", "eventid789"},
					{"x", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
			shouldError: true,
		},
		{
			name: "missing d tag",
			event: &nostr.Event{
				Kind: 60600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"n", "eventid456"},
					{"c", "eventid456"},
					{"m", "eventid789"},
					{"x", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
			shouldError: true,
		},
		{
			name: "missing c tag (connection)",
			event: &nostr.Event{
				Kind: 60600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "eventid123"},
					{"n", "eventid456"},
					{"m", "eventid789"},
					{"x", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
			shouldError: true,
		},
		{
			name: "invalid transaction ID length",
			event: &nostr.Event{
				Kind: 60600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "eventid123"},
					{"n", "eventid456"},
					{"c", "eventid456"},
					{"m", "eventid789"},
					{"x", "tooshort"},
				},
			},
			shouldError: true,
		},
		{
			name: "invalid transaction ID hex",
			event: &nostr.Event{
				Kind: 60600,
				Tags: nostr.Tags{
					{"t", "DNN"},
					{"d", "eventid123"},
					{"n", "eventid456"},
					{"c", "eventid456"},
					{"m", "eventid789"},
					{"x", "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"}, // 'g' is not hex
				},
			},
			shouldError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.ValidateAnchorEvent(test.event)
			if test.shouldError && err == nil {
				t.Errorf("Expected error, but got none")
			}
			if !test.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func BenchmarkValidateName(b *testing.B) {
	validator := NewValidator()

	for i := 0; i < b.N; i++ {
		validator.ValidateName("alice-smith-123")
	}
}

func BenchmarkValidateNameEvent(b *testing.B) {
	validator := NewValidator()

	event := &nostr.Event{
		Kind: 61600,
		Tags: nostr.Tags{
			{"t", "DNN"},
			{"d", "alice"},
			{"o", "alice-smith"},
			{"o", "alice-jones"},
		},
		Content: "{}",
		ID:      "validid",
		PubKey:  "validpubkey",
		Sig:     "validsig",
	}

	for i := 0; i < b.N; i++ {
		validator.ValidateNameEvent(event)
	}
}
