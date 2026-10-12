package scanutil

import "testing"

func TestOriginalOffset(t *testing.T) {
	raw := []byte("\xEF\xBB\xBFab\r\ncd\ref")
	norm := NormalizeSource(raw) // "ab\ncd\nef"
	for i := 0; i <= len(norm); i++ {
		o := OriginalOffset(raw, i)
		if i < len(norm) && norm[i] != '\n' && raw[o] != norm[i] {
			t.Errorf("normalized %d (%q) → raw %d (%q)", i, norm[i], o, raw[o])
		}
	}
	if got := OriginalOffset(raw, len(norm)); got != len(raw) {
		t.Errorf("end: got %d, want %d", got, len(raw))
	}
	// The newline of a CRLF maps to its "\r\n" pair's "\n".
	if got := OriginalOffset(raw, 2); raw[got] != '\n' && raw[got] != '\r' {
		t.Errorf("newline maps to %q", raw[got])
	}
}
