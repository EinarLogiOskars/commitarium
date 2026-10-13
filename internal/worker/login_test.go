package worker

import "testing"

func TestIsLoginFailureRecognizesExpiredProviderLogins(t *testing.T) {
	for _, text := range []string{
		"Failed to authenticate: OAuth session expired and could not be refreshed",
		"Invalid API key · Please run /login",
		"unexpected status 401 Unauthorized: Your access token could not be refreshed",
	} {
		if !IsLoginFailure(text) {
			t.Errorf("%q was not recognized as a login failure", text)
		}
	}
	for _, text := range []string{"rate limit exceeded", "Codex reported that the turn failed.", ""} {
		if IsLoginFailure(text) {
			t.Errorf("%q was recognized as a login failure", text)
		}
	}
}

func TestUsageLimitTextMatchesOnlyAccountLimits(t *testing.T) {
	for _, text := range []string{
		"You've hit your usage limit. Try again at 3:45 PM.",
		"Claude AI usage limit reached|1791810000",
		"429 Too Many Requests",
	} {
		if !IsUsageLimit(text) {
			t.Errorf("%q is a usage limit", text)
		}
	}
	for _, text := range []string{
		"Context window limit reached; start a new conversation.",
		"The command exceeded its time limit.",
	} {
		if IsUsageLimit(text) {
			t.Errorf("%q is not a usage limit", text)
		}
	}
}

func TestTransientFailureTextMatchesOnlyProviderAndNetworkErrors(t *testing.T) {
	for _, text := range []string{
		`API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		"API Error: 500 Internal server error",
		"stream disconnected before completion: error sending request for url",
		"fetch failed: ECONNRESET",
	} {
		if !IsTransientFailure(text) {
			t.Errorf("%q is transient", text)
		}
	}
	for _, text := range []string{
		"Claude AI usage limit reached|1791810000",
		"Failed to authenticate. API Error: 401 OAuth token has expired",
		"Context window limit reached; start a new conversation.",
		"Codex reported that the turn failed.",
	} {
		if IsTransientFailure(text) {
			t.Errorf("%q is not transient", text)
		}
	}
}
