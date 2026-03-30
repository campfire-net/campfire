package cmd

import "testing"

func TestConventionFlagsFromRawArgs(t *testing.T) {
	tests := []struct {
		name      string
		rawArgs   []string
		operation string
		want      []string
	}{
		{
			name:      "flags after operation",
			rawArgs:   []string{"cf", "7da512", "claim-item", "--item_id", "foo", "--description", "bar"},
			operation: "claim-item",
			want:      []string{"--item_id", "foo", "--description", "bar"},
		},
		{
			name:      "no flags",
			rawArgs:   []string{"cf", "7da512", "help"},
			operation: "help",
			want:      nil,
		},
		{
			name:      "with cf-home flag before operation",
			rawArgs:   []string{"cf", "--cf-home", "/tmp/test", "7da512", "claim-item", "--item_id", "x"},
			operation: "claim-item",
			want:      []string{"--item_id", "x"},
		},
		{
			name:      "operation not found",
			rawArgs:   []string{"cf", "7da512", "something-else"},
			operation: "claim-item",
			want:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conventionFlagsFromRawArgs(tt.rawArgs, tt.operation)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("arg[%d]: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
