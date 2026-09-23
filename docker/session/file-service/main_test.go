package main

import "testing"

func TestSafeFilename(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"report.pdf", true},
		{"中文 文件.txt", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../secret", false},
		{"dir/file", false},
		{`dir\file`, false},
		{"bad\x00name", false},
	}
	for _, tt := range tests {
		if _, ok := safeFilename(tt.name); ok != tt.ok {
			t.Errorf("safeFilename(%q) ok = %v, want %v", tt.name, ok, tt.ok)
		}
	}
}

func TestParsePositiveInt64(t *testing.T) {
	for _, raw := range []string{"", "0", "-1", "not-a-number"} {
		if _, err := parsePositiveInt64(raw); err == nil {
			t.Errorf("parsePositiveInt64(%q) unexpectedly succeeded", raw)
		}
	}
	if got, err := parsePositiveInt64("104857600"); err != nil || got != 104857600 {
		t.Fatalf("parsePositiveInt64 valid value = %d, %v", got, err)
	}
}
