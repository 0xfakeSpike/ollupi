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

func TestOnlyNumbersThatCouldBeCardsAreMaskedAsCards(t *testing.T) {
	r := New("k1")
	// Each passes the Luhn check.
	for _, number := range []string{"4111111111111111", "5555555555554444", "378282246310005", "6222021234567890102"} {
		if got := r.Mask("card " + number); got == "card "+number {
			t.Errorf("the card number %s was not masked", number)
		}
	}
	for _, text := range []string{
		"  [ 1] .text PROGBITS 0000000000401000 001000 000010",
		"  LOAD 0x000000 0x0000000000000000 0x0000000000000000",
		`{"ts": 1762905600007, "ns": 1762905600000000007}`,
	} {
		if got := r.Mask(text); got != text {
			t.Errorf("masked %q to %q", text, got)
		}
	}
}

func TestAMACIsSixPairsOnTheirOwn(t *testing.T) {
	r := New("k1")
	for _, text := range []string{"ether 3c:22:fb:12:34:56 txqueuelen 1000", "MAC AA-BB-CC-DD-EE-0F."} {
		if got := r.Mask(text); got == text {
			t.Errorf("the MAC address in %q was not masked", text)
		}
	}
	const fingerprint = "sha256 Fingerprint=20:04:32:92:77:F2:A8:86:9E:0D:FA:77:C9:D6:3C:DE:28:13:8B:D7:D6:C1:68:59:FE:6F:C9:A1:5B:8E:01:02"
	if got := r.Mask(fingerprint); got != fingerprint {
		t.Errorf("masked %q to %q", fingerprint, got)
	}
}
