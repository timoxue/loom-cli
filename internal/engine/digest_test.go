package engine

import (
	"errors"
	"testing"
)

func TestInputDigestDeterministic(t *testing.T) {
	t.Parallel()

	inputs := map[string]any{
		"a": "alice",
		"b": 42,
		"c": true,
	}

	first, err := ComputeInputDigest(inputs)
	if err != nil {
		t.Fatalf("first digest error = %v", err)
	}
	second, err := ComputeInputDigest(inputs)
	if err != nil {
		t.Fatalf("second digest error = %v", err)
	}
	if first != second {
		t.Fatalf("digest not deterministic: %q != %q", first, second)
	}
}

func TestInputDigestIgnoresKeyOrdering(t *testing.T) {
	t.Parallel()

	// Go map iteration is randomized but the digest must not be. Run 50
	// times to get reasonable coverage of iteration order permutations.
	baseline, err := ComputeInputDigest(map[string]any{"a": "x", "b": "y", "c": "z"})
	if err != nil {
		t.Fatalf("baseline error = %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := ComputeInputDigest(map[string]any{"c": "z", "a": "x", "b": "y"})
		if err != nil {
			t.Fatalf("iteration %d error = %v", i, err)
		}
		if got != baseline {
			t.Fatalf("iteration %d produced %q, want %q", i, got, baseline)
		}
	}
}

func TestInputDigestDistinguishesIntFromFloat(t *testing.T) {
	t.Parallel()

	// This is the whole point of type-aware canonicalization: int(2)
	// and float64(2) must NOT produce the same digest, because downstream
	// handlers will observe different types for that same key.
	intDigest, err := ComputeInputDigest(map[string]any{"n": int(2)})
	if err != nil {
		t.Fatalf("int digest error = %v", err)
	}
	floatDigest, err := ComputeInputDigest(map[string]any{"n": float64(2)})
	if err != nil {
		t.Fatalf("float digest error = %v", err)
	}
	if intDigest == floatDigest {
		t.Fatalf("digests collide across int/float: %q (would defeat type-tagging)", intDigest)
	}
}

func TestInputDigestDistinguishesStringFromBool(t *testing.T) {
	t.Parallel()

	// "true" and bool(true) are semantically different inputs; their
	// digests must reflect that.
	stringDigest, err := ComputeInputDigest(map[string]any{"flag": "true"})
	if err != nil {
		t.Fatalf("string digest error = %v", err)
	}
	boolDigest, err := ComputeInputDigest(map[string]any{"flag": true})
	if err != nil {
		t.Fatalf("bool digest error = %v", err)
	}
	if stringDigest == boolDigest {
		t.Fatalf("digest collapsed \"true\" and bool(true) to same fingerprint")
	}
}

func TestInputDigestRejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	_, err := ComputeInputDigest(map[string]any{"arr": []string{"a"}})
	if err == nil {
		t.Fatal("ComputeInputDigest error = nil, want UnsupportedInputTypeError")
	}
	var unsupportedErr *UnsupportedInputTypeError
	if !errors.As(err, &unsupportedErr) {
		t.Fatalf("error type = %T, want *UnsupportedInputTypeError", err)
	}
}

func TestInputDigestEmptyInputsIsStable(t *testing.T) {
	t.Parallel()

	digest1, err := ComputeInputDigest(nil)
	if err != nil {
		t.Fatalf("nil inputs error = %v", err)
	}
	digest2, err := ComputeInputDigest(map[string]any{})
	if err != nil {
		t.Fatalf("empty inputs error = %v", err)
	}
	if digest1 != digest2 {
		t.Fatalf("nil and empty inputs produced different digests: %q vs %q", digest1, digest2)
	}
}
