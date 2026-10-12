package jsonpath

import "testing"

// TestParseAcceptsCanonicalPointersAndEscapes covers the shared selector grammar and rendering.
func TestParseAcceptsCanonicalPointersAndEscapes(t *testing.T) {
	for path, expected := range map[string][]string{"": nil, "/a/0/b": {"a", "0", "b"}, "/a~1b/~0key": {"a/b", "~key"}} {
		tokens, err := Parse(path)
		if err != nil || Join(tokens) != path || len(tokens) != len(expected) {
			t.Fatalf("%s: %#v %v", path, tokens, err)
		}
		for i := range tokens {
			if tokens[i] != expected[i] {
				t.Fatalf("%s: %#v", path, tokens)
			}
		}
	}
}

// TestParseRejectsMalformedAndNonCanonicalPointers covers every invalid lexical family.
func TestParseRejectsMalformedAndNonCanonicalPointers(t *testing.T) {
	for _, path := range []string{"relative", "/bad~", "/bad~2", "/+1", "/01", "/-1"} {
		if _, err := Parse(path); err == nil {
			t.Fatalf("invalid path accepted: %s", path)
		}
	}
}

// TestOverlapsRequiresACommonAncestorChain distinguishes siblings from ancestors and descendants.
func TestOverlapsRequiresACommonAncestorChain(t *testing.T) {
	if !Overlaps(nil, []string{"a"}) || !Overlaps([]string{"a"}, []string{"a", "b"}) || !Overlaps([]string{"a", "b"}, []string{"a"}) || Overlaps([]string{"a"}, []string{"b"}) {
		t.Fatal("unexpected overlap result")
	}
}
