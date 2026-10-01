package creds

import (
	"os"
	"testing"
)

// Runs only against a throwaway Keychain entry, never the real "llok" one.
func TestKeychainRoundTrip(t *testing.T) {
	if svc := os.Getenv("LLOK_KEYCHAIN_SERVICE"); svc == "" || svc == "llok" {
		t.Skip("set LLOK_KEYCHAIN_SERVICE to a throwaway name to run")
	}
	want := Credential{Kind: "oauth", Secret: `sk-ant-oat01-TEST"quoted\back`}
	if err := Set("roundtrip", want); err != nil {
		t.Fatal(err)
	}
	defer Delete("roundtrip")
	got, err := Get("roundtrip")
	if err != nil || got != want {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, want)
	}
	if err := Delete("roundtrip"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("roundtrip"); err != ErrNotFound {
		t.Errorf("after delete expected ErrNotFound, got %v", err)
	}
}
