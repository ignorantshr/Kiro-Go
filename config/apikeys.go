package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// cloneApiKeyEntry returns a deep copy of the entry, duplicating the
// BoundAccountIDs slice so the caller cannot mutate the global config.
func cloneApiKeyEntry(e ApiKeyEntry) ApiKeyEntry {
	if len(e.BoundAccountIDs) > 0 {
		cp := make([]string, len(e.BoundAccountIDs))
		copy(cp, e.BoundAccountIDs)
		e.BoundAccountIDs = cp
	}
	return e
}

// ListApiKeys returns a snapshot of all configured API key entries.
func ListApiKeys() []ApiKeyEntry {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	out := make([]ApiKeyEntry, len(cfg.ApiKeys))
	for i := range cfg.ApiKeys {
		out[i] = cloneApiKeyEntry(cfg.ApiKeys[i])
	}
	return out
}

// GetApiKeyEntry returns a deep copy of the entry with the given ID, or nil if not found.
func GetApiKeyEntry(id string) *ApiKeyEntry {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return nil
	}
	for i := range cfg.ApiKeys {
		if cfg.ApiKeys[i].ID == id {
			cp := cloneApiKeyEntry(cfg.ApiKeys[i])
			return &cp
		}
	}
	return nil
}

// normalizeBindingLocked validates and deduplicates BoundAccountIDs against cfg.Accounts.
// Must be called with cfgLock already held.
func normalizeBindingLocked(boundIDs []string, strict bool) ([]string, bool, error) {
	if len(boundIDs) == 0 {
		if strict {
			return nil, false, errors.New("strictBinding requires at least one bound account")
		}
		return nil, false, nil
	}
	accountExists := make(map[string]bool, len(cfg.Accounts))
	for i := range cfg.Accounts {
		accountExists[cfg.Accounts[i].ID] = true
	}
	seen := make(map[string]bool, len(boundIDs))
	var result []string
	for _, id := range boundIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		if !accountExists[id] {
			return nil, false, errors.New("bound account not found: " + id)
		}
		seen[id] = true
		result = append(result, id)
	}
	if len(result) == 0 {
		if strict {
			return nil, false, errors.New("strictBinding requires at least one bound account")
		}
		return nil, false, nil
	}
	return result, strict, nil
}

// AddApiKey appends a new API key entry. Generates ID and CreatedAt if missing,
// rejects empty Key values, and refuses duplicates of an existing Key.
func AddApiKey(entry ApiKeyEntry) (ApiKeyEntry, error) {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return ApiKeyEntry{}, errors.New("config not initialized")
	}
	entry.Key = strings.TrimSpace(entry.Key)
	if entry.Key == "" {
		return ApiKeyEntry{}, errors.New("api key value must not be empty")
	}
	for _, existing := range cfg.ApiKeys {
		if existing.Key == entry.Key {
			return ApiKeyEntry{}, errors.New("api key already exists")
		}
	}
	normalized, strict, err := normalizeBindingLocked(entry.BoundAccountIDs, entry.StrictBinding)
	if err != nil {
		return ApiKeyEntry{}, err
	}
	entry.BoundAccountIDs = normalized
	entry.StrictBinding = strict
	if entry.ID == "" {
		entry.ID = newUUID()
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().Unix()
	}
	cfg.ApiKeys = append(cfg.ApiKeys, entry)
	if err := saveLocked(); err != nil {
		// Roll back the in-memory append so we don't leave inconsistent state.
		cfg.ApiKeys = cfg.ApiKeys[:len(cfg.ApiKeys)-1]
		return ApiKeyEntry{}, err
	}
	return entry, nil
}

// ApiKeyBindingPatch holds optional binding-field overrides for UpdateApiKeyFull.
// nil pointers mean "keep existing value".
type ApiKeyBindingPatch struct {
	BoundAccountIDs *[]string // nil = keep; non-nil = overwrite (empty slice = clear)
	StrictBinding   *bool     // nil = keep; non-nil = overwrite
}

// UpdateApiKey applies a patch to an existing API key (without binding changes).
// Delegates to UpdateApiKeyFull with nil binding patch.
func UpdateApiKey(id string, patch ApiKeyEntry) error {
	return UpdateApiKeyFull(id, patch, nil)
}

// UpdateApiKeyFull applies a patch to an existing API key atomically (single save).
// Patch semantics for scalar fields:
//   - Name, Key are overwritten when non-empty in patch.
//   - Enabled, TokenLimit, CreditLimit are always overwritten (zero values are valid).
//   - Counters are not touched here.
//   - Migrated stays as-is once true; only flips when explicitly set in patch.
//
// Binding patch (when non-nil):
//   - BoundAccountIDs: nil=keep, non-nil=overwrite (empty=clear)
//   - StrictBinding: nil=keep, non-nil=overwrite
//   - Clearing BoundAccountIDs forces StrictBinding=false
//   - Explicitly setting BoundAccountIDs=[] + StrictBinding=true returns error
func UpdateApiKeyFull(id string, patch ApiKeyEntry, binding *ApiKeyBindingPatch) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	idx := -1
	for i := range cfg.ApiKeys {
		if cfg.ApiKeys[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("api key not found")
	}
	if patch.Name != "" {
		cfg.ApiKeys[idx].Name = patch.Name
	}
	if patch.Key != "" {
		newKey := strings.TrimSpace(patch.Key)
		for j := range cfg.ApiKeys {
			if j != idx && cfg.ApiKeys[j].Key == newKey {
				return errors.New("api key value collides with existing entry")
			}
		}
		cfg.ApiKeys[idx].Key = newKey
	}
	cfg.ApiKeys[idx].Enabled = patch.Enabled
	cfg.ApiKeys[idx].TokenLimit = patch.TokenLimit
	cfg.ApiKeys[idx].CreditLimit = patch.CreditLimit
	if patch.Migrated {
		cfg.ApiKeys[idx].Migrated = true
	}

	if binding != nil {
		boundIDs := cfg.ApiKeys[idx].BoundAccountIDs
		strict := cfg.ApiKeys[idx].StrictBinding
		boundIDsExplicit := binding.BoundAccountIDs != nil
		strictExplicit := binding.StrictBinding != nil
		if boundIDsExplicit {
			boundIDs = *binding.BoundAccountIDs
		}
		if strictExplicit {
			strict = *binding.StrictBinding
		}
		// If clearing binding explicitly and strict was not explicitly set in this request,
		// force strict=false (per spec: clearing binding auto-clears strict).
		if boundIDsExplicit && len(boundIDs) == 0 && !strictExplicit {
			strict = false
		}
		normalized, finalStrict, err := normalizeBindingLocked(boundIDs, strict)
		if err != nil {
			return err
		}
		cfg.ApiKeys[idx].BoundAccountIDs = normalized
		cfg.ApiKeys[idx].StrictBinding = finalStrict
	}

	return saveLocked()
}

// DeleteApiKey removes the API key entry with the given ID. Returns nil even if
// the ID is unknown (idempotent), matching the existing DeleteAccount style.
func DeleteApiKey(id string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	for i, e := range cfg.ApiKeys {
		if e.ID == id {
			cfg.ApiKeys = append(cfg.ApiKeys[:i], cfg.ApiKeys[i+1:]...)
			return saveLocked()
		}
	}
	return nil
}

// FindApiKeyByValue returns a deep copy of the entry whose Key matches the given value,
// or nil if no match. O(n) linear scan.
func FindApiKeyByValue(key string) *ApiKeyEntry {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil || key == "" {
		return nil
	}
	for i := range cfg.ApiKeys {
		if cfg.ApiKeys[i].Key == key {
			cp := cloneApiKeyEntry(cfg.ApiKeys[i])
			return &cp
		}
	}
	return nil
}

// HasApiKeys returns true when at least one API key entry is configured.
func HasApiKeys() bool {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return false
	}
	return len(cfg.ApiKeys) > 0
}

// RecordApiKeyUsage atomically adds tokens and credits to the entry's counters,
// updates LastUsedAt, increments RequestsCount, and persists.
func RecordApiKeyUsage(id string, tokens int64, credits float64) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	for i := range cfg.ApiKeys {
		if cfg.ApiKeys[i].ID == id {
			if tokens > 0 {
				cfg.ApiKeys[i].TokensUsed += tokens
			}
			if credits > 0 {
				cfg.ApiKeys[i].CreditsUsed += credits
			}
			cfg.ApiKeys[i].RequestsCount++
			cfg.ApiKeys[i].LastUsedAt = time.Now().Unix()
			return saveLocked()
		}
	}
	return errors.New("api key not found")
}

// ResetApiKeyUsage clears TokensUsed/CreditsUsed/RequestsCount for the entry.
// LastUsedAt is preserved so operators can still see when the key was last used.
func ResetApiKeyUsage(id string) error {
	cfgLock.Lock()
	defer cfgLock.Unlock()
	if cfg == nil {
		return errors.New("config not initialized")
	}
	for i := range cfg.ApiKeys {
		if cfg.ApiKeys[i].ID == id {
			cfg.ApiKeys[i].TokensUsed = 0
			cfg.ApiKeys[i].CreditsUsed = 0
			cfg.ApiKeys[i].RequestsCount = 0
			return saveLocked()
		}
	}
	return errors.New("api key not found")
}

// GenerateApiKeyValue returns a new random 32-byte hex API key prefixed with "sk-".
func GenerateApiKeyValue() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return "sk-" + hex.EncodeToString(buf)
}

// MaskApiKey produces a display-friendly masked version: keeps first 6 and last 4
// characters, replaces the middle with "****". Returns "" for empty input and
// the original string if it's too short to mask meaningfully.
func MaskApiKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 10 {
		return key
	}
	return key[:6] + "****" + key[len(key)-4:]
}

// ApiKeyOverLimit returns (overToken, overCredit) for the entry. Limits with value 0
// are ignored. The function does not lock; callers should pass a copied entry.
func ApiKeyOverLimit(e ApiKeyEntry) (overToken bool, overCredit bool) {
	if e.TokenLimit > 0 && e.TokensUsed >= e.TokenLimit {
		overToken = true
	}
	if e.CreditLimit > 0 && e.CreditsUsed >= e.CreditLimit {
		overCredit = true
	}
	return
}
