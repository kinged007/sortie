package ui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var referencePattern = regexp.MustCompile(`\b(?:src|href)="([^"]*)"`)

func TestIndexReferencesExist(t *testing.T) {
	t.Parallel()

	index, err := fs.ReadFile(distFS, "dist/index.html")
	if err != nil {
		t.Fatalf("ReadFile(dist/index.html) = %v, want nil", err)
	}

	matches := referencePattern.FindAllStringSubmatch(string(index), -1)
	if len(matches) == 0 {
		t.Fatal("dist/index.html has no src or href reference, want at least one")
	}

	for _, m := range matches {
		ref := m[1]
		name, ok := strings.CutPrefix(ref, "/ui/")
		if !ok {
			t.Errorf("reference %q does not begin with /ui/", ref)
			continue
		}
		info, err := fs.Stat(distFS, "dist/"+name)
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("reference %q names no regular file in the embedded output (stat err = %v)", ref, err)
		}
	}
}
