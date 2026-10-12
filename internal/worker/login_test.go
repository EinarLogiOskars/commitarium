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
