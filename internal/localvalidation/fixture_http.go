package localvalidation

import (
	"fmt"

	"github.com/asierhr/featureProof/internal/planmanifest"
	"github.com/asierhr/featureProof/internal/specification"
	"github.com/asierhr/featureProof/internal/testidentity"
	httptool "github.com/asierhr/featureProof/internal/tools/http"
)

// BuildFixtureHTTPRuntime constructs the same controlled HTTP and late identity boundaries used by validation execution.
func BuildFixtureHTTPRuntime(spec specification.Specification, manifest planmanifest.Manifest, deps RuntimeDependencies) (*httptool.Tool, *testidentity.Catalog, error) {
	if deps.Clock == nil || deps.Resolver == nil || deps.Dialer == nil || deps.WithTimeout == nil {
		return nil, nil, fmt.Errorf("fixture HTTP dependencies are required")
	}
	identities, err := testidentity.NewCatalog(spec.Identities)
	if err != nil {
		return nil, nil, err
	}
	var authentication *testidentity.Transport
	if len(spec.Identities) > 0 {
		if deps.IdentityObserver == nil {
			return nil, nil, fmt.Errorf("fixture identity observer is required")
		}
		for _, identity := range spec.Identities {
			if deps.CredentialResolvers[identity.CredentialRef] == nil {
				return nil, nil, fmt.Errorf("fixture credential resolver is required")
			}
		}
		h := manifest.HTTP
		authentication, err = testidentity.NewTransport(testidentity.TransportConfig{Catalog: identities, Resolver: credentialRouter{resolvers: deps.CredentialResolvers}, Clock: deps.Clock, Observer: deps.IdentityObserver, ObservationTimeout: deps.IdentityObservationTimeout, MaxHeaderBytes: h.MaxRequestHeaderBytes})
		if err != nil {
			return nil, nil, err
		}
	}
	h := manifest.HTTP
	tool, err := httptool.New(httptool.Config{Target: spec.Target, AllowedMethods: h.AllowedMethods, AllowedRequestHeaders: h.AllowedRequestHeaders, AllowedResponseHeaders: h.AllowedResponseHeaders, FollowRedirectsForMethods: h.FollowRedirectsForMethods, MaxRedirects: h.MaxRedirects, Timeout: h.Timeout(), MaxURLBytes: h.MaxURLBytes, MaxRequestHeaderBytes: h.MaxRequestHeaderBytes, MaxRequestBodyBytes: h.MaxRequestBodyBytes, MaxResponseHeaderBytes: h.MaxResponseHeaderBytes, MaxResponseBodyBytes: h.MaxResponseBodyBytes, Resolver: deps.Resolver, Dialer: deps.Dialer, Clock: deps.Clock, WithTimeout: deps.WithTimeout, Authentication: authentication})
	if err != nil {
		return nil, nil, err
	}
	return tool, identities, nil
}
