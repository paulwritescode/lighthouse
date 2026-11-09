package hostname

import (
	"strings"
	"testing"
)

func TestReplaceManagedBlockIsIdempotentAndPreservesOtherEntries(t *testing.T) {
	original := "127.0.0.1 localhost\n192.0.2.4 other.local\n"
	registered, err := replaceManagedBlock(original, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(registered, "127.0.0.1 localhost") || !strings.Contains(registered, "192.0.2.4 other.local") {
		t.Fatalf("registration changed unrelated hosts entries: %q", registered)
	}
	if strings.Count(registered, startMarker) != 1 || !strings.Contains(registered, "127.0.0.1 lighthouse") {
		t.Fatalf("registration did not add exactly one managed Lighthouse mapping: %q", registered)
	}
	repeated, err := replaceManagedBlock(registered, true)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != registered {
		t.Fatal("registering twice changed the hosts file")
	}
	removed, err := replaceManagedBlock(registered, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed != original {
		t.Fatalf("removing Lighthouse's block changed unrelated hosts entries: %q", removed)
	}
}

func TestReplaceManagedBlockRejectsIncompleteMarkers(t *testing.T) {
	if _, err := replaceManagedBlock("127.0.0.1 localhost\n"+startMarker+"\n", true); err == nil {
		t.Fatal("incomplete managed block should require manual repair")
	}
}
