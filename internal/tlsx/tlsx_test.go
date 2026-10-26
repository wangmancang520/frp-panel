package tlsx

import (
	"strings"
	"testing"
)

// Managed clients pin the self-signed key, so regenerating the certificate
// (e.g. to add a SAN) must not change the pin.
func TestPinSurvivesRegeneration(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateSelfSigned(dir, nil); err != nil {
		t.Fatal(err)
	}
	pin1, err := SelfSignedPin(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp1, _ := SelfSignedFingerprint(dir)
	if err := GenerateSelfSigned(dir, []string{"1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	pin2, _ := SelfSignedPin(dir)
	fp2, _ := SelfSignedFingerprint(dir)
	if !strings.HasPrefix(pin1, "sha256//") || pin1 != pin2 {
		t.Fatalf("pin changed: %s -> %s", pin1, pin2)
	}
	if fp1 == fp2 {
		t.Fatal("certificate was not regenerated")
	}
}
