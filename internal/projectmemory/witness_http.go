package projectmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPMonotonicWitness is a client for a separately operated linearizable authority that forbids rollback and deletion.
type HTTPMonotonicWitness struct {
	base       *url.URL
	credential string
	client     *http.Client
	maxBytes   int64
}

// witnessAdvanceRequest carries both sides of the authority's atomic compare-and-advance operation.
type witnessAdvanceRequest struct {
	Expected WitnessState `json:"expected"`
	Next     WitnessState `json:"next"`
}

// NewHTTPMonotonicWitness validates a dedicated HTTPS authority endpoint; loopback HTTP is allowed for hermetic tests.
func NewHTTPMonotonicWitness(endpoint, credential string, client *http.Client) (*HTTPMonotonicWitness, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || credential == "" || client == nil {
		return nil, errors.New("invalid monotonic witness configuration")
	}
	host := parsed.Hostname()
	if parsed.Scheme != "https" {
		address := net.ParseIP(host)
		if parsed.Scheme != "http" || address == nil || !address.IsLoopback() {
			return nil, errors.New("monotonic witness requires HTTPS")
		}
	}
	clone := *client
	clone.CheckRedirect = denyWitnessRedirect
	if clone.Timeout == 0 {
		clone.Timeout = 10 * time.Second
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &HTTPMonotonicWitness{base: parsed, credential: credential, client: &clone, maxBytes: 64 << 10}, nil
}

// denyWitnessRedirect prevents authority credentials and CAS operations from crossing the configured origin.
func denyWitnessRedirect(*http.Request, []*http.Request) error {
	return errors.New("witness redirects denied")
}

// Read obtains the exact current authority state and never interprets missing as generation zero.
func (witness *HTTPMonotonicWitness) Read(ctx context.Context, project string) (WitnessState, error) {
	return witness.stateRequest(ctx, http.MethodGet, project, "", nil)
}

// Bootstrap invokes the separately authorized create-once operation for a demonstrably virgin stream.
func (witness *HTTPMonotonicWitness) Bootstrap(ctx context.Context, project string) (WitnessState, error) {
	return witness.stateRequest(ctx, http.MethodPost, project, "bootstrap", map[string]string{"project_id": project})
}

// CompareAndAdvance delegates the only permitted monotonic mutation to one atomic external operation.
func (witness *HTTPMonotonicWitness) CompareAndAdvance(ctx context.Context, expected, next WitnessState) error {
	state, err := witness.stateRequest(ctx, http.MethodPost, expected.ProjectID, "advance", witnessAdvanceRequest{Expected: expected, Next: next})
	if err != nil {
		return err
	}
	if state != next {
		return errors.New("witness returned an incompatible state")
	}
	return nil
}

// stateRequest applies transport bounds, strict decoding, authentication, and typed authority outcomes.
func (witness *HTTPMonotonicWitness) stateRequest(ctx context.Context, method, project, operation string, body any) (WitnessState, error) {
	if !safeID.MatchString(project) {
		return WitnessState{}, errors.New("invalid witness project")
	}
	endpoint := *witness.base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/project-memory-witness/" + url.PathEscape(project)
	if operation != "" {
		endpoint.Path += "/" + operation
	}
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return WitnessState{}, err
		}
		input = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), input)
	if err != nil {
		return WitnessState{}, err
	}
	request.Header.Set("Authorization", "Bearer "+witness.credential)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := witness.client.Do(request)
	if err != nil {
		return WitnessState{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return WitnessState{}, ErrWitnessMissing
	}
	if response.StatusCode == http.StatusConflict {
		return WitnessState{}, ErrWitnessConflict
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return WitnessState{}, fmt.Errorf("witness status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, witness.maxBytes+1))
	if err != nil || int64(len(raw)) > witness.maxBytes {
		return WitnessState{}, errors.New("invalid witness response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state WitnessState
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF || state.ProjectID != project {
		return WitnessState{}, errors.New("invalid witness response")
	}
	return state, nil
}
