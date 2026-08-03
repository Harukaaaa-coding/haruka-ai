package main

import "testing"

func TestOutputArgument(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "separate argument", arguments: []string{"-c", "input.c", "-o", `C:\\tmp\\_cgo_.o`}, want: `C:\\tmp\\_cgo_.o`},
		{name: "combined argument", arguments: []string{"-c", "input.c", `-oC:\\tmp\\_cgo_.o`}, want: `C:\\tmp\\_cgo_.o`},
		{name: "no output", arguments: []string{"--version"}, want: ""},
		{name: "missing separate value", arguments: []string{"-c", "input.c", "-o"}, want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outputArgument(test.arguments); got != test.want {
				t.Fatalf("outputArgument() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestShouldConvertOutput(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "cgo probe", path: `C:\\tmp\\go-build123\\_cgo_.o`, want: true},
		{name: "ordinary object", path: `C:\\tmp\\go-build123\\image.o`, want: false},
		{name: "similar name", path: `C:\\tmp\\go-build123\\prefix_cgo_.o`, want: false},
		{name: "empty", path: "", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldConvertOutput(test.path); got != test.want {
				t.Fatalf("shouldConvertOutput(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}

func TestParseObjectFormat(t *testing.T) {
	got, err := parseObjectFormat("sample.o:     file format pe-bigobj-x86-64\narchitecture: i386:x86-64")
	if err != nil {
		t.Fatalf("parseObjectFormat() returned an error: %v", err)
	}
	if got != "pe-bigobj-x86-64" {
		t.Fatalf("parseObjectFormat() = %q, want pe-bigobj-x86-64", got)
	}
	if _, err := parseObjectFormat("architecture: i386:x86-64"); err == nil {
		t.Fatal("parseObjectFormat() accepted output without a format")
	}
}
