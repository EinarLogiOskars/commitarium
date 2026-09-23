package validation

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeCommandsPreservesOrderAndRejectsUnsafeShape(t *testing.T) {
	commands, err := NormalizeCommands([]string{" go test ./... ", "cargo test"})
	if err != nil || !reflect.DeepEqual(commands, []string{"go test ./...", "cargo test"}) {
		t.Fatalf("NormalizeCommands = %v, %v", commands, err)
	}
	for _, invalid := range [][]string{{}, {""}, {"go test\x00echo nope"}} {
		if _, err := NormalizeCommands(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizeCommands(%q) error = %v, want ErrInvalid", invalid, err)
		}
	}
}
