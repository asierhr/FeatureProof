package planmanifest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asierhr/featureProof/internal/specification"
)

// identityManifestSpecification returns a v2 specification whose root HTTP operation requires explicit identity selection.
func identityManifestSpecification() specification.Specification {
	return specification.Specification{Version: 2, Target: specification.Target{ID: "deployed"}, Constraints: []specification.Constraint{{ID: "c"}}, Identities: []specification.TestIdentity{{ID: "customer", TargetID: "deployed", CredentialRef: "cred_customer", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "GET", Path: "/"}}}}}
}

// identityManifest converts the existing strict fixture to v2 and optionally supplies its explicit identity ID.
func identityManifest(identityID string) string {
	raw := strings.Replace(validManifest("null"), `"version":1`, `"version":2`, 1)
	if identityID != "" {
		raw = strings.Replace(raw, `"tool":"http.request"`, `"tool":"http.request","identity_id":"`+identityID+`"`, 1)
	}
	return raw
}

// TestManifestV2RequiresAndSealsExactIdentityBinding proves manifest selection is derived against the trusted specification.
func TestManifestV2RequiresAndSealsExactIdentityBinding(t *testing.T) {
	manifest, err := Load(context.Background(), writeManifest(t, identityManifest("customer")), identityManifestSpecification())
	if err != nil {
		t.Fatal(err)
	}
	binding := manifest.Plans[0].Steps[0].IdentityBinding
	if manifest.Version != 2 || binding == nil || binding.IdentityID != "customer" || len(binding.Digest) != 64 {
		t.Fatalf("unsealed manifest: %#v", manifest)
	}
	for _, id := range []string{"", "invented"} {
		_, err := Load(context.Background(), writeManifest(t, identityManifest(id)), identityManifestSpecification())
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("identity %q accepted: %v", id, err)
		}
	}
}

// TestManifestV1DoesNotReinterpretIdentityID proves the old contract rejects the new member instead of silently accepting it.
func TestManifestV1DoesNotReinterpretIdentityID(t *testing.T) {
	raw := strings.Replace(validManifest("null"), `"tool":"http.request"`, `"tool":"http.request","identity_id":"customer"`, 1)
	_, err := Load(context.Background(), writeManifest(t, raw), specification.Specification{Version: 1, Constraints: []specification.Constraint{{ID: "c"}}})
	var typed *Error
	if !errors.As(err, &typed) || typed.Category != "unknown_field" {
		t.Fatalf("v1 identity member result: %v", err)
	}
}

// TestManifestV2RejectsReferencedAuthenticatedOperationBeforeExecution proves unresolved method/path cannot bypass identity binding.
func TestManifestV2RejectsReferencedAuthenticatedOperationBeforeExecution(t *testing.T) {
	reference := `{"reference":{"step_id":"source","path":[{"field":"value"}],"value_type":"string"}}`
	for _, test := range []struct {
		name, identityID, field string
	}{
		{name: "method reference with identity", identityID: "customer", field: "method"},
		{name: "path reference with identity", identityID: "customer", field: "path"},
		{name: "method reference without identity", field: "method"},
		{name: "path reference with unknown identity", identityID: "invented", field: "path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := identityManifest(test.identityID)
			literal := `{"literal":"GET"}`
			if test.field == "path" {
				literal = `{"literal":"/"}`
			}
			raw = strings.Replace(raw, `"`+test.field+`":`+literal, `"`+test.field+`":`+reference, 1)
			_, err := Load(context.Background(), writeManifest(t, raw), identityManifestSpecification())
			var typed *Error
			if !errors.As(err, &typed) || typed.Category != "invalid_relation" || !strings.HasSuffix(typed.Field, "input."+test.field) {
				t.Fatalf("referenced %s accepted: %v", test.field, err)
			}
		})
	}
}
