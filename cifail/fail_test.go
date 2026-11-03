package cifail

import "testing"

// Deliberate failure to exercise the failing-PR path (panel + comment).
func TestDeliberateFailure(t *testing.T) {
	t.Fatal("deliberate CI failure test")
}
