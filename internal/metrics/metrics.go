package metrics

import (
	"encoding/json"
	"sync"
	"time"
)

// Metrics tracks node performance and statistics
type Metrics struct {
	mu sync.RWMutex

	// Counters
	EventsReceived   int64 `json:"events_received"`
	EventsPublished  int64 `json:"events_published"`
	EventsRejected   int64 `json:"events_rejected"`
	NamesResolved    int64 `json:"names_resolved"`
	ResolutionErrors int64 `json:"resolution_errors"`
	PeersConnected   int64 `json:"peers_connected"`
	PeersDisconnected int64 `json:"peers_disconnected"`
	BlocksSynced     int64 `json:"blocks_synced"`
	
	// Gauges
	CurrentConnections int   `json:"current_connections"`
	CurrentPeers       int   `json:"current_peers"`
	DatabaseSize       int64 `json:"database_size"`
	CacheHits          int64 `json:"cache_hits"`
	CacheMisses        int64 `json:"cache_misses"`
	
	// Timing
	AverageResolutionTime time.Duration `json:"average_resolution_time"`
	AverageSyncTime       time.Duration `json:"average_sync_time"`
	LastSyncTime          time.Time     `json:"last_sync_time"`
	
	// Histograms
	resolutionTimes []time.Duration
	syncTimes       []time.Duration
	
	// Start time
	StartTime time.Time `json:"start_time"`
}

// NewMetrics creates a new metrics tracker
func NewMetrics() *Metrics {
	return &Metrics{
		StartTime:       time.Now(),
		resolutionTimes: make([]time.Duration, 0, 1000),
		syncTimes:       make([]time.Duration, 0, 100),
	}
}

// IncrementEventsReceived increments the events received counter
func (m *Metrics) IncrementEventsReceived() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EventsReceived++
}

// IncrementEventsPublished increments the events published counter
func (m *Metrics) IncrementEventsPublished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EventsPublished++
}

// IncrementEventsRejected increments the events rejected counter
func (m *Metrics) IncrementEventsRejected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EventsRejected++
}

// IncrementNamesResolved increments the names resolved counter
func (m *Metrics) IncrementNamesResolved() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NamesResolved++
}

// IncrementResolutionErrors increments the resolution errors counter
func (m *Metrics) IncrementResolutionErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ResolutionErrors++
}

// IncrementCacheHit increments the cache hit counter
func (m *Metrics) IncrementCacheHit() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CacheHits++
}

// IncrementCacheMiss increments the cache miss counter
func (m *Metrics) IncrementCacheMiss() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CacheMisses++
}

// UpdateConnectionCount updates the current connection count
func (m *Metrics) UpdateConnectionCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CurrentConnections = count
}

// UpdatePeerCount updates the current peer count
func (m *Metrics) UpdatePeerCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CurrentPeers = count
}

// UpdateDatabaseSize updates the database size
func (m *Metrics) UpdateDatabaseSize(size int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DatabaseSize = size
}

// RecordResolutionTime records a name resolution time
func (m *Metrics) RecordResolutionTime(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.resolutionTimes = append(m.resolutionTimes, duration)
	
	// Keep only last 1000 measurements
	if len(m.resolutionTimes) > 1000 {
		m.resolutionTimes = m.resolutionTimes[len(m.resolutionTimes)-1000:]
	}
	
	// Update average
	m.AverageResolutionTime = m.calculateAverage(m.resolutionTimes)
}

// RecordSyncTime records a sync operation time
func (m *Metrics) RecordSyncTime(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.syncTimes = append(m.syncTimes, duration)
	m.LastSyncTime = time.Now()
	
	// Keep only last 100 measurements
	if len(m.syncTimes) > 100 {
		m.syncTimes = m.syncTimes[len(m.syncTimes)-100:]
	}
	
	// Update average
	m.AverageSyncTime = m.calculateAverage(m.syncTimes)
}

// IncrementBlocksSynced increments the blocks synced counter
func (m *Metrics) IncrementBlocksSynced() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BlocksSynced++
}

// PeerConnected records a peer connection
func (m *Metrics) PeerConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PeersConnected++
	m.CurrentPeers++
}

// PeerDisconnected records a peer disconnection
func (m *Metrics) PeerDisconnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PeersDisconnected++
	if m.CurrentPeers > 0 {
		m.CurrentPeers--
	}
}

// calculateAverage calculates the average of a slice of durations
func (m *Metrics) calculateAverage(times []time.Duration) time.Duration {
	if len(times) == 0 {
		return 0
	}
	
	var total time.Duration
	for _, t := range times {
		total += t
	}
	
	return total / time.Duration(len(times))
}

// GetSnapshot returns a snapshot of current metrics
func (m *Metrics) GetSnapshot() *MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	uptime := time.Since(m.StartTime)
	
	// Calculate rates
	uptimeSeconds := uptime.Seconds()
	eventsPerSecond := float64(0)
	resolutionsPerSecond := float64(0)
	
	if uptimeSeconds > 0 {
		eventsPerSecond = float64(m.EventsReceived) / uptimeSeconds
		resolutionsPerSecond = float64(m.NamesResolved) / uptimeSeconds
	}
	
	// Calculate cache hit rate
	totalCacheOps := m.CacheHits + m.CacheMisses
	cacheHitRate := float64(0)
	if totalCacheOps > 0 {
		cacheHitRate = float64(m.CacheHits) / float64(totalCacheOps) * 100
	}
	
	return &MetricsSnapshot{
		Timestamp:              time.Now(),
		Uptime:                 uptime,
		EventsReceived:         m.EventsReceived,
		EventsPublished:        m.EventsPublished,
		EventsRejected:         m.EventsRejected,
		NamesResolved:          m.NamesResolved,
		ResolutionErrors:       m.ResolutionErrors,
		CurrentConnections:     m.CurrentConnections,
		CurrentPeers:           m.CurrentPeers,
		DatabaseSize:           m.DatabaseSize,
		CacheHits:              m.CacheHits,
		CacheMisses:            m.CacheMisses,
		CacheHitRate:           cacheHitRate,
		AverageResolutionTime:  m.AverageResolutionTime,
		AverageSyncTime:        m.AverageSyncTime,
		LastSyncTime:           m.LastSyncTime,
		BlocksSynced:           m.BlocksSynced,
		EventsPerSecond:        eventsPerSecond,
		ResolutionsPerSecond:   resolutionsPerSecond,
		PeersConnected:         m.PeersConnected,
		PeersDisconnected:      m.PeersDisconnected,
	}
}

// MetricsSnapshot represents a point-in-time snapshot of metrics
type MetricsSnapshot struct {
	Timestamp              time.Time     `json:"timestamp"`
	Uptime                 time.Duration `json:"uptime"`
	EventsReceived         int64         `json:"events_received"`
	EventsPublished        int64         `json:"events_published"`
	EventsRejected         int64         `json:"events_rejected"`
	NamesResolved          int64         `json:"names_resolved"`
	ResolutionErrors       int64         `json:"resolution_errors"`
	CurrentConnections     int           `json:"current_connections"`
	CurrentPeers           int           `json:"current_peers"`
	DatabaseSize           int64         `json:"database_size"`
	CacheHits              int64         `json:"cache_hits"`
	CacheMisses            int64         `json:"cache_misses"`
	CacheHitRate           float64       `json:"cache_hit_rate"`
	AverageResolutionTime  time.Duration `json:"average_resolution_time"`
	AverageSyncTime        time.Duration `json:"average_sync_time"`
	LastSyncTime           time.Time     `json:"last_sync_time"`
	BlocksSynced           int64         `json:"blocks_synced"`
	EventsPerSecond        float64       `json:"events_per_second"`
	ResolutionsPerSecond   float64       `json:"resolutions_per_second"`
	PeersConnected         int64         `json:"peers_connected"`
	PeersDisconnected      int64         `json:"peers_disconnected"`
}

// MarshalJSON implements json.Marshaler for MetricsSnapshot
func (ms *MetricsSnapshot) MarshalJSON() ([]byte, error) {
	type Alias MetricsSnapshot
	return json.Marshal(&struct {
		*Alias
		Uptime                string `json:"uptime"`
		AverageResolutionTime string `json:"average_resolution_time"`
		AverageSyncTime       string `json:"average_sync_time"`
	}{
		Alias:                 (*Alias)(ms),
		Uptime:                ms.Uptime.String(),
		AverageResolutionTime: ms.AverageResolutionTime.String(),
		AverageSyncTime:       ms.AverageSyncTime.String(),
	})
}

// Reset resets all metrics
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.EventsReceived = 0
	m.EventsPublished = 0
	m.EventsRejected = 0
	m.NamesResolved = 0
	m.ResolutionErrors = 0
	m.PeersConnected = 0
	m.PeersDisconnected = 0
	m.BlocksSynced = 0
	m.CacheHits = 0
	m.CacheMisses = 0
	m.resolutionTimes = make([]time.Duration, 0, 1000)
	m.syncTimes = make([]time.Duration, 0, 100)
	m.StartTime = time.Now()
}