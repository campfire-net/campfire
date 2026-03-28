package forge

import (
	"encoding/json"
	"io"
	"time"
)

// decodeJSON decodes JSON from r into dst and closes r.
func decodeJSON(r io.ReadCloser, dst any) error {
	defer r.Close()
	return json.NewDecoder(r).Decode(dst)
}

// waitCh returns a channel that receives after d has elapsed.
// Used in retry loops to allow context cancellation.
func waitCh(d time.Duration) <-chan time.Time {
	return time.After(d)
}
