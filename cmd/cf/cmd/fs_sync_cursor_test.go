package cmd

// campfireagent-e58 / campfire-d80: the incremental filesystem sync now lives in
// the protocol package (protocol.SyncFilesystem), with end-to-end incrementality
// proven by protocol.TestSyncIfFilesystem_Incremental. The cmd-side functions are
// thin shims over it. What remains worth testing here is the shared lookback-window
// env override that both the SDK and CLI sync paths read.

import (
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// TestLoadFSSyncLookback verifies the CF_FS_SYNC_LOOKBACK_MS env override
// (shared loader in the fs package, used by both sync paths).
func TestLoadFSSyncLookback(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 2 * time.Second}, // unset → default
		{"0", 0},              // strict cursor
		{"500", 500 * time.Millisecond},
		{"-5", 2 * time.Second},         // negative → default (invalid)
		{"notanumber", 2 * time.Second}, // garbage → default
	}
	for _, c := range cases {
		t.Setenv("CF_FS_SYNC_LOOKBACK_MS", c.env)
		if c.env == "" {
			os.Unsetenv("CF_FS_SYNC_LOOKBACK_MS")
		}
		if got := fs.SyncLookbackFromEnv(); got != c.want {
			t.Errorf("CF_FS_SYNC_LOOKBACK_MS=%q: got %v, want %v", c.env, got, c.want)
		}
	}
}
