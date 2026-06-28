package gci

import (
	"testing"
	"time"
)

// TestPulledColEmpty verifies a never-pulled row shows "~" (null placeholder,
// matching the SHA column) rather than "-".
func TestPulledColEmpty(t *testing.T) {
	if got := pulledCol(time.Time{}, true); got != "~" {
		t.Errorf("empty PULLED = %q, want %q", got, "~")
	}
	if got := pulledCol(time.Time{}, false); got != "~" {
		t.Errorf("empty PULLED (piped) = %q, want %q", got, "~")
	}
}
