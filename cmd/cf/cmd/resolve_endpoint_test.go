package cmd

import "testing"

func TestResolveEndpoint_HTTP(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		want   string
	}{
		{name: "port-only", listen: ":9001", want: "http://localhost:9001"},
		{name: "host-port", listen: "example.com:9001", want: "http://example.com:9001"},
		{name: "ip-port", listen: "192.168.1.1:8080", want: "http://192.168.1.1:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEndpoint(tt.listen, false)
			if got != tt.want {
				t.Errorf("resolveEndpoint(%q, false) = %q, want %q", tt.listen, got, tt.want)
			}
		})
	}
}

func TestResolveEndpoint_HTTPS(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		want   string
	}{
		{name: "port-only", listen: ":9001", want: "https://localhost:9001"},
		{name: "host-port", listen: "example.com:9001", want: "https://example.com:9001"},
		{name: "ip-port", listen: "192.168.1.1:8080", want: "https://192.168.1.1:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveEndpoint(tt.listen, true)
			if got != tt.want {
				t.Errorf("resolveEndpoint(%q, true) = %q, want %q", tt.listen, got, tt.want)
			}
		})
	}
}
