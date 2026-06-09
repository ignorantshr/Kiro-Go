package pool

import (
	"errors"
	"kiro-go/config"
	"path/filepath"
	"testing"
	"time"
)

func TestOverLimitAccountsAreSkippedByDefault(t *testing.T) {
	p := &AccountPool{}
	normal := config.Account{ID: "normal"}
	overLimit := config.Account{ID: "over", UsageCurrent: 10, UsageLimit: 10}

	p.accounts = []config.Account{normal, overLimit}

	for i := 0; i < 5; i++ {
		acc := p.GetNext()
		if acc == nil {
			t.Fatalf("expected an account")
		}
		if acc.ID == "over" {
			t.Fatalf("expected over-limit account to be skipped when upstream OverageStatus is empty")
		}
	}
}

func TestOverLimitAccountsCanBeSelectedWhenUpstreamOverageEnabled(t *testing.T) {
	p := &AccountPool{}
	overLimit := config.Account{
		ID:            "over",
		UsageCurrent:  10,
		UsageLimit:    10,
		OverageStatus: "ENABLED",
	}

	p.accounts = []config.Account{overLimit}

	acc := p.GetNext()
	if acc == nil {
		t.Fatalf("expected upstream-enabled overage account to be selectable")
	}
	if acc.ID != "over" {
		t.Fatalf("expected overage account, got %q", acc.ID)
	}
}

func TestOverLimitAccountsRemainSkippedWhenUpstreamOverageDisabled(t *testing.T) {
	p := &AccountPool{}
	overLimit := config.Account{
		ID:            "over",
		UsageCurrent:  10,
		UsageLimit:    10,
		OverageStatus: "DISABLED",
	}

	p.accounts = []config.Account{overLimit}

	if acc := p.GetNext(); acc != nil {
		t.Fatalf("expected nil when upstream OverageStatus=DISABLED, got %q", acc.ID)
	}
}

func TestGetNextKeepsFiveMinuteTokenAvailable(t *testing.T) {
	p := &AccountPool{}
	account := config.Account{
		ID:          "acct-1",
		AccessToken: "access-token",
		ExpiresAt:   time.Now().Unix() + 300,
	}

	p.accounts = []config.Account{account}

	got := p.GetNext()
	if got == nil {
		t.Fatalf("expected five-minute token to be available")
	}
	if got.ID != account.ID {
		t.Fatalf("expected account %q, got %q", account.ID, got.ID)
	}
}

// TestGetNextKeepsNearExpiryTokenAvailable locks the post-refactor rule: the
// selection layer no longer skips near-expiry tokens. A token 60s from expiry
// (well inside the old 120s skew window that used to filter it out) must still
// be selectable — the synchronous ensureValidToken at request time is the sole
// refresh gate now.
func TestGetNextKeepsNearExpiryTokenAvailable(t *testing.T) {
	p := &AccountPool{}
	account := config.Account{
		ID:          "acct-near-expiry",
		AccessToken: "access-token",
		ExpiresAt:   time.Now().Unix() + 60,
	}

	p.accounts = []config.Account{account}

	got := p.GetNext()
	if got == nil {
		t.Fatal("expected near-expiry token to remain selectable")
	}
	if got.ID != account.ID {
		t.Fatalf("expected account %q, got %q", account.ID, got.ID)
	}
}

// TestSelectAccountReturnsCopyNotPoolPointer guards the concurrency fix: every
// selectAccount return path must hand back a copy, never a pointer into the
// pool's internal slice. Otherwise a concurrent UpdateToken (background refresh
// or request-time refresh) races the request goroutine reading the same fields.
func TestSelectAccountReturnsCopyNotPoolPointer(t *testing.T) {
	// Fast path: an immediately selectable account.
	p := newTestPool(config.Account{ID: "a", AccessToken: "old"})
	got := p.GetNext()
	if got == nil {
		t.Fatal("expected an account from fast path")
	}
	if got == &p.accounts[0] {
		t.Fatal("fast path leaked pool-internal pointer")
	}
	p.UpdateToken("a", "new", "", 0, "")
	if got.AccessToken != "old" {
		t.Fatalf("returned copy mutated by UpdateToken: got %q", got.AccessToken)
	}

	// Cooldown fallback path: the only account is cooling down; non-strict
	// callers still get it back — and it must also be a copy.
	p2 := newTestPool(config.Account{ID: "b", AccessToken: "old"})
	p2.RecordError("b", true) // 1h cooldown
	fb := p2.GetNextWithinExcluding(map[string]bool{"b": true}, nil, false)
	if fb == nil {
		t.Fatal("expected cooldown fallback account")
	}
	if fb == &p2.accounts[0] {
		t.Fatal("cooldown fallback leaked pool-internal pointer")
	}
	p2.UpdateToken("b", "new", "", 0, "")
	if fb.AccessToken != "old" {
		t.Fatalf("fallback copy mutated by UpdateToken: got %q", fb.AccessToken)
	}
}

// TestUpdateProfileArnUpdatesPoolCache verifies the runtime ARN cache write-back
// that compensates for selectAccount returning copies: ResolveProfileArn must be
// able to push a resolved ARN into the pool so later selections see it.
func TestUpdateProfileArnUpdatesPoolCache(t *testing.T) {
	p := newTestPool(config.Account{ID: "a"})
	p.UpdateProfileArn("a", "arn:test")
	got := p.GetByID("a")
	if got == nil || got.ProfileArn != "arn:test" {
		t.Fatalf("expected pool ProfileArn cache updated, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// IsAuthFailure
// ---------------------------------------------------------------------------

func TestIsAuthFailureRecognizes401And403(t *testing.T) {
	positives := []string{
		"HTTP 401 from server",
		"received 403 Forbidden",
		"bad credentials",
		"invalid_grant",
		"invalid_token",
		"token expired",
		"token has expired",
		"unauthorized",
	}
	for _, msg := range positives {
		if !IsAuthFailure(errors.New(msg)) {
			t.Errorf("IsAuthFailure(%q) = false, want true", msg)
		}
	}
}

func TestIsAuthFailureIgnoresFalsePositives(t *testing.T) {
	// hasStatusToken only excludes digit boundaries; e.g. "4011" contains "401"
	// but the trailing '1' is a digit so it does NOT match.
	negatives := []string{
		"status code 4011 found", // digit immediately after 401 → not a standalone token
		"error 14013 exceeded",   // digit before and after 401
		"some random error",
		"status 200 OK",
	}
	for _, msg := range negatives {
		if IsAuthFailure(errors.New(msg)) {
			t.Errorf("IsAuthFailure(%q) = true, want false", msg)
		}
	}
}

func TestIsAuthFailureNilError(t *testing.T) {
	if IsAuthFailure(nil) {
		t.Fatal("IsAuthFailure(nil) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// IsSuspensionError
// ---------------------------------------------------------------------------

func TestIsSuspensionErrorDetectsKnownMessages(t *testing.T) {
	positives := []string{
		"account temporarily_suspended",
		"account temporarily suspended",
		"no available kiro profile",
		"No Available Kiro Profile", // case-insensitive
	}
	for _, msg := range positives {
		if !IsSuspensionError(errors.New(msg)) {
			t.Errorf("IsSuspensionError(%q) = false, want true", msg)
		}
	}
}

func TestIsSuspensionErrorIgnoresUnrelatedErrors(t *testing.T) {
	negatives := []string{
		"some other error",
		"unauthorized",
		"429 too many requests",
	}
	for _, msg := range negatives {
		if IsSuspensionError(errors.New(msg)) {
			t.Errorf("IsSuspensionError(%q) = true, want false", msg)
		}
	}
}

func TestIsSuspensionErrorNilError(t *testing.T) {
	if IsSuspensionError(nil) {
		t.Fatal("IsSuspensionError(nil) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// GetNextForModelExcluding
// ---------------------------------------------------------------------------

func newTestPool(accounts ...config.Account) *AccountPool {
	p := &AccountPool{
		cooldowns:   make(map[string]time.Time),
		errorCounts: make(map[string]int),
		modelLists:  make(map[string]map[string]bool),
	}
	p.accounts = accounts
	return p
}

func TestGetNextForModelExcludingSkipsExcludedAccounts(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
	)
	excluded := map[string]bool{"a": true}
	for i := 0; i < 5; i++ {
		acc := p.GetNextForModelExcluding("model", excluded)
		if acc == nil {
			t.Fatal("expected account b, got nil")
		}
		if acc.ID == "a" {
			t.Fatalf("excluded account a was returned on iteration %d", i)
		}
	}
}

func TestGetNextForModelExcludingReturnsNilWhenAllExcluded(t *testing.T) {
	p := newTestPool(config.Account{ID: "only"})
	acc := p.GetNextForModelExcluding("model", map[string]bool{"only": true})
	if acc != nil {
		t.Fatalf("expected nil when only account is excluded, got %q", acc.ID)
	}
}

func TestGetNextForModelExcludingReturnsNilOnEmptyPool(t *testing.T) {
	p := newTestPool()
	acc := p.GetNextForModelExcluding("model", map[string]bool{})
	if acc != nil {
		t.Fatalf("expected nil for empty pool, got %q", acc.ID)
	}
}

// ---------------------------------------------------------------------------
// DisableAccount
// ---------------------------------------------------------------------------

func TestDisableAccountSetsCooldown(t *testing.T) {
	// Initialize a temporary config so SetAccountBanStatus can persist safely.
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	p := newTestPool()
	p.DisableAccount("test-id", "test reason")

	p.mu.RLock()
	cooldown, ok := p.cooldowns["test-id"]
	p.mu.RUnlock()

	if !ok {
		t.Fatal("expected cooldown to be set after DisableAccount")
	}
	// Safety-net cooldown must be at least 23 hours from now.
	minExpected := time.Now().Add(23 * time.Hour)
	if cooldown.Before(minExpected) {
		t.Fatalf("expected cooldown >= 23h in future, got %v", cooldown)
	}
}

func TestGetNextExcludingSkipsExcludedAccount(t *testing.T) {
	p := &AccountPool{
		accounts: []config.Account{
			{ID: "a", Enabled: true},
			{ID: "b", Enabled: true},
		},
		cooldowns:    make(map[string]time.Time),
		errorCounts:  make(map[string]int),
		modelLists:   make(map[string]map[string]bool),
		currentIndex: ^uint64(0),
	}

	acc := p.GetNextExcluding(map[string]bool{"a": true})
	if acc == nil || acc.ID != "b" {
		t.Fatalf("expected account b, got %#v", acc)
	}
}

func TestGetNextForModelExcludingSkipsExcludedAccount(t *testing.T) {
	p := &AccountPool{
		accounts: []config.Account{
			{ID: "a", Enabled: true},
			{ID: "b", Enabled: true},
		},
		cooldowns:    make(map[string]time.Time),
		errorCounts:  make(map[string]int),
		modelLists:   make(map[string]map[string]bool),
		currentIndex: ^uint64(0),
	}
	p.SetModelList("a", []string{"claude-sonnet-4.5"})
	p.SetModelList("b", []string{"claude-sonnet-4.5"})

	acc := p.GetNextForModelExcluding("claude-sonnet-4.5", map[string]bool{"a": true})
	if acc == nil || acc.ID != "b" {
		t.Fatalf("expected account b, got %#v", acc)
	}
}

// ---------------------------------------------------------------------------
// Reload over-usage filtering
// ---------------------------------------------------------------------------

func TestReloadKeepsOverQuotaAccountWhenAllowOverUsage(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID:           "over",
		Enabled:      true,
		UsageCurrent: 10,
		UsageLimit:   10,
	}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	if err := config.UpdateAllowOverUsage(true); err != nil {
		t.Fatalf("UpdateAllowOverUsage: %v", err)
	}

	p := newTestPool()
	p.Reload()

	if got := p.GetNext(); got == nil || got.ID != "over" {
		t.Fatalf("expected over-quota account to remain routable when allowOverUsage=true, got %#v", got)
	}
}

func TestReloadDropsOverQuotaAccountWhenAllowOverUsageDisabled(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID:           "over",
		Enabled:      true,
		UsageCurrent: 10,
		UsageLimit:   10,
	}); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	p := newTestPool()
	p.Reload()

	if got := p.GetNext(); got != nil {
		t.Fatalf("expected over-quota account to be dropped, got %q", got.ID)
	}
}

// ---------------------------------------------------------------------------
// GetNextWithinExcluding / GetNextForModelWithinExcluding
// ---------------------------------------------------------------------------

func TestGetNextWithinExcludingOnlyReturnsAllowed(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
		config.Account{ID: "c"},
	)
	allowed := map[string]bool{"b": true, "c": true}
	for i := 0; i < 10; i++ {
		acc := p.GetNextWithinExcluding(allowed, nil, false)
		if acc == nil {
			t.Fatal("expected an account")
		}
		if acc.ID == "a" {
			t.Fatalf("account 'a' is not in allowed set but was returned")
		}
	}
}

func TestGetNextWithinExcludingEmptyAllowedReturnsNil(t *testing.T) {
	p := newTestPool(config.Account{ID: "a"})
	acc := p.GetNextWithinExcluding(map[string]bool{}, nil, false)
	if acc != nil {
		t.Fatalf("expected nil for empty allowedIDs, got %q", acc.ID)
	}
}

func TestGetNextForModelWithinExcludingRespectsModelFilter(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
	)
	p.SetModelList("a", []string{"claude-sonnet-4.5"})
	p.SetModelList("b", []string{"claude-opus-4.5"})

	allowed := map[string]bool{"a": true, "b": true}
	acc := p.GetNextForModelWithinExcluding("claude-opus-4.5", allowed, nil, false)
	if acc == nil || acc.ID != "b" {
		t.Fatalf("expected account b (supports opus), got %v", acc)
	}
}

func TestGetNextWithinExcludingStrictSkipsCooldownFallback(t *testing.T) {
	p := newTestPool(config.Account{ID: "a"})
	// Cool down the only allowed account
	p.RecordError("a", true) // 1h cooldown

	allowed := map[string]bool{"a": true}
	// Non-strict: should return the cooling-down account as fallback
	acc := p.GetNextWithinExcluding(allowed, nil, false)
	if acc == nil {
		t.Fatal("non-strict should fallback to cooling-down account")
	}

	// Strict: should return nil
	acc = p.GetNextWithinExcluding(allowed, nil, true)
	if acc != nil {
		t.Fatalf("strict should return nil when all accounts cooling down, got %q", acc.ID)
	}
}

func TestGetNextWithinExcludingRespectsExcluded(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
	)
	allowed := map[string]bool{"a": true, "b": true}
	excluded := map[string]bool{"a": true}
	for i := 0; i < 5; i++ {
		acc := p.GetNextWithinExcluding(allowed, excluded, false)
		if acc == nil {
			t.Fatal("expected account b")
		}
		if acc.ID == "a" {
			t.Fatal("excluded account was returned")
		}
	}
}

func TestGetNextWithinExcludingPreservesWeighting(t *testing.T) {
	// Account "a" has weight 3, "b" has weight 1
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "a"},
		config.Account{ID: "a"},
		config.Account{ID: "b"},
	)
	allowed := map[string]bool{"a": true, "b": true}
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		acc := p.GetNextWithinExcluding(allowed, nil, false)
		if acc != nil {
			counts[acc.ID]++
		}
	}
	if counts["a"] < counts["b"] {
		t.Fatalf("expected weighted preference for 'a', got a=%d b=%d", counts["a"], counts["b"])
	}
}
