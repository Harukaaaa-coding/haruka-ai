package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLabelsRequiresExactlyOneThousandLabels(t *testing.T) {
	path := writeLabelsFile(t, imageNetClassCount, -1)
	labels, err := loadLabels(path)
	if err != nil {
		t.Fatalf("loadLabels returned an error: %v", err)
	}
	if len(labels) != imageNetClassCount {
		t.Fatalf("loadLabels returned %d labels, want %d", len(labels), imageNetClassCount)
	}
	if labels[0] != "class-0000" || labels[imageNetClassCount-1] != "class-0999" {
		t.Fatalf("unexpected first or last label: %q, %q", labels[0], labels[imageNetClassCount-1])
	}
}

func TestLoadLabelsRejectsWrongCount(t *testing.T) {
	path := writeLabelsFile(t, imageNetClassCount-1, -1)
	_, err := loadLabels(path)
	if err == nil || !strings.Contains(err.Error(), "exactly 1000") {
		t.Fatalf("loadLabels error = %v, want exact-count error", err)
	}
}

func TestLoadLabelsRejectsEmptyLine(t *testing.T) {
	path := writeLabelsFile(t, imageNetClassCount, 42)
	_, err := loadLabels(path)
	if err == nil || !strings.Contains(err.Error(), "empty label at line 43") {
		t.Fatalf("loadLabels error = %v, want empty-line error", err)
	}
}

func writeLabelsFile(t *testing.T, count, emptyIndex int) string {
	t.Helper()
	var contents strings.Builder
	for index := 0; index < count; index++ {
		if index != emptyIndex {
			fmt.Fprintf(&contents, "class-%04d", index)
		}
		contents.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "synset.txt")
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatalf("write labels file: %v", err)
	}
	return path
}
