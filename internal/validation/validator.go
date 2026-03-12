package validation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

// Validator handles DNN event validation
type Validator struct {
	namePattern *regexp.Regexp
}

// NewValidator creates a new validator
func NewValidator() *Validator {
	// Names can contain lowercase letters, numbers, and hyphens
	// Can start with letter or number (like .com domains: 123.com, test.com)
	// Must end with letter or number (not hyphen)
	// Minimum 2 characters, maximum 63 characters
	namePattern := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

	return &Validator{
		namePattern: namePattern,
	}
}

// ValidateName validates a DNN name
// Names are normalized to lowercase since DNS is case-insensitive
func (v *Validator) ValidateName(name string) error {
	// Normalize to lowercase for validation
	name = strings.ToLower(name)

	if len(name) < 2 {
		return fmt.Errorf("name too short: minimum 2 characters (got '%s' with length %d)", name, len(name))
	}

	if len(name) > 63 {
		return fmt.Errorf("name too long: maximum 63 characters (got '%s' with length %d)", name, len(name))
	}

	if !v.namePattern.MatchString(name) {
		return fmt.Errorf("invalid name format: must contain only letters, numbers, and hyphens (got '%s')", name)
	}

	// Check for consecutive hyphens
	if strings.Contains(name, "--") {
		return fmt.Errorf("invalid name: consecutive hyphens not allowed")
	}

	// Check for reserved names
	reserved := []string{"admin", "root", "system", "api", "www", "mail", "ftp", "localhost"}
	for _, r := range reserved {
		if name == r {
			return fmt.Errorf("name '%s' is reserved", name)
		}
	}

	return nil
}

// ValidateNameEvent validates a kind 61600 event
func (v *Validator) ValidateNameEvent(event *nostr.Event) error {
	if event.Kind != 61600 {
		return fmt.Errorf("invalid kind: expected 61600, got %d", event.Kind)
	}

	// Check for DNN tag
	hasDNNTag := false
	var dTag, primaryName string
	var otherNames []string

	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			if tag[0] == "t" && tag[1] == "DNN" {
				hasDNNTag = true
			}
			if tag[0] == "d" {
				dTag = tag[1]
			}
			if tag[0] == "n" {
				primaryName = tag[1]
			}
			if tag[0] == "o" {
				otherNames = append(otherNames, tag[1])
			}
		}
	}

	if !hasDNNTag {
		return fmt.Errorf("missing required DNN tag")
	}

	if dTag == "" {
		return fmt.Errorf("missing required d tag")
	}

	if primaryName == "" {
		return fmt.Errorf("missing required n tag (primary name)")
	}

	// Validate primary name (n tag, not d tag!)
	if err := v.ValidateName(primaryName); err != nil {
		return fmt.Errorf("invalid primary name: %w", err)
	}

	// Validate other names
	for _, name := range otherNames {
		if err := v.ValidateName(name); err != nil {
			return fmt.Errorf("invalid other name '%s': %w", name, err)
		}
	}

	// Check for duplicate names
	nameMap := make(map[string]bool)
	nameMap[primaryName] = true
	for _, name := range otherNames {
		if nameMap[name] {
			return fmt.Errorf("duplicate name: %s", name)
		}
		nameMap[name] = true
	}

	return nil
}

// ValidateConnectionEvent validates a kind 62600 event
func (v *Validator) ValidateConnectionEvent(event *nostr.Event) error {
	if event.Kind != 62600 {
		return fmt.Errorf("invalid kind: expected 62600, got %d", event.Kind)
	}

	// Check for DNN tag
	hasDNNTag := false
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "DNN" {
			hasDNNTag = true
			break
		}
	}

	if !hasDNNTag {
		return fmt.Errorf("missing required DNN tag")
	}

	// Validate content is valid JSON
	if event.Content == "" {
		return fmt.Errorf("connection content cannot be empty")
	}

	var content map[string]interface{}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return fmt.Errorf("invalid JSON content: %w", err)
	}

	// Connection content must have at least one domain key
	// Each key is an explicit domain name
	if len(content) == 0 {
		return fmt.Errorf("connection data must contain at least one domain key")
	}

	// Validate each connection entry
	for key, value := range content {
		conn, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid connection format for key '%s'", key)
		}

		// Check for records array
		if records, ok := conn["records"]; ok {
			recordsArray, ok := records.([]interface{})
			if !ok {
				return fmt.Errorf("invalid records format for key '%s'", key)
			}

			// Validate each record
			for _, record := range recordsArray {
				if err := v.validateDNSRecord(record); err != nil {
					return fmt.Errorf("invalid DNS record in '%s': %w", key, err)
				}
			}
		}

		// Validate certificates if present
		if cert, ok := conn["cert"]; ok {
			if err := v.validateCertificates(cert); err != nil {
				return fmt.Errorf("invalid certificate in '%s': %w", key, err)
			}
		}
	}

	return nil
}

// validateDNSRecord validates a DNS record entry
// Format: ["TYPE", "name", ...values, "ttl"]
// Examples:
// - ["A", "@", "203.0.113.41", "3600"]
// - ["AAAA", "@", "2001:db8::1", "3600"]
// - ["SRV", "_https._tcp", "10", "5", "443", "@", "3600"]
func (v *Validator) validateDNSRecord(record interface{}) error {
	recordArray, ok := record.([]interface{})
	if !ok || len(recordArray) < 3 {
		return fmt.Errorf("invalid record format: must be array with at least [type, name, value]")
	}

	// Validate record type (first element)
	recordType, ok := recordArray[0].(string)
	if !ok {
		return fmt.Errorf("invalid record type: must be string")
	}

	validTypes := []string{"A", "AAAA", "CNAME", "MX", "TXT", "SRV", "NS", "PTR", "SOA", "CAA"}
	isValid := false
	for _, valid := range validTypes {
		if recordType == valid {
			isValid = true
			break
		}
	}

	if !isValid {
		return fmt.Errorf("unsupported record type: %s", recordType)
	}

	return nil
}

// validateCertificates validates certificate data
func (v *Validator) validateCertificates(cert interface{}) error {
	certMap, ok := cert.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid certificate format")
	}

	// Check for chain array
	if chain, ok := certMap["chain"]; ok {
		chainArray, ok := chain.([]interface{})
		if !ok {
			return fmt.Errorf("invalid certificate chain format")
		}

		for _, certObj := range chainArray {
			certData, ok := certObj.(map[string]interface{})
			if !ok {
				return fmt.Errorf("invalid certificate object format")
			}

			// Check for required fields
			if _, ok := certData["type"]; !ok {
				return fmt.Errorf("certificate missing 'type' field")
			}
			if _, ok := certData["pem"]; !ok {
				return fmt.Errorf("certificate missing 'pem' field")
			}

			// Validate PEM format
			pem, ok := certData["pem"].(string)
			if !ok || !strings.Contains(pem, "-----BEGIN CERTIFICATE-----") {
				return fmt.Errorf("invalid PEM certificate format")
			}
		}
	}

	return nil
}

// ValidateMetadataEvent validates a kind 63600 event
func (v *Validator) ValidateMetadataEvent(event *nostr.Event) error {
	if event.Kind != 63600 {
		return fmt.Errorf("invalid kind: expected 63600, got %d", event.Kind)
	}

	// Check for DNN tag
	hasDNNTag := false
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "DNN" {
			hasDNNTag = true
			break
		}
	}

	if !hasDNNTag {
		return fmt.Errorf("missing required DNN tag")
	}

	// Validate content is valid JSON
	if event.Content == "" {
		return fmt.Errorf("metadata content cannot be empty")
	}

	var content map[string]interface{}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return fmt.Errorf("invalid JSON content: %w", err)
	}

	return nil
}

// ValidateAnchorEvent validates a kind 60600 event
func (v *Validator) ValidateAnchorEvent(event *nostr.Event) error {
	if event.Kind != 60600 {
		return fmt.Errorf("invalid kind: expected 60600, got %d", event.Kind)
	}

	// Check for required tags
	// Single-character tags for relay indexability: n=names, c=connection, m=metadata, x=transaction
	var hasDNNTag, hasDTag, hasNames, hasConnection, hasMetadata, hasTransaction bool

	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "t":
				if tag[1] == "DNN" {
					hasDNNTag = true
				}
			case "d":
				hasDTag = true
			case "n":
				hasNames = true
			case "c":
				hasConnection = true
			case "m":
				hasMetadata = true
			case "x":
				hasTransaction = true
			}
		}
	}

	if !hasDNNTag {
		return fmt.Errorf("missing required DNN tag")
	}

	if !hasDTag {
		return fmt.Errorf("missing required d tag")
	}

	if !hasNames {
		return fmt.Errorf("missing required n tag (names reference)")
	}

	if !hasConnection {
		return fmt.Errorf("missing required c tag (connection reference)")
	}

	if !hasMetadata {
		return fmt.Errorf("missing required m tag (metadata reference)")
	}

	if !hasTransaction {
		return fmt.Errorf("missing required x tag (transaction ID)")
	}

	return nil
}

// ValidateDNNEvent validates any DNN event
func (v *Validator) ValidateDNNEvent(event *nostr.Event) error {
	// Check signature
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		return fmt.Errorf("invalid signature")
	}

	// Validate based on kind
	switch event.Kind {
	case 61600:
		return v.ValidateNameEvent(event)
	case 62600:
		return v.ValidateConnectionEvent(event)
	case 63600:
		return v.ValidateMetadataEvent(event)
	case 60600:
		return v.ValidateAnchorEvent(event)
	default:
		return fmt.Errorf("unsupported DNN event kind: %d", event.Kind)
	}
}
