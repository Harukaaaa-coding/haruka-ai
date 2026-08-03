package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	realGCCEnvironment = "GOPHERAI_REAL_GCC"
	objcopyEnvironment = "GOPHERAI_OBJCOPY"
	objdumpEnvironment = "GOPHERAI_OBJDUMP"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	realGCC := os.Getenv(realGCCEnvironment)
	objcopy := os.Getenv(objcopyEnvironment)
	objdump := os.Getenv(objdumpEnvironment)
	if strings.TrimSpace(realGCC) == "" || strings.TrimSpace(objcopy) == "" || strings.TrimSpace(objdump) == "" {
		fmt.Fprintf(
			stderr,
			"cgo-gcc-wrapper: %s, %s, and %s must point to executables\n",
			realGCCEnvironment,
			objcopyEnvironment,
			objdumpEnvironment,
		)
		return 2
	}

	gcc := exec.Command(realGCC, arguments...)
	gcc.Stdin = stdin
	gcc.Stdout = stdout
	gcc.Stderr = stderr
	if err := gcc.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return exitError.ExitCode()
		}
		fmt.Fprintf(stderr, "cgo-gcc-wrapper: could not run GCC %q: %v\n", realGCC, err)
		return 1
	}

	outputPath := outputArgument(arguments)
	if !shouldConvertOutput(outputPath) {
		return 0
	}
	format, err := inspectObjectFormat(objdump, outputPath)
	if err != nil {
		fmt.Fprintf(stderr, "cgo-gcc-wrapper: could not inspect %q: %v\n", outputPath, err)
		return 1
	}
	if format == "pe-x86-64" {
		return 0
	}
	if format != "pe-bigobj-x86-64" {
		fmt.Fprintf(stderr, "cgo-gcc-wrapper: unsupported object format %q in %q\n", format, outputPath)
		return 1
	}

	if err := convertBigObj(objcopy, outputPath, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "cgo-gcc-wrapper: could not convert %q to standard PE/COFF: %v\n", outputPath, err)
		return 1
	}

	return 0
}

func inspectObjectFormat(objdump, outputPath string) (string, error) {
	command := exec.Command(objdump, "-f", outputPath)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run objdump %q: %w: %s", objdump, err, strings.TrimSpace(string(output)))
	}
	return parseObjectFormat(string(output))
}

func parseObjectFormat(output string) (string, error) {
	const marker = "file format "
	for _, line := range strings.Split(output, "\n") {
		if index := strings.Index(line, marker); index >= 0 {
			format := strings.TrimSpace(line[index+len(marker):])
			if format != "" {
				return format, nil
			}
		}
	}
	return "", fmt.Errorf("objdump output did not contain an object format")
}

func convertBigObj(objcopy, outputPath string, stdout, stderr io.Writer) error {
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("GCC output is unavailable: %w", err)
	}
	if !outputInfo.Mode().IsRegular() {
		return fmt.Errorf("GCC output is not a regular file")
	}

	temporaryFile, err := os.CreateTemp(filepath.Dir(outputPath), "_cgo-standard-pe-*.o")
	if err != nil {
		return fmt.Errorf("create conversion target: %w", err)
	}
	convertedPath := temporaryFile.Name()
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(convertedPath)
		return fmt.Errorf("close conversion target: %w", err)
	}
	if err := os.Remove(convertedPath); err != nil {
		return fmt.Errorf("prepare conversion target: %w", err)
	}
	defer os.Remove(convertedPath) // Best-effort cleanup after failures.

	convert := exec.Command(
		objcopy,
		"-I", "pe-bigobj-x86-64",
		"-O", "pe-x86-64",
		outputPath,
		convertedPath,
	)
	convert.Stdout = stdout
	convert.Stderr = stderr
	if err := convert.Run(); err != nil {
		return fmt.Errorf("run objcopy %q: %w", objcopy, err)
	}

	convertedInfo, err := os.Stat(convertedPath)
	if err != nil {
		return fmt.Errorf("objcopy did not create its output: %w", err)
	}
	if !convertedInfo.Mode().IsRegular() {
		return fmt.Errorf("objcopy output is not a regular file")
	}

	backupFile, err := os.CreateTemp(filepath.Dir(outputPath), "_cgo-bigobj-backup-*.o")
	if err != nil {
		return fmt.Errorf("create backup path: %w", err)
	}
	backupPath := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		_ = os.Remove(backupPath)
		return fmt.Errorf("close backup path: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("prepare backup path: %w", err)
	}
	defer os.Remove(backupPath) // Best-effort cleanup after successful replacement.

	if err := os.Rename(outputPath, backupPath); err != nil {
		return fmt.Errorf("preserve original GCC output: %w", err)
	}
	if err := os.Rename(convertedPath, outputPath); err != nil {
		if restoreErr := os.Rename(backupPath, outputPath); restoreErr != nil {
			return fmt.Errorf("install converted output: %v (also failed to restore original: %v)", err, restoreErr)
		}
		return fmt.Errorf("install converted output (original restored): %w", err)
	}

	return nil
}

func outputArgument(arguments []string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == "-o" {
			return arguments[index+1]
		}
	}
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "-o") && len(argument) > 2 {
			return argument[2:]
		}
	}
	return ""
}

func shouldConvertOutput(outputPath string) bool {
	return filepath.Base(outputPath) == "_cgo_.o"
}
