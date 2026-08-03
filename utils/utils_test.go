package utils

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSecureRandomNumbersUsesUnbiasedDigits(t *testing.T) {
	got, err := secureRandomNumbersFromReader(bytes.NewReader([]byte{0, 1, 9, 10, 249}), 5)
	if err != nil {
		t.Fatalf("secureRandomNumbersFromReader(): %v", err)
	}
	if got != "01909" {
		t.Fatalf("digits = %q, want 01909", got)
	}
}

func TestSecureRandomNumbersRejectsBiasedBytes(t *testing.T) {
	// 250 and 251 are discarded; the next unbiased bytes fill the result.
	got, err := secureRandomNumbersFromReader(bytes.NewReader([]byte{250, 1, 251, 2}), 2)
	if err != nil {
		t.Fatalf("secureRandomNumbersFromReader(): %v", err)
	}
	if got != "12" {
		t.Fatalf("digits = %q, want 12", got)
	}
}

func TestSecureRandomNumbersRejectsInvalidLengthAndReaderFailure(t *testing.T) {
	if _, err := secureRandomNumbersFromReader(bytes.NewReader(nil), 0); err == nil {
		t.Fatal("zero length error = nil, want error")
	}
	if _, err := secureRandomNumbersFromReader(failingReader{}, 6); !errors.Is(err, errSecureRandomFailure) {
		t.Fatalf("reader failure = %v, want random source error", err)
	}
}

func TestSecureRandomNumbersReturnsDigits(t *testing.T) {
	got, err := SecureRandomNumbers(32)
	if err != nil {
		t.Fatalf("SecureRandomNumbers(): %v", err)
	}
	if len(got) != 32 || strings.Trim(got, "0123456789") != "" {
		t.Fatalf("generated value = %q, want 32 decimal digits", got)
	}
}

var errSecureRandomFailure = errors.New("random source failed")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errSecureRandomFailure
}
