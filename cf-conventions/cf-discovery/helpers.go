package discovery

// helpers.go — internal utilities used across the cf-discovery package.

import "encoding/json"

// jsonUnmarshal is an alias for json.Unmarshal used by tier.go.
var jsonUnmarshal = json.Unmarshal
