package cli

import (
	"reflect"
	"testing"
)

func TestSplitKeys(t *testing.T) {
	got := splitKeys([]byte("a\x1b[B\r\x1b[A\x1b"))
	want := []string{"a", "\x1b[B", "\r", "\x1b[A", "\x1b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitKeys = %q, want %q", got, want)
	}
}
