package localvalidation

import (
	"bytes"
	"testing"
	"time"

	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
)

// fixtureHTTPManifest returns the finite controlled transport limits required by productive fixture composition.
func fixtureHTTPManifest() planmanifest.Manifest {
	return planmanifest.Manifest{HTTP: planmanifest.HTTPConfig{AllowedMethods: []string{"POST"}, AllowedRequestHeaders: []string{}, AllowedResponseHeaders: []string{"Content-Type"}, FollowRedirectsForMethods: []string{}, MaxRedirects: 0, TimeoutMS: 1000, MaxURLBytes: 2048, MaxRequestHeaderBytes: 4096, MaxRequestBodyBytes: 4096, MaxResponseHeaderBytes: 4096, MaxResponseBodyBytes: 4096}}
}

// fixtureHTTPSpec returns one target and optional late-bound identity accepted by the controlled HTTP runtime.
func fixtureHTTPSpec(withIdentity bool) specification.Specification {
	spec := specification.Specification{Target: specification.Target{ID: "deployed", BaseURL: "https://example.test"}}
	if withIdentity {
		spec.Identities = []specification.TestIdentity{{ID: "writer", TargetID: "deployed", CredentialRef: "credential", Mechanism: specification.AuthenticationMechanism{Type: "bearer"}, Operations: []specification.AuthorizedOperation{{Capability: "http.request", Method: "POST", Path: "/resources"}}}}
	}
	return spec
}

// TestBuildFixtureHTTPRuntimeCoversTrustedCompositionMatrix verifies mandatory network, identity, resolver, observer, and tool boundaries.
func TestBuildFixtureHTTPRuntimeCoversTrustedCompositionMatrix(t *testing.T) {
	if _, _, err := BuildFixtureHTTPRuntime(fixtureHTTPSpec(false), fixtureHTTPManifest(), RuntimeDependencies{}); err == nil {
		t.Fatal("missing controlled HTTP dependencies accepted")
	}
	deps := DefaultDependencies(t.TempDir(), &bytes.Buffer{}, &bytes.Buffer{})
	if tool, identities, err := BuildFixtureHTTPRuntime(fixtureHTTPSpec(false), fixtureHTTPManifest(), deps); err != nil || tool == nil || identities == nil {
		t.Fatalf("anonymous controlled HTTP composition failed: tool=%v identities=%v err=%v", tool, identities, err)
	}
	invalid := fixtureHTTPSpec(true)
	invalid.Identities = append(invalid.Identities, invalid.Identities[0])
	if _, _, err := BuildFixtureHTTPRuntime(invalid, fixtureHTTPManifest(), deps); err == nil {
		t.Fatal("invalid identity catalog accepted")
	}
	if _, _, err := BuildFixtureHTTPRuntime(fixtureHTTPSpec(true), fixtureHTTPManifest(), deps); err == nil {
		t.Fatal("missing identity observer accepted")
	}
	deps.IdentityObserver = &localIdentityObserver{}
	if _, _, err := BuildFixtureHTTPRuntime(fixtureHTTPSpec(true), fixtureHTTPManifest(), deps); err == nil {
		t.Fatal("missing credential resolver accepted")
	}
	deps.CredentialResolvers = map[string]testidentity.CredentialResolver{"credential": &localCredentialResolver{}}
	deps.IdentityObservationTimeout = time.Second
	tool, identities, err := BuildFixtureHTTPRuntime(fixtureHTTPSpec(true), fixtureHTTPManifest(), deps)
	if err != nil || tool == nil || identities == nil {
		t.Fatalf("authenticated controlled HTTP composition failed: tool=%v identities=%v err=%v", tool, identities, err)
	}
	if _, _, err := BuildFixtureHTTPRuntime(fixtureHTTPSpec(true), planmanifest.Manifest{}, deps); err == nil {
		t.Fatal("invalid HTTP authority accepted")
	}
}
