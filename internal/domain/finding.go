package domain

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	lineNo = regexp.MustCompile(`:\d+$`)
	nonAln = regexp.MustCompile(`[^a-z0-9]+`)
)

// Fingerprint builds a stable identity for a finding: category|location|title
// with the trailing ":<line>" stripped from the location and the title
// normalised (lowercase, non-alphanumeric runs collapsed to single spaces,
// trimmed). Returns the first 16 hex chars of the sha1.
func Fingerprint(category, location, title string) string {
	loc := lineNo.ReplaceAllString(location, "")
	title = strings.ToLower(title)
	title = nonAln.ReplaceAllString(title, " ")
	title = strings.Trim(title, " ")
	h := sha1.Sum([]byte(category + "|" + loc + "|" + title))
	return hex.EncodeToString(h[:])[:16]
}


