package redact

import "testing"

func TestPlaceholdersFollowTheKey(t *testing.T) {
	const text = "call 13812345678 or mail a@example.com"
	first, again, other := New("k1").Mask(text), New("k1").Mask(text), New("k2").Mask(text)
	if first == text {
		t.Fatalf("nothing was masked in %q", text)
	}
	if first != again {
		t.Errorf("the same key masked differently: %q and %q", first, again)
	}
	if first == other {
		t.Errorf("different keys masked the same: %q", first)
	}
	later := New("k1")
	later.Mask(text)
	if got := later.Restore(first); got != text {
		t.Errorf("a later redactor with the same key restored %q, want %q", got, text)
	}
}

func TestATextMaskedAgainIsMaskedAlike(t *testing.T) {
	r := New("k1")
	const text = "call 13812345678 or mail a@example.com"
	first := r.Mask(text)
	r.Mask("mail b@example.com")
	if again := r.Mask(text); again != first {
		t.Errorf("masked again %q, first %q", again, first)
	}
	if got := r.Restore(first); got != text {
		t.Errorf("restored %q, want %q", got, text)
	}
}
