package specyaml

import (
	"errors"
	"testing"
)

func TestSyntaxErrorPreservesOriginalCauseIdentity(t *testing.T) {
	cause := errors.New("identifiable parser cause")

	err := syntaxError(cause, 7, 3)
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(error, cause) = false; error = %v", err)
	}
}
