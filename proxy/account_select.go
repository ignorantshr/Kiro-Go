package proxy

import (
	"kiro-go/config"
	"kiro-go/logger"
)

// selectAccountForRequest implements two-phase account selection with API Key binding.
// Phase 1 (binding): if the entry has bound accounts, try those first using a strict
// lookup (no cooldown fallback) so that cooling-down bound accounts don't prevent
// the global fallback from running.
// Phase 2 (global fallback): if binding phase returns nil and strictBinding is false,
// fall back to the global pool.
func (h *Handler) selectAccountForRequest(model string, excluded map[string]bool, entry *config.ApiKeyEntry) (acc *config.Account) {
	defer func() {
		if acc != nil {
			if entry != nil {
				logger.Debugf("Selected account %s for model %s (API key binding: %v)", acc.Email, model, entry.Name)
			} else {
				logger.Debugf("Selected account %s for model %s (no API key binding)", acc.Email, model)
			}
		} else {
			if entry != nil {
				logger.Debugf("No account available for model %s (API key binding: %v)", model, entry.Name)
			} else {
				logger.Debugf("No account available for model %s (no API key binding)", model)
			}
		}
	}()

	if entry == nil || len(entry.BoundAccountIDs) == 0 {
		return h.pool.GetNextForModelExcluding(model, excluded)
	}

	allowedIDs := make(map[string]bool, len(entry.BoundAccountIDs))
	for _, id := range entry.BoundAccountIDs {
		allowedIDs[id] = true
	}

	// Always use strict=true for the binding-phase lookup: we only want an
	// immediately-available bound account. If none is ready, we decide below
	// whether to fall back to global or fail.
	account := h.pool.GetNextForModelWithinExcluding(model, allowedIDs, excluded, true)
	if account != nil {
		return account
	}

	if entry.StrictBinding {
		return nil
	}

	return h.pool.GetNextForModelExcluding(model, excluded)
}
