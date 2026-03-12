package database

import (
	"encoding/json"
	"strings"

	"github.com/nbd-wtf/go-nostr"
)

// QueryEvents queries events matching the given filter
func (d *Database) QueryEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	var events []*nostr.Event

	// Build SQL query based on filter
	// For DNN, we mainly care about kinds 61600, 62600, 63600, 60600

	// Query each event type
	if len(filter.Kinds) == 0 || containsKind(filter.Kinds, 61600) {
		nameEvents, err := d.queryNameEvents(filter)
		if err == nil {
			events = append(events, nameEvents...)
		}
	}

	if len(filter.Kinds) == 0 || containsKind(filter.Kinds, 62600) {
		connEvents, err := d.queryConnectionEvents(filter)
		if err == nil {
			events = append(events, connEvents...)
		}
	}

	if len(filter.Kinds) == 0 || containsKind(filter.Kinds, 63600) {
		metaEvents, err := d.queryMetadataEvents(filter)
		if err == nil {
			events = append(events, metaEvents...)
		}
	}

	if len(filter.Kinds) == 0 || containsKind(filter.Kinds, 60600) {
		anchorEvents, err := d.queryAnchorEvents(filter)
		if err == nil {
			events = append(events, anchorEvents...)
		}
	}

	// Apply limit
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}

	return events, nil
}

func (d *Database) queryNameEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	query := "SELECT id, pubkey, created_at, d_tag, primary_name, other_names, content, sig FROM name_events WHERE 1=1"
	var args []interface{}

	// Add filters
	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND id IN (" + placeholders + ")"
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}

	if len(filter.Authors) > 0 {
		placeholders := strings.Repeat("?,", len(filter.Authors))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND pubkey IN (" + placeholders + ")"
		for _, author := range filter.Authors {
			args = append(args, author)
		}
	}

	if filter.Since != nil {
		query += " AND created_at >= ?"
		args = append(args, int64(*filter.Since))
	}

	if filter.Until != nil {
		query += " AND created_at <= ?"
		args = append(args, int64(*filter.Until))
	}

	// Filter by d-tag if specified
	if dTags, ok := filter.Tags["d"]; ok && len(dTags) > 0 {
		placeholders := strings.Repeat("?,", len(dTags))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND d_tag IN (" + placeholders + ")"
		for _, dTag := range dTags {
			args = append(args, dTag)
		}
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*nostr.Event
	for rows.Next() {
		var id, pubkey, dTag, primaryName, otherNames, content, sig string
		var createdAt int64

		if err := rows.Scan(&id, &pubkey, &createdAt, &dTag, &primaryName, &otherNames, &content, &sig); err != nil {
			continue
		}

		// Reconstruct tags including the n tag (primary name)
		tags := nostr.Tags{{"t", "DNN"}, {"d", dTag}}

		// Add n tag with primary name
		if primaryName != "" {
			tags = append(tags, nostr.Tag{"n", primaryName})
		}

		// Add o tags for other names (if any)
		if otherNames != "" && otherNames != "[]" {
			// Parse JSON array of other names
			var otherNamesList []string
			if err := json.Unmarshal([]byte(otherNames), &otherNamesList); err == nil {
				for _, name := range otherNamesList {
					tags = append(tags, nostr.Tag{"o", name})
				}
			}
		}

		event := &nostr.Event{
			ID:        id,
			PubKey:    pubkey,
			CreatedAt: nostr.Timestamp(createdAt),
			Kind:      61600,
			Tags:      tags,
			Content:   content,
			Sig:       sig,
		}

		events = append(events, event)
	}

	return events, nil
}

func (d *Database) queryConnectionEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	query := "SELECT id, pubkey, created_at, d_tag, content, sig, COALESCE(tags_json, '[]') FROM connection_events WHERE 1=1"
	var args []interface{}

	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND id IN (" + placeholders + ")"
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}

	if len(filter.Authors) > 0 {
		placeholders := strings.Repeat("?,", len(filter.Authors))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND pubkey IN (" + placeholders + ")"
		for _, author := range filter.Authors {
			args = append(args, author)
		}
	}

	if filter.Since != nil {
		query += " AND created_at >= ?"
		args = append(args, int64(*filter.Since))
	}

	if filter.Until != nil {
		query += " AND created_at <= ?"
		args = append(args, int64(*filter.Until))
	}

	// Filter by d-tag if specified
	if dTags, ok := filter.Tags["d"]; ok && len(dTags) > 0 {
		placeholders := strings.Repeat("?,", len(dTags))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND d_tag IN (" + placeholders + ")"
		for _, dTag := range dTags {
			args = append(args, dTag)
		}
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*nostr.Event
	for rows.Next() {
		var id, pubkey, dTag, content, sig, tagsJSON string
		var createdAt int64

		if err := rows.Scan(&id, &pubkey, &createdAt, &dTag, &content, &sig, &tagsJSON); err != nil {
			continue
		}

		// Try to deserialize tags from JSON; fall back to basic tags if parsing fails
		var tags nostr.Tags
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil || len(tags) == 0 {
			// Fallback to basic tags for legacy events
			tags = nostr.Tags{{"t", "DNN"}}
			if dTag != "" {
				tags = append(tags, nostr.Tag{"d", dTag})
			}
		}

		event := &nostr.Event{
			ID:        id,
			PubKey:    pubkey,
			CreatedAt: nostr.Timestamp(createdAt),
			Kind:      62600,
			Tags:      tags,
			Content:   content,
			Sig:       sig,
		}

		events = append(events, event)
	}

	return events, nil
}

func (d *Database) queryMetadataEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	query := "SELECT id, pubkey, created_at, d_tag, content, sig FROM metadata_events WHERE 1=1"
	var args []interface{}

	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND id IN (" + placeholders + ")"
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}

	if len(filter.Authors) > 0 {
		placeholders := strings.Repeat("?,", len(filter.Authors))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND pubkey IN (" + placeholders + ")"
		for _, author := range filter.Authors {
			args = append(args, author)
		}
	}

	if filter.Since != nil {
		query += " AND created_at >= ?"
		args = append(args, int64(*filter.Since))
	}

	if filter.Until != nil {
		query += " AND created_at <= ?"
		args = append(args, int64(*filter.Until))
	}

	// Filter by d-tag if specified
	if dTags, ok := filter.Tags["d"]; ok && len(dTags) > 0 {
		placeholders := strings.Repeat("?,", len(dTags))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND d_tag IN (" + placeholders + ")"
		for _, dTag := range dTags {
			args = append(args, dTag)
		}
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*nostr.Event
	for rows.Next() {
		var id, pubkey, dTag, content, sig string
		var createdAt int64

		if err := rows.Scan(&id, &pubkey, &createdAt, &dTag, &content, &sig); err != nil {
			continue
		}

		tags := nostr.Tags{{"t", "DNN"}}
		if dTag != "" {
			tags = append(tags, nostr.Tag{"d", dTag})
		}

		event := &nostr.Event{
			ID:        id,
			PubKey:    pubkey,
			CreatedAt: nostr.Timestamp(createdAt),
			Kind:      63600,
			Tags:      tags,
			Content:   content,
			Sig:       sig,
		}

		events = append(events, event)
	}

	return events, nil
}

func (d *Database) queryAnchorEvents(filter nostr.Filter) ([]*nostr.Event, error) {
	query := "SELECT id, pubkey, created_at, d_tag, name_event_ref, connection_event_ref, metadata_event_ref, transaction_id, content, sig, COALESCE(tags_json, '[]') FROM anchor_events WHERE 1=1"
	var args []interface{}

	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND id IN (" + placeholders + ")"
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}

	if len(filter.Authors) > 0 {
		placeholders := strings.Repeat("?,", len(filter.Authors))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND pubkey IN (" + placeholders + ")"
		for _, author := range filter.Authors {
			args = append(args, author)
		}
	}

	// Filter by d-tag if specified
	if dTags, ok := filter.Tags["d"]; ok && len(dTags) > 0 {
		placeholders := strings.Repeat("?,", len(dTags))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND d_tag IN (" + placeholders + ")"
		for _, dTag := range dTags {
			args = append(args, dTag)
		}
	}

	// For addressable replaceable events, we need only the latest version per pubkey+d_tag
	// Use a subquery approach: select events where (pubkey, d_tag, created_at) matches the max
	query += " AND (pubkey, d_tag, created_at) IN (SELECT pubkey, d_tag, MAX(created_at) FROM anchor_events GROUP BY pubkey, d_tag)"
	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*nostr.Event
	for rows.Next() {
		var id, pubkey, dTag, nameEventRef, connEventRef, metaEventRef, txID, content, sig, tagsJSON string
		var createdAt int64

		if err := rows.Scan(&id, &pubkey, &createdAt, &dTag, &nameEventRef, &connEventRef, &metaEventRef, &txID, &content, &sig, &tagsJSON); err != nil {
			continue
		}

		// Use original tags from JSON if available
		var tags nostr.Tags
		if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil || len(tags) == 0 {
			// Fallback for legacy rows without tags_json (should not happen after DB wipe)
			tags = nostr.Tags{
				{"d", dTag},
				{"n", nameEventRef},
				{"c", connEventRef},
				{"m", metaEventRef},
				{"x", txID},
				{"t", "DNN"},
			}
		}

		event := &nostr.Event{
			ID:        id,
			PubKey:    pubkey,
			CreatedAt: nostr.Timestamp(createdAt),
			Kind:      60600,
			Tags:      tags, // Use original tags!
			Content:   content,
			Sig:       sig,
		}

		events = append(events, event)
	}

	return events, nil
}

func containsKind(kinds []int, kind int) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// =============================================================================
// Rate Limit Queries
// =============================================================================

// CountEventVersions counts how many versions of a specific d-tag exist for a kind and npub
func (d *Database) CountEventVersions(pubkey string, kind int, dTag string) (int, error) {
	table := kindToTable(kind)
	if table == "" {
		return 0, nil
	}

	query := "SELECT COUNT(*) FROM " + table + " WHERE pubkey = ? AND d_tag = ?"
	var count int
	err := d.db.QueryRow(query, pubkey, dTag).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CountDTagsForKind counts how many unique d-tags exist for a kind and npub
func (d *Database) CountDTagsForKind(pubkey string, kind int) (int, error) {
	table := kindToTable(kind)
	if table == "" {
		return 0, nil
	}

	query := "SELECT COUNT(DISTINCT d_tag) FROM " + table + " WHERE pubkey = ?"
	var count int
	err := d.db.QueryRow(query, pubkey).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetOldestEventVersion returns the event ID of the oldest version for a d-tag
func (d *Database) GetOldestEventVersion(pubkey string, kind int, dTag string) (string, error) {
	table := kindToTable(kind)
	if table == "" {
		return "", nil
	}

	query := "SELECT id FROM " + table + " WHERE pubkey = ? AND d_tag = ? ORDER BY created_at ASC LIMIT 1"
	var id string
	err := d.db.QueryRow(query, pubkey, dTag).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetOldestDTagForKind returns the d-tag with the oldest event for a kind and npub
func (d *Database) GetOldestDTagForKind(pubkey string, kind int) (string, error) {
	table := kindToTable(kind)
	if table == "" {
		return "", nil
	}

	// Get the d-tag whose most recent event is the oldest among all d-tags
	query := `
		SELECT d_tag FROM ` + table + ` 
		WHERE pubkey = ? 
		GROUP BY d_tag 
		ORDER BY MAX(created_at) ASC 
		LIMIT 1
	`
	var dTag string
	err := d.db.QueryRow(query, pubkey).Scan(&dTag)
	if err != nil {
		return "", err
	}
	return dTag, nil
}

// DeleteEventByID deletes an event by its ID from the appropriate table
func (d *Database) DeleteEventByID(kind int, eventID string) error {
	table := kindToTable(kind)
	if table == "" {
		return nil
	}

	query := "DELETE FROM " + table + " WHERE id = ?"
	_, err := d.db.Exec(query, eventID)
	return err
}

// DeleteEventsByDTag deletes all events with a specific d-tag for a pubkey and kind
func (d *Database) DeleteEventsByDTag(pubkey string, kind int, dTag string) error {
	table := kindToTable(kind)
	if table == "" {
		return nil
	}

	query := "DELETE FROM " + table + " WHERE pubkey = ? AND d_tag = ?"
	_, err := d.db.Exec(query, pubkey, dTag)
	return err
}

// IsDTagReferencedByAnchor checks if a d-tag for a specific kind is referenced by any anchor event
// This is used to prevent deleting events that are still referenced by anchors
func (d *Database) IsDTagReferencedByAnchor(pubkey string, kind int, dTag string) (bool, error) {
	// Anchors use n, c, m tags to reference other events
	// The tag value contains an naddr which includes the d-tag
	// We check if any anchor for this pubkey has a reference containing this d-tag

	var refColumn string
	switch kind {
	case 61600:
		refColumn = "name_event_ref"
	case 62600:
		refColumn = "connection_event_ref"
	case 63600:
		refColumn = "metadata_event_ref"
	default:
		return false, nil // Only these kinds are referenced by anchors
	}

	// Check if any anchor references an event with this d-tag
	// The ref column stores naddr which contains the d-tag
	query := "SELECT COUNT(*) FROM anchor_events WHERE pubkey = ? AND " + refColumn + " LIKE ?"
	var count int
	err := d.db.QueryRow(query, pubkey, "%"+dTag+"%").Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// kindToTable maps event kinds to their database tables
func kindToTable(kind int) string {
	switch kind {
	case 60600:
		return "anchor_events"
	case 61600:
		return "name_events"
	case 62600:
		return "connection_events"
	case 63600:
		return "metadata_events"
	default:
		return ""
	}
}
