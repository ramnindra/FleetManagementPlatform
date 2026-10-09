package mqttc

import (
	"testing"
	"time"
)

func TestParseReceivedAt(t *testing.T) {
	got := parseReceivedAt("2026-10-09T04:14:13.123456789+02:00")
	want := time.Date(2026, 10, 9, 2, 14, 13, 123456789, time.UTC)
	if got == nil || !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []any{nil, 42, "yesterday", ""} {
		if parseReceivedAt(bad) != nil {
			t.Errorf("%v should parse to nil", bad)
		}
	}
}
