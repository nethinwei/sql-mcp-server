package version

import "testing"

func TestString(t *testing.T) {
	if got := String(); got == "" {
		t.Fatal("version must not be empty")
	}
}
