package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultModelPath   = "models/mobilenetv2/mobilenetv2-7.onnx"
	DefaultLabelPath   = "models/mobilenetv2/synset.txt"
	DefaultRuntimePath = "runtime/onnxruntime-win-x64-1.22.0/lib/onnxruntime.dll"
)

// ResolveAssetPath turns a project-relative asset path into an absolute path.
// Absolute paths are accepted unchanged, which allows local overrides to live
// outside the repository.
func ResolveAssetPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("asset path is empty")
	}

	if filepath.IsAbs(path) {
		absolutePath, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("resolve absolute asset path %q: %w", path, err)
		}
		return absolutePath, nil
	}

	projectRoot, err := findProjectRoot()
	if err != nil {
		return "", fmt.Errorf("resolve project-relative asset path %q: %w", path, err)
	}

	absolutePath, err := filepath.Abs(filepath.Join(projectRoot, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("resolve asset path %q from project root %q: %w", path, projectRoot, err)
	}
	return absolutePath, nil
}

func findProjectRoot() (string, error) {
	workingDirectory, workingDirectoryErr := os.Getwd()
	if workingDirectoryErr == nil {
		if root, err := findProjectRootFrom(workingDirectory); err == nil {
			return root, nil
		}
	}

	executablePath, executableErr := os.Executable()
	if executableErr == nil {
		if root, err := findProjectRootFrom(filepath.Dir(executablePath)); err == nil {
			return root, nil
		}
	}

	if workingDirectoryErr != nil {
		return "", fmt.Errorf("get working directory: %w", workingDirectoryErr)
	}
	if executableErr != nil {
		return "", fmt.Errorf("no go.mod found above %q; get executable path: %w", workingDirectory, executableErr)
	}
	return "", fmt.Errorf("no go.mod found above working directory %q or executable directory %q", workingDirectory, filepath.Dir(executablePath))
}

func findProjectRootFrom(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve start directory %q: %w", start, err)
	}

	for {
		info, statErr := os.Stat(filepath.Join(current, "go.mod"))
		if statErr == nil && !info.IsDir() {
			return current, nil
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", fmt.Errorf("inspect go.mod in %q: %w", current, statErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return "", fmt.Errorf("no go.mod found above %q", start)
}

func requireRegularFile(path, description string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s not found at %q", description, path)
		}
		return fmt.Errorf("inspect %s at %q: %w", description, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s path %q is not a regular file", description, path)
	}
	return nil
}
