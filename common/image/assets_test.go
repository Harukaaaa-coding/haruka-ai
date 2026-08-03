package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectRootFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	nested := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	got, err := findProjectRootFrom(nested)
	if err != nil {
		t.Fatalf("findProjectRootFrom returned an error: %v", err)
	}
	if got != root {
		t.Fatalf("findProjectRootFrom = %q, want %q", got, root)
	}
}

func TestResolveAssetPathUsesProjectRoot(t *testing.T) {
	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("findProjectRoot returned an error: %v", err)
	}
	want := filepath.Join(root, filepath.FromSlash(DefaultModelPath))

	got, err := ResolveAssetPath(DefaultModelPath)
	if err != nil {
		t.Fatalf("ResolveAssetPath returned an error: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveAssetPath = %q, want %q", got, want)
	}
}

func TestResolveAssetPathPreservesAbsolutePath(t *testing.T) {
	want, err := filepath.Abs(filepath.Join(t.TempDir(), "model.onnx"))
	if err != nil {
		t.Fatalf("resolve test path: %v", err)
	}
	got, err := ResolveAssetPath(want)
	if err != nil {
		t.Fatalf("ResolveAssetPath returned an error: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveAssetPath = %q, want %q", got, want)
	}
}
