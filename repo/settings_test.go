package repo

import "testing"

func TestRedactionKeyOutlivesTheProcess(t *testing.T) {
	home := t.TempDir()
	read := func() string {
		db, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		settings, err := db.Settings()
		if err != nil {
			t.Fatal(err)
		}
		return settings.RedactionKey
	}
	key := read()
	if len(key) < 16 {
		t.Fatalf("redaction key %q is too short", key)
	}
	if again := read(); again != key {
		t.Errorf("reopening the database changed the redaction key from %q to %q", key, again)
	}
}
