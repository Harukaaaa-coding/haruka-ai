package image

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const imageNetClassCount = 1000

func loadLabels(path string) ([]string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open ImageNet label file %q: %w", path, err)
	}
	defer file.Close()

	labels := make([]string, 0, imageNetClassCount)
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		label := strings.TrimSpace(scanner.Text())
		if label == "" {
			return nil, fmt.Errorf("ImageNet label file %q contains an empty label at line %d", path, lineNumber)
		}
		labels = append(labels, label)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ImageNet label file %q: %w", path, err)
	}
	if len(labels) != imageNetClassCount {
		return nil, fmt.Errorf("ImageNet label file %q must contain exactly %d labels, got %d", path, imageNetClassCount, len(labels))
	}
	return labels, nil
}
