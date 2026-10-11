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
