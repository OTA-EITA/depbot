// Package version compares release versions found in module paths, git tags
// and container image tags, which may or may not carry a "v" prefix.
package version

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Canonical returns a semver string with a "v" prefix, or "" when s is not a
// version. Docker-style "1.25" and "1.25.3" become "v1.25" and "v1.25.3".
func Canonical(s string) string {
	v := s
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// Compare compares two versions; invalid versions sort first.
func Compare(a, b string) int { return semver.Compare(Canonical(a), Canonical(b)) }

// IsPrerelease reports versions such as v1.2.0-rc.1.
func IsPrerelease(s string) bool { return semver.Prerelease(Canonical(s)) != "" }

// Major returns "v1" for "1.25.3".
func Major(s string) string { return semver.Major(Canonical(s)) }

// Kind classifies a bump: "major", "minor" or "patch".
func Kind(from, to string) string {
	f, t := Canonical(from), Canonical(to)
	switch {
	case semver.Major(f) != semver.Major(t):
		return "major"
	case semver.MajorMinor(f) != semver.MajorMinor(t):
		return "minor"
	default:
		return "patch"
	}
}

// Precision counts the numeric components written in a version: "v4" is 1,
// "1.25" is 2 and "v4.1.7" is 3. Updates keep the precision of the original
// so a floating major tag stays a major tag.
func Precision(s string) int {
	core := strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	return strings.Count(core, ".") + 1
}

// Latest returns the highest stable version among candidates that has the
// same precision as current and is newer than it. With sameMajor it stays
// within the current major version. It returns "" when nothing is newer.
func Latest(current string, candidates []string, sameMajor bool) string {
	best := ""
	for _, c := range candidates {
		if Canonical(c) == "" || IsPrerelease(c) || Precision(c) != Precision(current) {
			continue
		}
		if sameMajor && Major(c) != Major(current) {
			continue
		}
		if Compare(c, current) <= 0 {
			continue
		}
		if best == "" || Compare(c, best) > 0 {
			best = c
		}
	}
	return best
}
