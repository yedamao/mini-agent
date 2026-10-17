package buggy

import "testing"

func TestReverseWords(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"go", "go"},
		{"hello world", "world hello"},
		{"hello big world", "world big hello"},
		{"  extra   spaces  ", "spaces extra"},
	}
	for _, tt := range tests {
		if got := ReverseWords(tt.in); got != tt.want {
			t.Errorf("ReverseWords(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
