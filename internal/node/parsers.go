package node

import (
	"encoding/json"
	"fmt"
)

// DNSRecord represents a parsed DNS record from connection content
type DNSRecord struct {
	Type     string   `json:"type"`               // A, AAAA, CNAME, TXT, MX, SRV
	Name     string   `json:"name"`               // @ or subdomain
	Values   []string `json:"values"`             // IP addresses, hostnames, or text
	TTL      int      `json:"ttl"`                // Time to live
	Priority *int     `json:"priority,omitempty"` // For MX, SRV
	Weight   *int     `json:"weight,omitempty"`   // For SRV
	Port     *int     `json:"port,omitempty"`     // For SRV
}

// CertSignature represents the Schnorr signature on a certificate
type CertSignature struct {
	Hash      string `json:"hash"`                // SHA256 hash of the PEM
	Signature string `json:"signature"`           // Schnorr signature
	Pubkey    string `json:"pubkey"`              // Signing pubkey (should match DNN owner)
	SignedAt  int64  `json:"signed_at,omitempty"` // When the cert was signed
}

// CertInfo represents a TLS certificate from connection content
type CertInfo struct {
	PEM           string         `json:"pem"`                      // X.509 certificate in PEM format
	CertSignature *CertSignature `json:"cert_signature,omitempty"` // Nostr signature info
	Expires       int64          `json:"expires,omitempty"`        // Expiration timestamp
}

// DirectTransport represents the direct IPv6 interception transport
type DirectTransport struct {
	IPv6 string `json:"ipv6"` // Deterministic IPv6 from SHA-256(npub)
}

// Transports holds all transport options for reaching the server
type Transports struct {
	Relay      []string         `json:"relay,omitempty"`       // Nostr relay websocket URLs
	Direct     *DirectTransport `json:"direct,omitempty"`      // IPv6 interception
	Tor        []string         `json:"tor,omitempty"`         // .onion addresses
	TollgateID []string         `json:"tollgate-id,omitempty"` // TollGate mesh IDs
	DHT        []string         `json:"dht,omitempty"`         // DHT node IDs
	Ethereum   []string         `json:"ethereum,omitempty"`    // Ethereum addresses
}

// ParsedConnection represents structured connection data
type ParsedConnection struct {
	Records      []DNSRecord            `json:"records"`
	Cert         *CertInfo              `json:"cert,omitempty"`
	Meta         map[string]interface{} `json:"meta,omitempty"`
	Delegation   string                 `json:"delegation,omitempty"`   // naddr to delegated 62600 event
	Npub         []string               `json:"npub,omitempty"`         // Server identity (array for multi-server)
	Transports   *Transports            `json:"transports,omitempty"`   // Transport options
	Capabilities []string               `json:"capabilities,omitempty"` // Supported protocols (http, https, etc.)
}

// ParsedMetadata represents structured metadata
type ParsedMetadata struct {
	Description string                   `json:"description,omitempty"`
	Relays      []string                 `json:"relays,omitempty"`
	Currencies  []map[string]interface{} `json:"currencies,omitempty"`
	Addresses   map[string]string        `json:"addresses,omitempty"`
	Links       map[string]string        `json:"links,omitempty"`
	UpdatedAt   int64                    `json:"updated_at,omitempty"`
	Raw         map[string]interface{}   `json:"raw,omitempty"` // Fallback for unknown fields
}

// parseConnectionContent parses connection JSON for a specific domain name.
// Each key in the 62600 content is an explicit domain name.
func parseConnectionContent(content string, domainName string) (*ParsedConnection, error) {
	if content == "" {
		return &ParsedConnection{Records: []DNSRecord{}}, nil
	}

	// Parse all keys from the connection JSON
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse connection JSON: %w", err)
	}

	// Look up the domain name key
	var domainData json.RawMessage
	var found bool
	if domainName != "" {
		domainData, found = raw[domainName]
	}
	if !found {
		// Fall back to first key in the map
		for _, v := range raw {
			domainData = v
			found = true
			break
		}
	}
	if !found {
		return &ParsedConnection{Records: []DNSRecord{}}, nil
	}

	return parseDomainConnectionData(domainData)
}

// parseSubdomainConnectionContent parses a specific subdomain's connection from the JSON.
// This is now a simple wrapper around the domain key lookup.
func parseSubdomainConnectionContent(content string, subdomain string) (*ParsedConnection, error) {
	if content == "" || subdomain == "" {
		return nil, fmt.Errorf("empty content or subdomain")
	}

	// Parse all keys from the connection JSON
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse connection JSON: %w", err)
	}

	// Look up the subdomain key directly
	subdomainData, exists := raw[subdomain]
	if !exists {
		return nil, fmt.Errorf("subdomain '%s' not found in connection content", subdomain)
	}

	return parseDomainConnectionData(subdomainData)
}

// parseDomainConnectionData parses a single domain's connection data from raw JSON
func parseDomainConnectionData(data json.RawMessage) (*ParsedConnection, error) {
	var connData struct {
		Records    [][]string             `json:"records"`
		Meta       map[string]interface{} `json:"meta"`
		Delegation string                 `json:"delegation"` // naddr to delegated 62600
		Cert       *struct {
			// New format: chain of certs
			Chain []struct {
				Type string `json:"type"` // "leaf" or "intermediate"
				PEM  string `json:"pem"`
			} `json:"chain"`
			// Old format: direct PEM
			PEM           string `json:"pem"`
			CertSignature *struct {
				Hash      string `json:"hash"`
				Signature string `json:"signature"`
				Pubkey    string `json:"pubkey"`
				SignedAt  int64  `json:"signed_at"`
			} `json:"cert_signature"`
			Expires int64 `json:"expires"`
		} `json:"cert"`
		// New 62600 spec fields
		Npub         json.RawMessage `json:"npub"`         // Can be string or []string
		Transports   *Transports     `json:"transports"`   // Transport options
		Capabilities []string        `json:"capabilities"` // Supported protocols
	}

	if err := json.Unmarshal(data, &connData); err != nil {
		return nil, fmt.Errorf("failed to parse domain connection: %w", err)
	}

	// Convert raw records to structured format
	records := make([]DNSRecord, 0, len(connData.Records))
	for _, raw := range connData.Records {
		if len(raw) < 3 {
			continue // Skip invalid records
		}

		record := DNSRecord{
			Type: raw[0],
			Name: raw[1],
			TTL:  3600, // Default TTL
		}

		// Parse based on record type
		switch record.Type {
		case "A", "AAAA", "CNAME", "TXT":
			record.Values = []string{raw[2]}
			if len(raw) > 3 {
				fmt.Sscanf(raw[3], "%d", &record.TTL)
			}

		case "MX":
			if len(raw) >= 4 {
				var priority int
				fmt.Sscanf(raw[2], "%d", &priority)
				record.Priority = &priority
				record.Values = []string{raw[3]}
				if len(raw) > 4 {
					fmt.Sscanf(raw[4], "%d", &record.TTL)
				}
			}

		case "SRV":
			if len(raw) >= 6 {
				var priority, weight, port int
				fmt.Sscanf(raw[2], "%d", &priority)
				fmt.Sscanf(raw[3], "%d", &weight)
				fmt.Sscanf(raw[4], "%d", &port)
				record.Priority = &priority
				record.Weight = &weight
				record.Port = &port
				record.Values = []string{raw[5]}
				if len(raw) > 6 {
					fmt.Sscanf(raw[6], "%d", &record.TTL)
				}
			}
		}

		records = append(records, record)
	}

	// Build cert info if present (support both chain format and direct pem format)
	var certInfo *CertInfo
	if connData.Cert != nil {
		var certPEM string

		// Try chain format first (new format with array of certs)
		if len(connData.Cert.Chain) > 0 {
			// Find the leaf certificate
			for _, cert := range connData.Cert.Chain {
				if cert.Type == "leaf" && cert.PEM != "" {
					certPEM = cert.PEM
					break
				}
			}
			// If no leaf found, use first cert
			if certPEM == "" && connData.Cert.Chain[0].PEM != "" {
				certPEM = connData.Cert.Chain[0].PEM
			}
		}

		// Fallback to direct PEM format (old format)
		if certPEM == "" && connData.Cert.PEM != "" {
			certPEM = connData.Cert.PEM
		}

		if certPEM != "" {
			certInfo = &CertInfo{
				PEM:     certPEM,
				Expires: connData.Cert.Expires,
			}
			if connData.Cert.CertSignature != nil {
				certInfo.CertSignature = &CertSignature{
					Hash:      connData.Cert.CertSignature.Hash,
					Signature: connData.Cert.CertSignature.Signature,
					Pubkey:    connData.Cert.CertSignature.Pubkey,
					SignedAt:  connData.Cert.CertSignature.SignedAt,
				}
			}
		}
	}

	// Parse npub field - can be string or []string for backwards compatibility
	var npubs []string
	if len(connData.Npub) > 0 {
		// Try to parse as array first
		if err := json.Unmarshal(connData.Npub, &npubs); err != nil {
			// Try as single string
			var singleNpub string
			if err := json.Unmarshal(connData.Npub, &singleNpub); err == nil && singleNpub != "" {
				npubs = []string{singleNpub}
			}
		}
	}

	return &ParsedConnection{
		Records:      records,
		Cert:         certInfo,
		Meta:         connData.Meta,
		Delegation:   connData.Delegation,
		Npub:         npubs,
		Transports:   connData.Transports,
		Capabilities: connData.Capabilities,
	}, nil
}

// parseMetadataContent parses metadata JSON into structured format
func parseMetadataContent(content string) (*ParsedMetadata, error) {
	if content == "" {
		return &ParsedMetadata{}, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}

	metadata := &ParsedMetadata{
		Raw: raw,
	}

	// The 63600 content wraps fields under a "metadata" key:
	// {"updated_at": ..., "metadata": {"description": "...", "relays": [...]}}
	// Unwrap the "metadata" key if present, otherwise use raw directly
	fields := raw
	if metadataMap, ok := raw["metadata"].(map[string]interface{}); ok {
		fields = metadataMap
	}

	// Extract known fields from the metadata object
	if desc, ok := fields["description"].(string); ok {
		metadata.Description = desc
	}

	// Extract relays from metadata
	if relayList, ok := fields["relays"].([]interface{}); ok {
		for _, r := range relayList {
			if relayURL, ok := r.(string); ok {
				metadata.Relays = append(metadata.Relays, relayURL)
			}
		}
	}

	if currencies, ok := fields["currencies"].([]interface{}); ok {
		metadata.Currencies = make([]map[string]interface{}, 0, len(currencies))
		for _, c := range currencies {
			if curr, ok := c.(map[string]interface{}); ok {
				metadata.Currencies = append(metadata.Currencies, curr)
			}
		}
	}

	if addresses, ok := fields["addresses"].(map[string]interface{}); ok {
		metadata.Addresses = make(map[string]string)
		for k, v := range addresses {
			if addr, ok := v.(string); ok {
				metadata.Addresses[k] = addr
			}
		}
	}

	if links, ok := fields["links"].(map[string]interface{}); ok {
		metadata.Links = make(map[string]string)
		for k, v := range links {
			if link, ok := v.(string); ok {
				metadata.Links[k] = link
			}
		}
	}

	// updated_at is at the top level, not inside metadata
	if updatedAt, ok := raw["updated_at"].(float64); ok {
		metadata.UpdatedAt = int64(updatedAt)
	}

	return metadata, nil
}
