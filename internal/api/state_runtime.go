package api

import (
	"errors"
	"strings"
	"time"

	"moltenhub/internal/model"
	"moltenhub/internal/store"
)

type stateMetadataBestEffortStore interface {
	UpdateAgentMetadataSelfBestEffort(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error)
}

type stateHandleBestEffortStore interface {
	FinalizeAgentHandleSelfBestEffort(agentUUID, handle string, now time.Time) (model.Agent, error)
}

func stateRuntimeFailureSummary(operation string, err error) string {
	base := strings.TrimSpace("state " + operation + " failed")
	detail := strings.TrimSpace(store.SanitizeError(err))
	if detail == "" {
		return base
	}
	return base + ": " + detail
}

func (h *Handler) runtimeFallbackEnabled() bool {
	return h.currentStorageHealth().StartupMode == store.StorageStartupModeDegraded
}

func (h *Handler) updateAgentMetadataSelfWithRuntimeFallback(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error) {
	agent, err := h.control.UpdateAgentMetadataSelf(agentUUID, metadata, now)
	if err == nil {
		h.clearStateRuntimeError()
		return agent, nil
	}
	if !isAgentMetadataClientError(err) {
		h.setStateRuntimeError(stateRuntimeFailureSummary("agent metadata update", err))
		if h.runtimeFallbackEnabled() {
			if bestEffort, ok := h.control.(stateMetadataBestEffortStore); ok {
				agent, fallbackErr := bestEffort.UpdateAgentMetadataSelfBestEffort(agentUUID, metadata, now)
				if fallbackErr == nil {
					return agent, nil
				}
				return model.Agent{}, fallbackErr
			}
		}
	}
	return model.Agent{}, err
}

func (h *Handler) finalizeAgentHandleSelfWithRuntimeFallback(agentUUID, handle string, now time.Time) (model.Agent, error) {
	agent, err := h.control.FinalizeAgentHandleSelf(agentUUID, handle, now)
	if err == nil {
		h.clearStateRuntimeError()
		return agent, nil
	}
	if !isAgentHandleClientError(err) {
		h.setStateRuntimeError(stateRuntimeFailureSummary("agent handle finalize", err))
		if h.runtimeFallbackEnabled() {
			if bestEffort, ok := h.control.(stateHandleBestEffortStore); ok {
				agent, fallbackErr := bestEffort.FinalizeAgentHandleSelfBestEffort(agentUUID, handle, now)
				if fallbackErr == nil {
					return agent, nil
				}
				return model.Agent{}, fallbackErr
			}
		}
	}
	return model.Agent{}, err
}

func isAgentMetadataClientError(err error) bool {
	return errors.Is(err, store.ErrAgentNotFound) ||
		errors.Is(err, store.ErrInvalidAgentType) ||
		errors.Is(err, store.ErrInvalidAgentSkills) ||
		errors.Is(err, store.ErrInvalidSkillDescription)
}

func isAgentHandleClientError(err error) bool {
	return errors.Is(err, store.ErrAgentNotFound) ||
		errors.Is(err, store.ErrInvalidHandle) ||
		errors.Is(err, store.ErrAgentExists) ||
		errors.Is(err, store.ErrAgentHandleLocked)
}
