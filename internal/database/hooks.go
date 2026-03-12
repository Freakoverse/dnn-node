package database

import (
	"log"
	"sync"

	"github.com/nbd-wtf/go-nostr"
)

// EventHook is an interface for hooks that react to event storage
type EventHook interface {
	// OnEventStored is called synchronously after an event is stored
	// kind is the Nostr event kind (61600, 62600, 63600, 60600)
	// Returns an error if the hook fails (logged but doesn't block storage)
	OnEventStored(event *nostr.Event, kind int) error

	// Name returns the hook name for logging
	Name() string
}

// HookManager manages event hooks
type HookManager struct {
	hooks []EventHook
	mu    sync.RWMutex
}

// NewHookManager creates a new hook manager
func NewHookManager() *HookManager {
	return &HookManager{
		hooks: make([]EventHook, 0),
	}
}

// Register adds a hook to the manager
func (hm *HookManager) Register(hook EventHook) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	hm.hooks = append(hm.hooks, hook)
	log.Printf("[HookManager] Registered hook: %s", hook.Name())
}

// TriggerOnEventStored calls all registered hooks synchronously
// Errors are logged but don't prevent other hooks from running
func (hm *HookManager) TriggerOnEventStored(event *nostr.Event, kind int) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()

	for _, hook := range hm.hooks {
		if err := hook.OnEventStored(event, kind); err != nil {
			log.Printf("[HookManager] Hook %s error: %v", hook.Name(), err)
		}
	}
}

// HookCount returns the number of registered hooks
func (hm *HookManager) HookCount() int {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	return len(hm.hooks)
}

// AnchorFetchHook fetches referenced events when an anchor is stored
// It uses a callback function to perform the actual fetch
type AnchorFetchHook struct {
	fetcher func(event *nostr.Event) error
}

// NewAnchorFetchHook creates a new anchor fetch hook
func NewAnchorFetchHook(fetcher func(event *nostr.Event) error) *AnchorFetchHook {
	return &AnchorFetchHook{fetcher: fetcher}
}

// Name returns the hook name
func (h *AnchorFetchHook) Name() string {
	return "AnchorFetchHook"
}

// OnEventStored is called when an event is stored
func (h *AnchorFetchHook) OnEventStored(event *nostr.Event, kind int) error {
	// Only handle anchor events (kind 60600)
	if kind != 60600 {
		return nil
	}
	return h.fetcher(event)
}
