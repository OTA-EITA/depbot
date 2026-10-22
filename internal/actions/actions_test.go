package actions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OTA-EITA/depbot/internal/github"
	"github.com/OTA-EITA/depbot/internal/update"
)

type fakeTags map[string][]github.Tag

func (f fakeTags) Tags(_ context.Context, owner, repo string, _ int) ([]github.Tag, error) {
	return f[owner+"/"+repo], nil
}

var tags = fakeTags{
	"actions/checkout": {
		{Name: "v5.0.0", SHA: strings.Repeat("5", 40)}, {Name: "v5", SHA: strings.Repeat("5", 40)},
		{Name: "v4.2.2", SHA: strings.Repeat("4", 40)}, {Name: "v4", SHA: strings.Repeat("4", 40)},
	},
	"actions/setup-go":     {{Name: "v6", SHA: strings.Repeat("6", 40)}, {Name: "v6.0.0", SHA: strings.Repeat("6", 40)}},
	"github/codeql-action": {{Name: "v4", SHA: strings.Repeat("c", 40)}, {Name: "v3", SHA: strings.Repeat("b", 40)}},
}

const workflow = `name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/checkout@` + "4444444444444444444444444444444444444444" + ` # v4.2.2
      - uses: "actions/setup-go@v6"
      - uses: github/codeql-action/upload-sarif@v3
      - uses: ./local-action
      - uses: docker://alpine:3.20
      - uses: someone/tool@main
      # - uses: actions/checkout@v3
`

func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte(workflow), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "action.yml"), []byte("runs:\n  steps:\n    - uses: actions/setup-go@v6 # keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindUpdates(t *testing.T) {
	root := setup(t)
	ups, err := Find(context.Background(), root, tags, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range ups {
		got = append(got, u.Name+" "+u.From+"->"+u.To+" "+u.Kind)
	}
	want := []string{
		"actions/checkout v4->v5 major",
		"actions/checkout v4.2.2->v5.0.0 major",
		"github/codeql-action v3->v4 major",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	for _, u := range ups {
		if err := update.ApplyEdits(root, u.Edits); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	s := string(data)
	for _, want := range []string{
		"- uses: actions/checkout@v5\n",
		"- uses: actions/checkout@" + strings.Repeat("5", 40) + " # v5.0.0\n",
		"- uses: github/codeql-action/upload-sarif@v4\n",
		"# - uses: actions/checkout@v3",
		"someone/tool@main",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
}

func TestPin(t *testing.T) {
	root := setup(t)
	ups, err := Find(context.Background(), root, tags, Options{Pin: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range ups {
		if err := update.ApplyEdits(root, u.Edits); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if !strings.Contains(string(data), `- uses: "actions/setup-go@`+strings.Repeat("6", 40)+`" # v6`) {
		t.Errorf("quoted setup-go must be pinned with the comment outside the quotes:\n%s", data)
	}
	action, _ := os.ReadFile(filepath.Join(root, "action.yml"))
	if !strings.Contains(string(action), "actions/setup-go@"+strings.Repeat("6", 40)+" # v6") {
		t.Errorf("composite action was not pinned:\n%s", action)
	}
	pins := 0
	for _, u := range ups {
		if u.Kind == "pin" {
			pins++
		}
	}
	if pins != 2 {
		t.Errorf("want 2 pin-only updates (setup-go in ci.yml and action.yml), got %d", pins)
	}
}

func TestPinOnlyKeepsVersions(t *testing.T) {
	root := setup(t)
	ups, err := Find(context.Background(), root, tags, Options{PinOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range ups {
		if u.Kind != "pin" || u.From != u.To {
			t.Errorf("pin-only must not bump: %+v", u)
		}
		if err := update.ApplyEdits(root, u.Edits); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	s := string(data)
	for _, want := range []string{
		"- uses: actions/checkout@" + strings.Repeat("4", 40) + " # v4\n",
		"- uses: actions/checkout@" + strings.Repeat("4", 40) + " # v4.2.2\n",
		"- uses: github/codeql-action/upload-sarif@" + strings.Repeat("b", 40) + " # v3\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
}
