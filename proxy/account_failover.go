// account_failover.go classifies upstream errors by string-matching the message
// and decides how the offending account is handled: short cooldown, quota cooldown,
// overage refresh, or hard disable (ban). It sits above the endpoint-level fallback
// in CallKiroAPI and below the handler's account-retry loop.
package proxy

import (
	"kiro-go/config"
	"kiro-go/logger"
	"strings"
	"time"
)

// maxAccountRetryAttempts caps how many different accounts the handler will try
// for a single client request before giving up.
const maxAccountRetryAttempts = 3

// isQuotaErrorMessage reports whether the error looks like a rate-limit/quota hit
// (HTTP 429 or a "quota" mention). Quota errors trigger a 1h account cooldown.
func isQuotaErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "429") || strings.Contains(msg, "quota")
}

// isOverageErrorMessage reports whether the upstream rejected the request because
// the account exceeded its overage allowance (HTTP 402 + "overage").
func isOverageErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "402") && strings.Contains(msg, "overage")
}

// isSuspensionErrorMessage reports whether AWS has temporarily suspended the account
// (typically flagged as unusual activity). These accounts get hard-disabled.
func isSuspensionErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "temporarily_suspended") ||
		strings.Contains(msg, "temporarily is suspended") ||
		strings.Contains(msg, "account suspended")
}

// isProfileUnavailableErrorMessage reports whether no Kiro profile ARN could be
// resolved for the account. This is treated as a soft, transient failure.
func isProfileUnavailableErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "no available kiro profile")
}

// isAuthErrorMessage reports whether the failure is an authentication/authorization
// problem (401/403, expired or invalid token, bad grant). These accounts get banned.
func isAuthErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "token invalid") ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "access token expired") ||
		strings.Contains(msg, "refresh token expired")
}

// disableAccount marks an account disabled with a ban status/reason, persists the
// change, and reloads the pool so it stops being selected. No-op if the account is
// already in the same disabled state.
func (h *Handler) disableAccount(account *config.Account, banStatus, banReason string) {
	if account == nil {
		return
	}

	updatedAccount := *account
	if !updatedAccount.Enabled && updatedAccount.BanStatus == banStatus && updatedAccount.BanReason == banReason {
		return
	}

	updatedAccount.Enabled = false
	updatedAccount.BanStatus = banStatus
	updatedAccount.BanReason = banReason
	updatedAccount.BanTime = time.Now().Unix()

	if err := config.UpdateAccount(account.ID, updatedAccount); err != nil {
		logger.Warnf("[AccountFailover] Failed to disable %s: %v", account.Email, err)
		return
	}

	logger.Warnf("[AccountFailover] Disabled %s: %s", account.Email, banReason)
	h.pool.Reload()
}

// disableAccountOverage re-fetches the account's authoritative overage status from
// upstream and persists it, rather than blindly disabling. Whether the account keeps
// serving is then decided by isQuotaBlocked on the next selection pass.
func (h *Handler) disableAccountOverage(account *config.Account) {
	if account == nil {
		return
	}

	snap, fetchErr := FetchOverageStatus(account)
	if fetchErr != nil {
		logger.Warnf("[AccountFailover] Failed to refresh overage status for %s: %v", account.Email, fetchErr)
		return
	}
	if persistErr := PersistOverageSnapshot(account.ID, snap); persistErr != nil {
		logger.Warnf("[AccountFailover] Failed to persist overage snapshot for %s: %v", account.Email, persistErr)
		return
	}

	logger.Warnf("[AccountFailover] Refreshed overage status for %s after upstream overage limit error: %s", account.Email, snap.Status)
	h.pool.Reload()
}

// handleAccountFailure routes an upstream error to the appropriate account action:
// overage refresh, quota cooldown, hard disable (suspension/auth), or a generic
// short cooldown for everything else. It is the single entry point the retry loop
// calls after a failed CallKiroAPI.
func (h *Handler) handleAccountFailure(account *config.Account, err error) {
	if account == nil || err == nil {
		return
	}

	errMsg := err.Error()
	switch {
	case isOverageErrorMessage(errMsg):
		h.disableAccountOverage(account)
		h.pool.RecordError(account.ID, false)
	case isQuotaErrorMessage(errMsg):
		h.pool.RecordError(account.ID, true)
	case isSuspensionErrorMessage(errMsg):
		h.disableAccount(account, "BANNED", "AWS temporarily suspended - unusual user activity detected")
	case isProfileUnavailableErrorMessage(errMsg):
		// Profile ARN may be transiently unresolvable (upstream blip, stale token).
		// Treat as a soft failure: short cooldown so the next request rotates account,
		// but never auto-disable — operators can still investigate via warn logs.
		h.pool.RecordError(account.ID, false)
	case isAuthErrorMessage(errMsg):
		h.disableAccount(account, "BANNED", "Authentication failed - token invalid or expired")
	default:
		h.pool.RecordError(account.ID, false)
	}
}
