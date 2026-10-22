package version

import "testing"

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{"1.25": "v1.25", "v4": "v4", "3.20.3": "v3.20.3", "latest": "", "1.25-alpine": "", "1.2.3-rc.1": "v1.2.3-rc.1", "": ""} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKindAndPrecision(t *testing.T) {
	if Kind("v4.1.0", "v5.0.0") != "major" || Kind("1.25", "1.26") != "minor" || Kind("v1.2.3", "v1.2.4") != "patch" {
		t.Error("Kind")
	}
	for in, want := range map[string]int{"v4": 1, "1.25": 2, "v4.1.7": 3, "1.2.3-rc.1": 3} {
		if got := Precision(in); got != want {
			t.Errorf("Precision(%q) = %d", in, got)
		}
	}
}

func TestLatest(t *testing.T) {
	tags := []string{"v3", "v4", "v4.1.0", "v4.2.2", "v5", "v5.0.0", "v6.0.0-beta.1", "v6-rc", "latest", "main"}
	cases := []struct {
		current   string
		sameMajor bool
		want      string
	}{
		{"v4", false, "v5"},
		{"v4.1.0", false, "v5.0.0"},
		{"v4.1.0", true, "v4.2.2"},
		{"v5", false, ""},
		{"v5.0.0", false, ""},
	}
	for _, c := range cases {
		if got := Latest(c.current, tags, c.sameMajor); got != c.want {
			t.Errorf("Latest(%q, sameMajor=%v) = %q, want %q", c.current, c.sameMajor, got, c.want)
		}
	}
	if got := Latest("1.25", []string{"1.24", "1.26", "1.26.1", "1.27rc1"}, false); got != "1.26" {
		t.Errorf("docker style: %q", got)
	}
}
