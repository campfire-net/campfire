package encoding

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

var encMode cbor.EncMode
var decMode cbor.DecMode

func init() {
	var err error
	encMode, err = cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("initializing CBOR encoder: %v", err))
	}
	decMode, err = cbor.DecOptions{}.DecMode()
	if err != nil {
		panic(fmt.Sprintf("initializing CBOR decoder: %v", err))
	}
}

// Marshal encodes a value using CBOR Core Deterministic Encoding.
func Marshal(v interface{}) ([]byte, error) {
	return encMode.Marshal(v)
}

// Unmarshal decodes CBOR data into a value.
func Unmarshal(data []byte, v interface{}) error {
	return decMode.Unmarshal(data, v)
}
