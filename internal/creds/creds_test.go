package creds

import "testing"

func TestQuote(t *testing.T) {
	cases := map[string]string{
		`plain`:            `"plain"`,
		`with "quotes"`:    `"with \"quotes\""`,
		`back\slash`:       `"back\\slash"`,
		`{"kind":"oauth"}`: `"{\"kind\":\"oauth\"}"`,
	}
	for in, want := range cases {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMasked(t *testing.T) {
	if Masked("short") != "••••" {
		t.Error("short secrets must be fully masked")
	}
	if got := Masked("sk-ant-oat01-ABCDEFGHIJKLMNOP"); got != "sk-ant-o…MNOP" {
		t.Errorf("Masked = %q", got)
	}
}
