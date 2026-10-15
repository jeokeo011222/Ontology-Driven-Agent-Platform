package ids

import "testing"

func TestNewAndStringRoundTrip(t *testing.T) {
	id := New()
	if id.IsZero() {
		t.Fatal("New() returned zero id")
	}
	s := id.String()
	if len(s) != 36 {
		t.Fatalf("String() length = %d, want 36", len(s))
	}
	if s[14] != '4' {
		t.Fatalf("version nibble = %c, want 4", s[14])
	}
	if s[19] != '8' && s[19] != '9' && s[19] != 'a' && s[19] != 'b' {
		t.Fatalf("variant nibble = %c, want 8/9/a/b", s[19])
	}
	parsed, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", s, err)
	}
	if parsed != id {
		t.Fatalf("round trip mismatch: %s != %s", parsed, id)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "xyz", "11111111-1111-1111-1111-11111111111g"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) expected error", bad)
		}
	}
}

func TestUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 10000; i++ {
		s := New().String()
		if seen[s] {
			t.Fatalf("duplicate id generated: %s", s)
		}
		seen[s] = true
	}
}
