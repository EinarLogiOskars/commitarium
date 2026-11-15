package workerhttp

import "testing"

func TestARunTokenMustBeTheCoordinatorsHex(t *testing.T) {
	for token, valid := range map[string]bool{
		"": true,
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": true,
		"not hex": false,
		"0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef": false,
	} {
		if got := runTokenPattern.MatchString(token); got != valid {
			t.Errorf("%q: valid = %v, want %v", token, got, valid)
		}
	}
}
