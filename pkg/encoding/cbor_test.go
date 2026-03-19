package encoding

// Tests for workspace-97g: pkg/encoding/cbor.go has no tests.

import (
	"bytes"
	"testing"
)

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	type sample struct {
		Name  string `cbor:"1,keyasint"`
		Value int    `cbor:"2,keyasint"`
	}
	in := sample{Name: "campfire", Value: 42}

	data, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Marshal produced empty output")
	}

	var out sample
	if err := Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Name != in.Name {
		t.Errorf("Name = %q, want %q", out.Name, in.Name)
	}
	if out.Value != in.Value {
		t.Errorf("Value = %d, want %d", out.Value, in.Value)
	}
}

func TestMarshalDeterministic(t *testing.T) {
	type sample struct {
		A string `cbor:"1,keyasint"`
		B string `cbor:"2,keyasint"`
	}
	v := sample{A: "hello", B: "world"}

	data1, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal (1): %v", err)
	}
	data2, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal (2): %v", err)
	}
	if !bytes.Equal(data1, data2) {
		t.Error("Marshal is not deterministic: two calls produced different bytes")
	}
}

func TestUnmarshalInvalidData(t *testing.T) {
	var out struct{ Name string }
	err := Unmarshal([]byte("not valid cbor data!!!"), &out)
	if err == nil {
		t.Error("Unmarshal should fail on invalid CBOR data")
	}
}

func TestMarshalNilSlice(t *testing.T) {
	type sample struct {
		Items []string `cbor:"1,keyasint"`
	}
	v := sample{Items: nil}
	data, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal nil slice: %v", err)
	}
	var out sample
	if err := Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal nil slice: %v", err)
	}
}

func TestMarshalEmptyStruct(t *testing.T) {
	type empty struct{}
	data, err := Marshal(empty{})
	if err != nil {
		t.Fatalf("Marshal empty struct: %v", err)
	}
	var out empty
	if err := Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal empty struct: %v", err)
	}
}

func TestMarshalByteSlice(t *testing.T) {
	input := []byte{0x01, 0x02, 0x03, 0xFF}
	data, err := Marshal(input)
	if err != nil {
		t.Fatalf("Marshal []byte: %v", err)
	}
	var output []byte
	if err := Unmarshal(data, &output); err != nil {
		t.Fatalf("Unmarshal []byte: %v", err)
	}
	if !bytes.Equal(output, input) {
		t.Errorf("round-tripped bytes = %v, want %v", output, input)
	}
}
