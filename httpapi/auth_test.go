package httpapi

import "testing"

func TestValidBearer(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
		want     bool
	}{
		{name: "valid", header: "Bearer abc123", expected: "abc123", want: true},
		{name: "case insensitive prefix", header: "bearer abc123", expected: "abc123", want: true},
		{name: "wrong token", header: "Bearer wrong", expected: "abc123", want: false},
		{name: "missing bearer", header: "abc123", expected: "abc123", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validBearer(tt.header, tt.expected); got != tt.want {
				t.Fatalf("validBearer() = %v, want %v", got, tt.want)
			}
		})
	}
}
