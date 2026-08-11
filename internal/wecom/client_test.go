package wecom

import "testing"

func TestTextCell(t *testing.T) {
	if got := textCell([]any{map[string]any{"type": "text", "text": "ok"}}); got != "ok" {
		t.Fatalf("got %q", got)
	}
}
