package tracing

import "testing"

func TestParseTraceVerbose(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"abc", false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			if got := parseTraceVerbose(tt.value); got != tt.want {
				t.Fatalf("parseTraceVerbose(%q) = %v, muốn %v", tt.value, got, tt.want)
			}
		})
	}
}
