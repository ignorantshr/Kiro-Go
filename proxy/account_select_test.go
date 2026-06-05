package proxy

import (
	"kiro-go/config"
	"kiro-go/pool"
	"path/filepath"
	"testing"
)

func newTestHandler(t *testing.T, accounts ...config.Account) *Handler {
	t.Helper()
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	for _, acc := range accounts {
		if err := config.AddAccount(acc); err != nil {
			t.Fatalf("AddAccount: %v", err)
		}
	}
	p := pool.GetPool()
	p.Reload()
	return &Handler{pool: p}
}

func TestSelectAccountForRequestNilEntry(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "a", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	h := &Handler{pool: p}

	acc := h.selectAccountForRequest("", nil, nil)
	if acc == nil {
		t.Fatal("expected global pool selection when entry is nil")
	}
}

func TestSelectAccountForRequestNoBoundAccounts(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "a", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	h := &Handler{pool: p}

	entry := &config.ApiKeyEntry{ID: "key-1", BoundAccountIDs: nil}
	acc := h.selectAccountForRequest("", nil, entry)
	if acc == nil {
		t.Fatal("expected global pool selection when no bound accounts")
	}
}

func TestSelectAccountForRequestPrefersBoundAccount(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "global", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "bound", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	h := &Handler{pool: p}

	entry := &config.ApiKeyEntry{
		ID:              "key-1",
		BoundAccountIDs: []string{"bound"},
	}
	acc := h.selectAccountForRequest("", nil, entry)
	if acc == nil || acc.ID != "bound" {
		t.Fatalf("expected bound account, got %v", acc)
	}
}

func TestSelectAccountForRequestStrictBindingFailsWhenBoundUnavailable(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "global", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "bound", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	h := &Handler{pool: p}

	// Exclude the bound account (simulates it failed in prior attempt)
	excluded := map[string]bool{"bound": true}
	entry := &config.ApiKeyEntry{
		ID:              "key-1",
		BoundAccountIDs: []string{"bound"},
		StrictBinding:   true,
	}
	acc := h.selectAccountForRequest("", excluded, entry)
	if acc != nil {
		t.Fatalf("strict binding should return nil when bound account excluded, got %q", acc.ID)
	}
}

func TestSelectAccountForRequestNonStrictFallsBackToGlobal(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "global", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "bound", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	h := &Handler{pool: p}

	excluded := map[string]bool{"bound": true}
	entry := &config.ApiKeyEntry{
		ID:              "key-1",
		BoundAccountIDs: []string{"bound"},
		StrictBinding:   false,
	}
	acc := h.selectAccountForRequest("", excluded, entry)
	if acc == nil {
		t.Fatal("non-strict should fall back to global pool")
	}
	if acc.ID != "global" {
		t.Fatalf("expected global account, got %q", acc.ID)
	}
}

func TestSelectAccountForRequestNonStrictFallsBackWhenBoundCoolingDown(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "bound", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "global", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	// Cool down the bound account
	p.RecordError("bound", true)
	h := &Handler{pool: p}

	entry := &config.ApiKeyEntry{
		ID:              "key-1",
		BoundAccountIDs: []string{"bound"},
		StrictBinding:   false,
	}
	acc := h.selectAccountForRequest("", nil, entry)
	if acc == nil {
		t.Fatal("non-strict should fall back to global when bound account is cooling down")
	}
	if acc.ID != "global" {
		t.Fatalf("expected global account, got %q", acc.ID)
	}
}

func TestSelectAccountForRequestExcludedSharedAcrossPhases(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	// "shared" is in both bound set and global pool
	if err := config.AddAccount(config.Account{ID: "shared", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "other", Enabled: true}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	p := pool.GetPool()
	p.Reload()
	h := &Handler{pool: p}

	// "shared" failed in binding phase, now excluded
	excluded := map[string]bool{"shared": true}
	entry := &config.ApiKeyEntry{
		ID:              "key-1",
		BoundAccountIDs: []string{"shared"},
		StrictBinding:   false,
	}
	acc := h.selectAccountForRequest("", excluded, entry)
	if acc == nil {
		t.Fatal("expected global fallback")
	}
	// Should not pick "shared" again in global phase
	if acc.ID == "shared" {
		t.Fatal("excluded account should not be selected in global fallback")
	}
}
