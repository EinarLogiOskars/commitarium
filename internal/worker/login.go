package worker

import "strings"

// loginFailureMarkers are phrases provider CLIs use when their stored login
// can no longer be used: an expired or revoked subscription session or an
// invalid API key.
var loginFailureMarkers = []string{
	"failed to authenticate",
	"oauth session expired",
	"oauth token has expired",
	"please run /login",
	"not logged in",
	"invalid api key",
	"invalid x-api-key",
	"authentication_error",
	"401 unauthorized",
	"access token could not be refreshed",
	"refresh token",
	"please log in again",
}

// IsLoginFailure reports whether a provider's failure text says that its
// login must be renewed, so the work order can wait for the user to log in
// instead of retrying a turn that cannot succeed (ADR-017).
func IsLoginFailure(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range loginFailureMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// usageLimitMarkers are phrases provider CLIs use when an account reached
// its usage or rate limit.
// Only phrases that mean an account limit: a looser "limit reached" would also
// match a context-window error, which retrying never fixes.
var usageLimitMarkers = []string{
	"usage limit",
	"hit your limit",
	"rate limit reached",
	"rate_limit_error",
	"429 too many requests",
}

// IsUsageLimit reports whether a provider's failure text says the account
// reached its usage limit, so the work waits for the reset instead of failing.
// Adapters prefer the providers' structured signals; this covers text-only
// failures.
func IsUsageLimit(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range usageLimitMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// transientFailureMarkers are phrases provider CLIs use when a turn failed
// for the provider's or the network's sake, not the work's: an overloaded or
// erroring API, or a dropped connection.
var transientFailureMarkers = []string{
	"overloaded",
	"api error: 5",
	"internal server error",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"connection error",
	"connection reset",
	"connection refused",
	"econnreset",
	"econnrefused",
	"etimedout",
	"socket hang up",
	"network error",
	"fetch failed",
	"stream disconnected",
	"error sending request",
}

// IsTransientFailure reports whether a provider's failure text says the turn
// was cut off by the provider or the network, so trying it again later can
// succeed. A login or usage-limit failure is not transient: it waits for the
// user or the reset instead.
func IsTransientFailure(text string) bool {
	if IsLoginFailure(text) || IsUsageLimit(text) {
		return false
	}
	text = strings.ToLower(text)
	for _, marker := range transientFailureMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
