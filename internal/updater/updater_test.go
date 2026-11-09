package updater

import (
	"strings"
	"testing"
)

func TestWithGOBINReplacesExistingValueWithoutDuplicates(t *testing.T) {
	env := withGOBIN([]string{"PATH=/bin", "GOBIN=/old/bin", "HOME=/home/test", "GOBIN=/ignored"}, "/custom/bin")
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "GOBIN=") {
			count++
			if entry != "GOBIN=/custom/bin" {
				t.Fatalf("GOBIN = %q, want custom directory", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("found %d GOBIN entries, want one", count)
	}
}

func TestWithGOBINAddsMissingValue(t *testing.T) {
	env := withGOBIN([]string{"PATH=/bin"}, "/custom/bin")
	if len(env) != 2 || env[1] != "GOBIN=/custom/bin" {
		t.Fatalf("withGOBIN() = %#v", env)
	}
}
