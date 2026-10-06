package domain

import "testing"

func TestFingerprintLineNumbersStripped(t *testing.T) {
	a := Fingerprint("bug", "internal/x.go:12", "  Too MANY logs!! ")
	b := Fingerprint("bug", "internal/x.go:99", "too many logs")
	if a != b {
		t.Errorf("Fingerprint a = %q, b = %q; want equal (line number stripped, case/space normalised)", a, b)
	}
}

func TestFingerprintDifferentCategory(t *testing.T) {
	a := Fingerprint("bug", "internal/x.go:12", "too many logs")
	b := Fingerprint("smell", "internal/x.go:12", "too many logs")
	if a == b {
		t.Error("different category must produce a different fingerprint")
	}
}

func TestFingerprintSegmentAlignedLocation(t *testing.T) {
	// different locations that are not just line-number variants differ
	a := Fingerprint("bug", "internal/x.go", "too many logs")
	b := Fingerprint("bug", "internal/y.go", "too many logs")
	if a == b {
		t.Error("different file locations must produce different fingerprints")
	}
}

func TestFingerprintFormat(t *testing.T) {
	// lowercase, non-alnum runs collapsed to single space, trimmed
	a := Fingerprint("bug", "internal/x.go:3", "Too   many --- logs?!")
	b := Fingerprint("bug", "internal/x.go:3", "too many logs")
	if a != b {
		t.Errorf("Fingerprint a = %q, b = %q; want equal after normalisation", a, b)
	}
	if len(a) != 16 {
		t.Errorf("fingerprint length = %d, want 16 (sha1 hex, first 16 chars)", len(a))
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	if Fingerprint("bug", "l:1", "t") != Fingerprint("bug", "l:1", "t") {
		t.Error("fingerprint must be deterministic")
	}
}
