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
