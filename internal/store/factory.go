package store

import (
	"fmt"
	"os"
	"strings"
)

const (
	defaultStateBackend            = "memory"
	defaultQueueBackend            = "memory"
	defaultStorageStartupMode      = StorageStartupModeStrict
	storageStartupModeEnv          = "STATOCYST_STORAGE_STARTUP_MODE"
	storageStartupModeStrictRaw    = "strict"
	storageStartupModeDegradedRaw  = "degraded"
	storageStartupModeFallbackRaw  = "fallback-memory"
	storageStartupModeFallbackAlt1 = "fallback_memory"
	storageStartupModeFallbackAlt2 = "fallbackmemory"
)

type StorageStartupMode string

const (
	StorageStartupModeStrict   StorageStartupMode = "strict"
	StorageStartupModeDegraded StorageStartupMode = "degraded"
)

type StorageBackendHealth struct {
	Backend string
	Healthy bool
	Error   string
}

type StorageHealthStatus struct {
	StartupMode StorageStartupMode
	State       StorageBackendHealth
	Queue       StorageBackendHealth
}

func DefaultStorageHealthStatus() StorageHealthStatus {
	return StorageHealthStatus{
		StartupMode: defaultStorageStartupMode,
		State: StorageBackendHealth{
			Backend: defaultStateBackend,
			Healthy: true,
		},
		Queue: StorageBackendHealth{
			Backend: defaultQueueBackend,
			Healthy: true,
		},
	}
}

func (s StorageHealthStatus) OverallStatus() string {
	if s.State.Healthy && s.Queue.Healthy {
		return "ok"
	}
	return "degraded"
}

func ParseStorageStartupMode(raw string) (StorageStartupMode, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "", storageStartupModeStrictRaw:
		return StorageStartupModeStrict, nil
	case storageStartupModeDegradedRaw, storageStartupModeFallbackRaw, storageStartupModeFallbackAlt1, storageStartupModeFallbackAlt2:
		return StorageStartupModeDegraded, nil
	default:
		return "", fmt.Errorf("unsupported %s %q (expected strict or degraded)", storageStartupModeEnv, raw)
	}
}

func StorageStartupModeFromEnv() (StorageStartupMode, error) {
	return ParseStorageStartupMode(os.Getenv(storageStartupModeEnv))
}

func configuredBackendsFromEnv() (string, string, error) {
	stateBackend := strings.ToLower(strings.TrimSpace(os.Getenv("STATOCYST_STATE_BACKEND")))
	queueBackend := strings.ToLower(strings.TrimSpace(os.Getenv("STATOCYST_QUEUE_BACKEND")))
	if stateBackend == "" {
		stateBackend = defaultStateBackend
	}
	if queueBackend == "" {
		queueBackend = defaultQueueBackend
	}
	if stateBackend != "memory" && stateBackend != "s3" {
		return "", "", fmt.Errorf("unsupported state backend %q", stateBackend)
	}
	if queueBackend != "memory" && queueBackend != "s3" {
		return "", "", fmt.Errorf("unsupported queue backend %q", queueBackend)
	}
	return stateBackend, queueBackend, nil
}

// NewStoresFromEnv wires backend implementations from env configuration.
func NewStoresFromEnv() (ControlPlaneStore, MessageQueueStore, error) {
	control, queue, _, err := NewStoresFromEnvWithMode(StorageStartupModeStrict)
	return control, queue, err
}

// NewStoresFromEnvWithMode wires backend implementations from env configuration, optionally degrading to memory stores.
func NewStoresFromEnvWithMode(mode StorageStartupMode) (ControlPlaneStore, MessageQueueStore, StorageHealthStatus, error) {
	stateBackend, queueBackend, err := configuredBackendsFromEnv()
	if err != nil {
		return nil, nil, DefaultStorageHealthStatus(), err
	}
	if mode != StorageStartupModeStrict && mode != StorageStartupModeDegraded {
		return nil, nil, DefaultStorageHealthStatus(), fmt.Errorf("unsupported storage startup mode %q", mode)
	}

	health := StorageHealthStatus{
		StartupMode: mode,
		State: StorageBackendHealth{
			Backend: stateBackend,
			Healthy: true,
		},
		Queue: StorageBackendHealth{
			Backend: queueBackend,
			Healthy: true,
		},
	}

	mem := NewMemoryStore()
	var controlStore ControlPlaneStore = mem
	var stateQueueStore MessageQueueStore = mem

	if stateBackend == "s3" {
		state, stateErr := NewS3StateStoreFromEnv()
		if stateErr != nil {
			health.State.Healthy = false
			health.State.Error = stateErr.Error()
			if mode == StorageStartupModeStrict {
				return nil, nil, health, stateErr
			}
			controlStore = mem
			stateQueueStore = mem
		} else {
			controlStore = state
			stateQueueStore = state
		}
	}

	var queueStore MessageQueueStore
	if queueBackend == "s3" {
		if state, ok := controlStore.(*s3StateStore); ok && stateQueueStore == state {
			queueStore = state
		} else {
			queue, queueErr := NewS3QueueStoreFromEnv()
			if queueErr != nil {
				health.Queue.Healthy = false
				health.Queue.Error = queueErr.Error()
				if mode == StorageStartupModeStrict {
					return nil, nil, health, queueErr
				}
				queueStore = mem
			} else {
				queueStore = queue
			}
		}
	} else {
		queueStore = stateQueueStore
	}

	return controlStore, queueStore, health, nil
}
