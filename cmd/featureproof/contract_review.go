package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/asierhr/featureProof/internal/contractcatalog"
	"github.com/asierhr/featureProof/internal/contractreview"
)

// contractReviewCatalogConfig binds one exact reference to trusted identity and pre-authorized neutral sources.
type contractReviewCatalogConfig struct {
	Identity          contractreview.CatalogIdentity    `json:"identity"`
	Kind              string                            `json:"kind"`
	ApprovedReference *contractreview.ApprovedReference `json:"approved_reference,omitempty"`
	Analyzers         []contractreview.AnalyzerIdentity `json:"analyzers"`
	Sources           []contractcatalog.SourceSnapshot  `json:"sources"`
}

// contractReviewConfig is the local trusted composition file and never originates from a review command.
type contractReviewConfig struct {
	Version, Principal, ExportRoot, Title, DocumentVersion, PolicyDigest string
	Candidates                                                           map[string]contractReviewCatalogConfig
	Baselines                                                            map[string]contractReviewCatalogConfig
	ProjectionPolicy                                                     contractcatalog.ProjectionPolicy
}

// configuredReviewResolver serves only exact aliases materialized and verified during process composition.
type configuredReviewResolver struct {
	candidates map[string]contractreview.CandidateSnapshot
	baselines  map[string]contractreview.BaselineSnapshot
}

// ResolveCandidate returns an isolated configured candidate without filesystem or network resolution during review.
func (r configuredReviewResolver) ResolveCandidate(_ context.Context, reference string) (contractreview.CandidateSnapshot, error) {
	value, ok := r.candidates[reference]
	if !ok {
		return contractreview.CandidateSnapshot{}, errors.New("candidate unavailable")
	}
	return value, nil
}

// ResolveBaseline returns only the exact explicitly selected approved baseline alias.
func (r configuredReviewResolver) ResolveBaseline(_ context.Context, reference string) (contractreview.BaselineSnapshot, error) {
	value, ok := r.baselines[reference]
	if !ok {
		return contractreview.BaselineSnapshot{}, errors.New("baseline unavailable")
	}
	return value, nil
}

// configuredReviewAuthorizer permits the configured principal only for repositories present in configured catalogs.
type configuredReviewAuthorizer struct {
	principal    string
	repositories map[string]bool
}

// Authorize fails closed when either authenticated principal or repository falls outside trusted configuration.
func (a configuredReviewAuthorizer) Authorize(_ context.Context, principal string, _ contractreview.Action, repository, _ string) error {
	if principal != a.principal || !a.repositories[repository] {
		return errors.New("unauthorized")
	}
	return nil
}

// reviewWallClock supplies trusted UTC wall time to local review audit events.
type reviewWallClock struct{}

// Now returns the operating system clock normalized to UTC.
func (reviewWallClock) Now() time.Time { return time.Now().UTC() }

// composeContractReviewCLI loads trusted local composition and builds the shared review service.
func composeContractReviewCLI(ctx context.Context) (contractreview.CLIAdapter, error) {
	path := os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_CONFIG")
	if path == "" {
		path = filepath.Join(".featureproof", "contract-review-config.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return contractreview.CLIAdapter{}, err
	}
	var config contractReviewConfig
	if json.Unmarshal(raw, &config) != nil || config.Version != contractreview.VersionV1 {
		return contractreview.CLIAdapter{}, errors.New("invalid contract review config")
	}
	limits := contractcatalog.Limits{MaxSources: 32, MaxOperations: 4096, MaxClaims: 65536, MaxBytes: 32 << 20, MaxDepth: 64, MaxDiagnostics: 4096, MaxLocationBytes: 512}
	projection, policyErr := contractcatalog.NewProjectionPolicy(config.ProjectionPolicy)
	if policyErr != nil {
		return contractreview.CLIAdapter{}, errors.New("invalid contract review projection policy")
	}
	if len(projection.SensitiveNames) == 0 || len(projection.SensitiveHeaderNames) == 0 || len(projection.ForbiddenValues) == 0 {
		return contractreview.CLIAdapter{}, errors.New("incomplete contract review projection policy")
	}
	policyRaw, _ := json.Marshal(projection)
	policySum := sha256.Sum256(policyRaw)
	if hex.EncodeToString(policySum[:]) != config.PolicyDigest {
		return contractreview.CLIAdapter{}, errors.New("contract review policy digest mismatch")
	}
	resolver := configuredReviewResolver{candidates: map[string]contractreview.CandidateSnapshot{}, baselines: map[string]contractreview.BaselineSnapshot{}}
	repositories := map[string]bool{}
	for reference, value := range config.Candidates {
		catalog, buildErr := buildConfiguredReviewCatalog(ctx, value.Sources, limits, projection)
		if buildErr != nil {
			return contractreview.CLIAdapter{}, buildErr
		}
		if value.Identity.Digest != catalog.Digest() {
			return contractreview.CLIAdapter{}, errors.New("candidate digest mismatch")
		}
		resolver.candidates[reference] = contractreview.CandidateSnapshot{Identity: value.Identity, Analyzers: value.Analyzers, Catalog: catalog}
		repositories[value.Identity.RepositoryID] = true
	}
	for reference, value := range config.Baselines {
		catalog, buildErr := buildConfiguredReviewCatalog(ctx, value.Sources, limits, projection)
		if buildErr != nil {
			return contractreview.CLIAdapter{}, buildErr
		}
		if value.Identity.Digest != catalog.Digest() {
			return contractreview.CLIAdapter{}, errors.New("baseline digest mismatch")
		}
		resolver.baselines[reference] = contractreview.BaselineSnapshot{Identity: value.Identity, Kind: value.Kind, ApprovedReference: value.ApprovedReference, Catalog: catalog}
		repositories[value.Identity.RepositoryID] = true
	}
	root, err := os.Getwd()
	if err != nil {
		return contractreview.CLIAdapter{}, err
	}
	store, err := contractreview.NewLocalStore(filepath.Join(root, ".featureproof", "contract-reviews"))
	if err != nil {
		return contractreview.CLIAdapter{}, err
	}
	exportRoot := filepath.Join(root, config.ExportRoot)
	publisher, err := contractreview.NewLocalPublisher(exportRoot)
	if err != nil {
		_ = store.Close()
		return contractreview.CLIAdapter{}, err
	}
	service := contractreview.Service{Authorizer: configuredReviewAuthorizer{config.Principal, repositories}, Resolver: resolver, Policy: contractreview.FixedPolicyAuthority(config.PolicyDigest), Clock: reviewWallClock{}, Store: store, Exporter: contractreview.Exporter{}, Publisher: publisher}
	reviewLimits := contractreview.Limits{MaxOperations: 4096, MaxItems: 32768, MaxSchemaDepth: 64, MaxSchemaNodes: 65536, MaxSources: 32, MaxProvenance: 256, MaxDiagnostics: 4096, MaxBytes: 32 << 20}
	return contractreview.CLIAdapter{API: contractreview.APIAdapter{Service: service, Config: contractreview.SurfaceConfig{PrincipalID: config.Principal, PolicyDigest: config.PolicyDigest, Limits: reviewLimits, Info: contractreview.ExportInfo{Title: config.Title, Version: config.DocumentVersion}}}}, nil
}

// buildConfiguredReviewCatalog validates every configured source through the neutral registry before use.
func buildConfiguredReviewCatalog(ctx context.Context, snapshots []contractcatalog.SourceSnapshot, limits contractcatalog.Limits, projection contractcatalog.ProjectionPolicy) (contractcatalog.OperationCatalog, error) {
	routes := map[string]contractcatalog.ContractSource{}
	requests := make([]contractcatalog.SourceRequest, 0, len(snapshots))
	for index := range snapshots {
		snapshot := snapshots[index]
		routes[snapshot.Source.Kind] = &contractcatalog.StaticSource{Snapshot: snapshot}
		requests = append(requests, contractcatalog.NewSourceRequest(contractcatalog.SourceDescriptor{Kind: snapshot.Source.Kind, ID: snapshot.Source.ID, Class: snapshot.Source.Class, Confidence: snapshot.Source.Confidence, Completeness: snapshot.Source.Completeness, ExpectedDigest: snapshot.Source.Digest, AdapterVersion: snapshot.Source.AdapterVersion, Limits: limits, Projection: projection}))
	}
	registry, err := contractcatalog.NewRegistry(routes)
	if err != nil {
		return contractcatalog.OperationCatalog{}, err
	}
	loaded, err := registry.Load(ctx, requests, limits)
	if err != nil {
		return contractcatalog.OperationCatalog{}, err
	}
	return contractcatalog.Build(ctx, loaded, limits)
}

// runContractReviewCommand composes local authority and writes only safe versioned CLI output.
func runContractReviewCommand(ctx context.Context, args []string, output io.Writer) int {
	cli, err := composeContractReviewCLI(ctx)
	if err != nil {
		return 3
	}
	defer cli.Close()
	if err = cli.Run(ctx, args, output); err != nil {
		return 2
	}
	return 0
}

// runContractReviewMCP composes the same durable service as CLI and serves all lifecycle tools over standard I/O.
func runContractReviewMCP(ctx context.Context, input io.Reader, output io.Writer) int {
	cli, err := composeContractReviewCLI(ctx)
	if err != nil {
		return 3
	}
	defer cli.Close()
	server := contractreview.MCPServer{Adapter: contractreview.MCPAdapter{API: cli.API}, MaxLine: 1 << 20}
	if err = server.Serve(ctx, input, output); err != nil {
		return 3
	}
	return 0
}

// runContractReviewAPI composes the shared durable service and owns bounded HTTP shutdown.
func runContractReviewAPI(ctx context.Context) int {
	cli, err := composeContractReviewCLI(ctx)
	if err != nil {
		return 3
	}
	defer cli.Close()
	address := os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_ADDRESS")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	host, _, splitErr := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	token := []byte(os.Getenv("FEATUREPROOF_CONTRACT_REVIEW_API_TOKEN"))
	if splitErr != nil || ip == nil || !ip.IsLoopback() || len(token) < 16 {
		return 3
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return 3
	}
	return serveContractReviewAPI(ctx, cli.API, token, listener)
}

// serveContractReviewAPI serves the product HTTP adapter on an already-bound loopback listener and joins shutdown.
func serveContractReviewAPI(ctx context.Context, api contractreview.APIAdapter, token []byte, listener net.Listener) int {
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
		_ = listener.Close()
		return 3
	}
	authenticator := contractreview.BearerAuthenticator{Token: token, Principal: api.Config.PrincipalID}
	server := &http.Server{Handler: (contractreview.HTTPHandler{API: api, MaxBody: 1 << 20, Auth: authenticator}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return 3
		}
		return 0
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			return 3
		}
		return 0
	}
}
