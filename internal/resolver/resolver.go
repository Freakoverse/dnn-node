package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dnn-node/internal/database"
	"dnn-node/internal/encoder"
)

// Resolver handles DNN name resolution
type Resolver struct {
	db      *database.Database
	encoder *encoder.Encoder
	cache   *ResolverCache
}

// ResolverCache provides in-memory caching for resolved names
type ResolverCache struct {
	entries map[string]*CacheEntry
	maxSize int
	ttl     time.Duration
}

// CacheEntry represents a cached resolution result
type CacheEntry struct {
	Result    *ResolutionResult
	ExpiresAt time.Time
}

// ResolutionResult represents a complete resolved name
type ResolutionResult struct {
	Name              string                 `json:"name"`
	Pubkey            string                 `json:"pubkey"`
	DNNBlock          int64                  `json:"dnn_block"`
	BitcoinBlock      int64                  `json:"bitcoin_block"`
	Position          int                    `json:"position"`
	Connection        map[string]interface{} `json:"connection"`
	Metadata          map[string]interface{} `json:"metadata"`
	EncodedNotation   string                 `json:"encoded_notation"`
	AlternateNotations []string              `json:"alternate_notations"`
	CreatedAt         time.Time              `json:"created_at"`
}

// NewResolver creates a new resolver with testnet defaults
func NewResolver(db *database.Database) *Resolver {
	return NewResolverWithNetwork(db, "testnet")
}

// NewResolverWithNetwork creates a new resolver for a specific network
func NewResolverWithNetwork(db *database.Database, networkMode string) *Resolver {
	return &Resolver{
		db:      db,
		encoder: encoder.NewEncoderWithNetwork(networkMode),
		cache: &ResolverCache{
			entries: make(map[string]*CacheEntry),
			maxSize: 1000,
			ttl:     5 * time.Minute,
		},
	}
}

// Resolve resolves a DNN name in any supported format
func (r *Resolver) Resolve(ctx context.Context, input string) (*ResolutionResult, error) {
	// Check cache first
	if cached := r.cache.Get(input); cached != nil {
		return cached, nil
	}

	// Parse the input notation
	name, blockNum, position, err := r.parseInput(input)
	if err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	// Query database
	record, err := r.db.GetAnchorByName(name, blockNum, &position)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}

	if record == nil {
		return nil, fmt.Errorf("name not found: %s", name)
	}

	// Build resolution result
	result, err := r.buildResult(record)
	if err != nil {
		return nil, fmt.Errorf("failed to build result: %w", err)
	}

	// Cache the result
	r.cache.Set(input, result)

	return result, nil
}

// parseInput parses various input formats
func (r *Resolver) parseInput(input string) (name string, blockNum *int64, position int, err error) {
	// Remove any protocol prefix (dnn://, https://, etc.)
	input = strings.TrimPrefix(input, "dnn://")
	input = strings.TrimPrefix(input, "https://")
	input = strings.TrimPrefix(input, "http://")

	// Check if it's just a name (no block/position)
	if !strings.Contains(input, ".") {
		return input, nil, 1, nil
	}

	// Try to parse as name.notation
	parts := strings.SplitN(input, ".", 2)
	if len(parts) != 2 {
		return "", nil, 0, fmt.Errorf("invalid format")
	}

	name = parts[0]
	notation := parts[1]

	// Check for encoded format (e.g., agd-abandon)
	if strings.Contains(notation, "-") {
		block, pos, err := r.encoder.Decode(notation)
		if err == nil {
			return name, &block, pos, nil
		}
	}

	// Try to parse as block notation
	parsedName, block, pos, err := r.encoder.ParseNotation(input)
	if err == nil {
		return parsedName, &block, pos, nil
	}

	// If all parsing fails, treat the whole thing as a name
	return input, nil, 1, nil
}

// buildResult builds a complete resolution result
func (r *Resolver) buildResult(record *database.AnchorRecord) (*ResolutionResult, error) {
	// Parse connection data
	var connection map[string]interface{}
	if err := json.Unmarshal([]byte(record.ConnectionContent), &connection); err != nil {
		return nil, fmt.Errorf("invalid connection data: %w", err)
	}

	// Parse metadata
	var metadataWrapper map[string]interface{}
	if err := json.Unmarshal([]byte(record.MetadataContent), &metadataWrapper); err != nil {
		return nil, fmt.Errorf("invalid metadata: %w", err)
	}

	metadata, _ := metadataWrapper["metadata"].(map[string]interface{})

	// Generate encoded notation
	encoded, _ := r.encoder.Encode(record.DNNBlock, record.Position)

	// Generate alternate notations
	alternates := r.generateAlternateNotations(record.Name, record.DNNBlock, record.Position, encoded)

	result := &ResolutionResult{
		Name:               record.Name,
		Pubkey:             record.Pubkey,
		DNNBlock:           record.DNNBlock,
		BitcoinBlock:       record.BitcoinBlock,
		Position:           record.Position,
		Connection:         connection,
		Metadata:           metadata,
		EncodedNotation:    encoded,
		AlternateNotations: alternates,
		CreatedAt:          time.Unix(record.CreatedAt, 0),
	}

	return result, nil
}

// generateAlternateNotations generates all valid notation formats for a name
func (r *Resolver) generateAlternateNotations(name string, dnnBlock int64, position int, encoded string) []string {
	notations := []string{
		// DNN block format
		r.encoder.FormatDNNBlock(dnnBlock, position),
		// Bitcoin block format
		r.encoder.FormatBitcoinBlock(dnnBlock, position),
		// Encoded format
		encoded,
	}

	// Add with name prefix
	result := []string{}
	for _, notation := range notations {
		result = append(result, fmt.Sprintf("%s.%s", name, notation))
	}

	// Add shorthand versions if applicable
	if position == 1 {
		result = append(result, fmt.Sprintf("%s.n%d", name, dnnBlock))
		result = append(result, fmt.Sprintf("%s.b%d", name, dnnBlock+1000000))
	}

	return result
}

// ResolveMultiple resolves multiple names in parallel
func (r *Resolver) ResolveMultiple(ctx context.Context, names []string) (map[string]*ResolutionResult, error) {
	results := make(map[string]*ResolutionResult)
	errChan := make(chan error, len(names))
	resultChan := make(chan struct {
		name   string
		result *ResolutionResult
	}, len(names))

	// Resolve each name concurrently
	for _, name := range names {
		go func(n string) {
			result, err := r.Resolve(ctx, n)
			if err != nil {
				errChan <- fmt.Errorf("%s: %w", n, err)
				return
			}
			resultChan <- struct {
				name   string
				result *ResolutionResult
			}{name: n, result: result}
		}(name)
	}

	// Collect results
	for i := 0; i < len(names); i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-errChan:
			// Continue collecting other results even if one fails
			_ = err
		case r := <-resultChan:
			results[r.name] = r.result
		}
	}

	return results, nil
}

// ResolvePath resolves a path-like notation (e.g., alice.n50/profile)
func (r *Resolver) ResolvePath(ctx context.Context, path string) (*PathResolution, error) {
	parts := strings.Split(path, "/")
	if len(parts) < 1 {
		return nil, fmt.Errorf("empty path")
	}

	// Resolve the base name
	baseName := parts[0]
	result, err := r.Resolve(ctx, baseName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base name: %w", err)
	}

	// Build path resolution
	pathRes := &PathResolution{
		Base:     result,
		Path:     path,
		Segments: parts[1:],
	}

	// If there are additional segments, try to resolve them from metadata
	if len(parts) > 1 {
		pathRes.ResolvedValue = r.resolveMetadataPath(result.Metadata, parts[1:])
	}

	return pathRes, nil
}

// resolveMetadataPath traverses metadata to find a value at the given path
func (r *Resolver) resolveMetadataPath(metadata map[string]interface{}, segments []string) interface{} {
	current := metadata

	for _, segment := range segments {
		if current == nil {
			return nil
		}

		value, ok := current[segment]
		if !ok {
			return nil
		}

		// Try to traverse deeper if it's a map
		if nextMap, ok := value.(map[string]interface{}); ok {
			current = nextMap
		} else {
			// Return the value if we've reached a leaf or there are no more segments
			return value
		}
	}

	return current
}

// PathResolution represents a resolved path
type PathResolution struct {
	Base          *ResolutionResult `json:"base"`
	Path          string            `json:"path"`
	Segments      []string          `json:"segments"`
	ResolvedValue interface{}       `json:"resolved_value,omitempty"`
}

// Cache methods

// Get retrieves a cached result
func (rc *ResolverCache) Get(key string) *ResolutionResult {
	entry, ok := rc.entries[key]
	if !ok {
		return nil
	}

	if time.Now().After(entry.ExpiresAt) {
		delete(rc.entries, key)
		return nil
	}

	return entry.Result
}

// Set stores a result in the cache
func (rc *ResolverCache) Set(key string, result *ResolutionResult) {
	// Implement simple eviction if cache is full
	if len(rc.entries) >= rc.maxSize {
		// Remove oldest entry (simple strategy)
		for k := range rc.entries {
			delete(rc.entries, k)
			break
		}
	}

	rc.entries[key] = &CacheEntry{
		Result:    result,
		ExpiresAt: time.Now().Add(rc.ttl),
	}
}

// Clear clears the cache
func (rc *ResolverCache) Clear() {
	rc.entries = make(map[string]*CacheEntry)
}

// GetStats returns cache statistics
func (rc *ResolverCache) GetStats() CacheStats {
	var expired int
	now := time.Now()

	for _, entry := range rc.entries {
		if now.After(entry.ExpiresAt) {
			expired++
		}
	}

	return CacheStats{
		Size:    len(rc.entries),
		MaxSize: rc.maxSize,
		Expired: expired,
		TTL:     rc.ttl,
	}
}

// CacheStats represents cache statistics
type CacheStats struct {
	Size    int           `json:"size"`
	MaxSize int           `json:"max_size"`
	Expired int           `json:"expired"`
	TTL     time.Duration `json:"ttl"`
}