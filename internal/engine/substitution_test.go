package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestSubstituteStringHappyPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		binds  map[string]any
		want   string
	}{
		{"no references", "hello world", nil, "hello world"},
		{"single string", "hello ${name}", map[string]any{"name": "alice"}, "hello alice"},
		{"multiple references", "${a}-${b}-${a}", map[string]any{"a": "x", "b": "y"}, "x-y-x"},
		{"mid-string", "prefix ${name} suffix", map[string]any{"name": "mid"}, "prefix mid suffix"},
		{"int value", "n=${n}", map[string]any{"n": 42}, "n=42"},
		{"bool value", "b=${b}", map[string]any{"b": true}, "b=true"},
		{"float value", "f=${f}", map[string]any{"f": 0.5}, "f=0.5"},
		{"literal dollar passes through", "price: $100", nil, "price: $100"},
		{"dollar followed by non-brace passes through", "a $foo b", nil, "a $foo b"},
		{"empty string", "", nil, ""},
		{"empty reference expansion", "${name}", map[string]any{"name": ""}, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := substituteString(tc.input, tc.binds)
			if err != nil {
				t.Fatalf("substituteString(%q) error = %v, want success", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("substituteString(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSubstituteStringUnknownVariableFailsFast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		binds map[string]any
		miss  string
	}{
		{"standalone", "${missing}", map[string]any{"other": "x"}, "missing"},
		{"mid-string", "hi ${missing} bye", map[string]any{"name": "x"}, "missing"},
		{"typo vs declared", "${nmae}", map[string]any{"name": "x"}, "nmae"},
		{"first of many", "${first}-${second}", map[string]any{"second": "y"}, "first"},
		{"empty bindings", "${anything}", nil, "anything"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := substituteString(tc.input, tc.binds)
			if err == nil {
				t.Fatalf("substituteString(%q) error = nil, want UnknownVariableError", tc.input)
			}

			var unknownErr *UnknownVariableError
			if !errors.As(err, &unknownErr) {
				t.Fatalf("error type = %T, want *UnknownVariableError", err)
			}
			if unknownErr.Name != tc.miss {
				t.Fatalf("UnknownVariableError.Name = %q, want %q", unknownErr.Name, tc.miss)
			}
			if !strings.Contains(err.Error(), tc.miss) {
				t.Fatalf("error message %q does not contain missing var %q", err.Error(), tc.miss)
			}
		})
	}
}

func TestSubstituteStringSyntaxErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		reason string
	}{
		{"unterminated", "hi ${name", "unterminated"},
		{"empty braces", "hi ${}", "invalid variable reference"},
		{"whitespace inside braces", "hi ${ name }", "invalid variable reference"},
		{"digit first char", "hi ${1x}", "invalid variable reference"},
		{"dash inside identifier", "hi ${a-b}", "invalid variable reference"},
		{"dot inside identifier", "hi ${a.b}", "invalid variable reference"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := substituteString(tc.input, map[string]any{"name": "x"})
			if err == nil {
				t.Fatalf("substituteString(%q) error = nil, want contract error", tc.input)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.reason)
			}
		})
	}
}

func TestSubstituteStringRejectsNonPrimitiveInput(t *testing.T) {
	t.Parallel()

	// If sanitizer ever leaks a non-primitive input, substitution must
	// fail loudly rather than emit an arbitrary fmt.Sprint form.
	_, err := substituteString("${arr}", map[string]any{"arr": []string{"a", "b"}})
	if err == nil {
		t.Fatal("substituteString error = nil, want UnsupportedInputTypeError")
	}
	var unsupportedErr *UnsupportedInputTypeError
	if !errors.As(err, &unsupportedErr) {
		t.Fatalf("error type = %T, want *UnsupportedInputTypeError", err)
	}
}
