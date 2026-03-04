package store

import (
	"fmt"
	"os"
	"strings"
)

const (
	defaultStateBackend = "memory"
	defaultQueueBackend = "memory"
)

// NewStoresFromEnv wires backend implementations from env configuration.
func NewStoresFromEnv() (ControlPlaneStore, MessageQueueStore, error) {
	stateBackend := strings.ToLower(strings.TrimSpace(os.Getenv("STATOCYST_STATE_BACKEND")))
	queueBackend := strings.ToLower(strings.TrimSpace(os.Getenv("STATOCYST_QUEUE_BACKEND")))
	if stateBackend == "" {
		stateBackend = defaultStateBackend
	}
	if queueBackend == "" {
		queueBackend = defaultQueueBackend
	}

	switch {
	case stateBackend == "memory" && queueBackend == "memory":
		mem := NewMemoryStore()
		return mem, mem, nil
	case stateBackend != "memory":
		return nil, nil, fmt.Errorf("unsupported state backend %q", stateBackend)
	default:
		return nil, nil, fmt.Errorf("unsupported queue backend %q", queueBackend)
	}
}
