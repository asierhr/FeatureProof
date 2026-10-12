package httptool

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/asierhr/featureProof/internal/harness"
)

var errResponseHeaderLimit = errors.New("response header limit exceeded")
var errRequestHeaderLimit = errors.New("request header limit exceeded")
var errRequestHeaderMeasured = errors.New("request header measurement complete")

// requestHeaderCounter measures the HTTP/1.1 header block while stopping before body bytes are copied.
type requestHeaderCounter struct {
	limit       int
	count       int
	lineMatched int
	endMatched  int
	inHeaders   bool
}

// Write consumes serialization incrementally and terminates as soon as the complete header block is known.
func (w *requestHeaderCounter) Write(data []byte) (int, error) {
	for index, value := range data {
		if !w.inHeaders {
			if (w.lineMatched == 0 && value == '\r') || (w.lineMatched == 1 && value == '\n') {
				w.lineMatched++
				if w.lineMatched == 2 {
					w.inHeaders = true
				}
			} else {
				w.lineMatched = 0
			}
			continue
		}
		w.count++
		if w.count > w.limit {
			return index + 1, errRequestHeaderLimit
		}
		switch w.endMatched {
		case 0, 2:
			if value == '\r' {
				w.endMatched++
			} else {
				w.endMatched = 0
			}
		case 1, 3:
			if value == '\n' {
				w.endMatched++
			} else {
				w.endMatched = 0
			}
		}
		if w.endMatched == 4 {
			return index + 1, errRequestHeaderMeasured
		}
	}
	return len(data), nil
}

// headerLimitConn prevents net/http from receiving a response header block larger than the configured bound.
type headerLimitConn struct {
	net.Conn
	mu       sync.Mutex
	seen     int
	finished bool
	limit    int
	tail     []byte
}

// Read inspects only the initial header block and refuses to pass an oversized block to the HTTP parser.
func (c *headerLimitConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || n == 0 {
		return n, err
	}
	combined := append(append([]byte(nil), c.tail...), buffer[:n]...)
	if end := bytes.Index(combined, []byte("\r\n\r\n")); end >= 0 {
		c.seen = c.seen - len(c.tail) + end + 4
		c.finished = true
	} else {
		c.seen += n
		start := len(combined) - 3
		if start < 0 {
			start = 0
		}
		c.tail = append(c.tail[:0], combined[start:]...)
	}
	if c.seen > c.limit {
		return 0, errResponseHeaderLimit
	}
	return n, err
}

// Execute performs one target-scoped exchange and returns bounded data without retaining or persisting it.
func (t *Tool) Execute(parent context.Context, call harness.ToolCall) (harness.ToolResult, error) {
	if err := parent.Err(); err != nil {
		return harness.ToolResult{}, err
	}
	input, err := t.validateInput(call.Input)
	if err != nil {
		return harness.ToolResult{}, err
	}
	operation, cancel := t.withTimeout(parent, t.timeout)
	defer cancel()
	started := t.clock.Now()
	baseResult := emptyResult()
	addresses, err := t.resolve(operation)
	if err != nil {
		baseResult.DurationMS = durationMillis(started, t.clock.Now())
		if parent.Err() != nil || operation.Err() != nil {
			return classifiedContextError(parent, operation, err, baseResult)
		}
		return operationFailure("dns", transientNetworkError(err), err, baseResult)
	}
	transport := t.transport(addresses)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	client.CheckRedirect = t.redirectPolicy(addresses)
	request, err := t.request(operation, input)
	if err != nil {
		baseResult.DurationMS = durationMillis(started, t.clock.Now())
		return operationFailure("encode", false, err, baseResult)
	}
	response, exchangeErr := client.Do(request)
	baseResult.DurationMS = durationMillis(started, t.clock.Now())
	if exchangeErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if parent.Err() != nil || operation.Err() != nil {
			return classifiedContextError(parent, operation, exchangeErr, baseResult)
		}
		category, transient := classifyExchangeError(exchangeErr)
		return operationFailure(category, transient, exchangeErr, baseResult)
	}
	defer response.Body.Close()
	if responseHeaderBytes(response.Header) > t.maxResponseHead {
		return operationFailure("response_limit", false, nil, baseResult)
	}
	result, err := t.decodeResponse(operation, response, baseResult.DurationMS)
	if err != nil {
		if parent.Err() != nil || operation.Err() != nil {
			return classifiedContextError(parent, operation, err, baseResult)
		}
		return operationFailure("read", transientNetworkError(err), err, baseResult)
	}
	return harness.ToolResult{Output: resultMap(result), ErrorClass: harness.ErrorNone}, nil
}

// emptyResult supplies the complete stable output shape for operational failures with no response.
func emptyResult() Result {
	return Result{Status: nil, Headers: map[string][]string{}, Body: nil, BodyFormat: "empty", Error: nil}
}

// resolve creates the single address snapshot used by every hop in this operation.
func (t *Tool) resolve(ctx context.Context) ([]net.IP, error) {
	if parsed := net.ParseIP(t.scope.hostname); parsed != nil {
		return []net.IP{append(net.IP(nil), parsed...)}, nil
	}
	resolved, err := t.resolver.LookupIPAddr(ctx, t.scope.hostname)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	addresses := make([]net.IP, 0, len(resolved))
	for _, candidate := range resolved {
		if candidate.IP == nil {
			continue
		}
		key := candidate.IP.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		addresses = append(addresses, append(net.IP(nil), candidate.IP...))
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("resolver returned no addresses")
	}
	return addresses, nil
}

// transport disables proxy and binds all sockets to the authorized authority and pinned address snapshot.
func (t *Tool) transport(addresses []net.IP) *http.Transport {
	dialPinned := func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(host, "."), t.scope.hostname) || port != t.scope.port {
			return nil, errDestinationDenied
		}
		if len(addresses) == 0 {
			return nil, errDestinationDenied
		}
		// A call to Execute owns exactly one connection attempt. Selecting the
		// first resolver-ordered address preserves the pinned snapshot without
		// turning alternative addresses into implicit retries.
		return t.dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			connection, err := dialPinned(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return &headerLimitConn{Conn: connection, limit: t.maxResponseHead}, nil
		},
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			connection, err := dialPinned(ctx, network, address)
			if err != nil {
				return nil, err
			}
			tlsConnection := tls.Client(connection, &tls.Config{ServerName: t.scope.hostname, MinVersion: tls.VersionTLS12})
			if err := tlsConnection.HandshakeContext(ctx); err != nil {
				_ = connection.Close()
				return nil, err
			}
			return &headerLimitConn{Conn: tlsConnection, limit: t.maxResponseHead}, nil
		},
		TLSClientConfig:        &tls.Config{ServerName: t.scope.hostname, MinVersion: tls.VersionTLS12},
		MaxResponseHeaderBytes: 0,
		DisableCompression:     true,
	}
	return transport
}

// request builds a standard-library request only from the already validated and encoded representation.
func (t *Tool) request(ctx context.Context, input validatedInput) (*http.Request, error) {
	var body io.Reader
	if input.bodyPresent {
		body = bytes.NewReader(input.body)
	}
	request, err := http.NewRequestWithContext(ctx, input.method, t.buildURL(input).String(), body)
	if err != nil {
		return nil, err
	}
	for name, values := range input.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if input.bodyPresent {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

// validateSerializedRequestHeaders measures every header net/http will write, including Host, User-Agent, and Content-Length.
func (t *Tool) validateSerializedRequestHeaders(request *http.Request) error {
	clone := request.Clone(request.Context())
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return err
		}
		clone.Body = body
		defer body.Close()
	}
	counter := &requestHeaderCounter{limit: t.maxRequestHeader}
	err := clone.Write(counter)
	if errors.Is(err, errRequestHeaderMeasured) {
		return nil
	}
	return err
}

// redirectPolicy rechecks every generated hop against the original immutable scope before another dial.
func (t *Tool) redirectPolicy(addresses []net.IP) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		previous := via[len(via)-1]
		if _, follow := t.redirectMethods[previous.Method]; !follow {
			return http.ErrUseLastResponse
		}
		if len(via) > t.maxRedirects {
			return errRedirectLimit
		}
		if _, allowed := t.methods[request.Method]; !allowed {
			return errRedirectDenied
		}
		if err := t.validateDestination(request.URL, addresses); err != nil {
			return errRedirectDenied
		}
		// net/http synthesizes Referer during redirect construction; it is not
		// model input and is removed so it cannot widen the configured allowlist.
		request.Header.Del("Referer")
		for name, values := range request.Header {
			lower := strings.ToLower(name)
			if _, secret := sensitiveHeaders[lower]; secret {
				return errRedirectDenied
			}
			canonical, allowed := t.requestHeaders[lower]
			if !allowed || canonical == "" {
				return errRedirectDenied
			}
			for _, value := range values {
				if !validHeaderValue(value) {
					return errRedirectDenied
				}
			}
		}
		if err := t.validateSerializedRequestHeaders(request); err != nil {
			if errors.Is(err, errRequestHeaderLimit) {
				return errRequestHeaderLimit
			}
			return errRedirectDenied
		}
		return nil
	}
}

// validateDestination proves a redirect remains inside the original scheme, authority, and path segment boundary.
func (t *Tool) validateDestination(destination *url.URL, addresses []net.IP) error {
	if destination == nil || destination.User != nil || destination.Fragment != "" || destination.Scheme != t.scope.scheme {
		return errDestinationDenied
	}
	host := strings.ToLower(strings.TrimSuffix(destination.Hostname(), "."))
	port := destination.Port()
	if port == "" {
		if destination.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if host != t.scope.hostname || port != t.scope.port {
		return errDestinationDenied
	}
	segments, err := parsePath(destination.EscapedPath(), true)
	if err != nil || !segmentPrefix(segments, t.scope.baseSegments) {
		return errDestinationDenied
	}
	if ip := net.ParseIP(host); ip != nil && !containsIP(addresses, ip) {
		return errDestinationDenied
	}
	return nil
}

// segmentPrefix prevents textual prefix confusion such as treating /apix as a child of /api.
func segmentPrefix(path, base []string) bool {
	if len(path) < len(base) {
		return false
	}
	for i := range base {
		if path[i] != base[i] {
			return false
		}
	}
	return true
}

// containsIP checks literal redirects against the operation-local snapshot without another resolution.
func containsIP(addresses []net.IP, candidate net.IP) bool {
	for _, address := range addresses {
		if address.Equal(candidate) {
			return true
		}
	}
	return false
}

// decodeResponse filters metadata and captures at most one byte beyond the configured body prefix.
func (t *Tool) decodeResponse(ctx context.Context, response *http.Response, duration int64) (Result, error) {
	result := emptyResult()
	result.Status = response.StatusCode
	result.DurationMS = duration
	result.Headers = t.allowedResponseHeaders(response.Header)
	limited := io.LimitReader(response.Body, int64(t.maxResponseBody)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(data) > t.maxResponseBody {
		data = data[:t.maxResponseBody]
		result.BodyTruncated = true
	}
	result.BodyBytes = len(data)
	result.Body, result.BodyFormat = decodeBody(response.Header.Get("Content-Type"), data, result.BodyTruncated)
	return result, nil
}

// allowedResponseHeaders returns only configured non-sensitive names in deterministic canonical order.
func (t *Tool) allowedResponseHeaders(headers http.Header) map[string][]string {
	names := make([]string, 0, len(t.responseHeaders))
	for lower := range t.responseHeaders {
		names = append(names, lower)
	}
	sort.Strings(names)
	out := map[string][]string{}
	for _, lower := range names {
		if _, secret := sensitiveHeaders[lower]; secret {
			continue
		}
		canonical := t.responseHeaders[lower]
		if values, exists := headers[canonical]; exists {
			out[canonical] = append([]string(nil), values...)
		}
	}
	return out
}

// decodeBody selects JSON only for complete valid payloads and otherwise uses safe text or base64 representations.
func decodeBody(contentType string, data []byte, truncated bool) (any, string) {
	if len(data) == 0 {
		return nil, "empty"
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if !truncated && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			var extra any
			if decoder.Decode(&extra) == io.EOF {
				return value, "json"
			}
		}
	}
	if utf8.Valid(data) {
		return string(data), "text"
	}
	return base64.StdEncoding.EncodeToString(data), "base64"
}

// durationMillis clamps clock anomalies so evidence never receives a negative duration.
func durationMillis(start, end time.Time) int64 {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

// classifyExchangeError maps redirect and transport failures without including their diagnostic strings in output.
func classifyExchangeError(err error) (string, bool) {
	switch {
	case errors.Is(err, errRequestHeaderLimit):
		return "request_limit", false
	case errors.Is(err, errResponseHeaderLimit):
		return "response_limit", false
	case errors.Is(err, errRedirectLimit):
		return "redirect_limit", false
	case errors.Is(err, errRedirectDenied):
		return "redirect_denied", false
	case errors.Is(err, errDestinationDenied):
		return "destination_denied", false
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateError x509.CertificateInvalidError
	var recordError tls.RecordHeaderError
	var alertError tls.AlertError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.As(err, &certificateError) || errors.As(err, &recordError) || errors.As(err, &alertError) {
		return "tls", false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout", true
		}
		return "connect", netErr.Temporary()
	}
	return "connect", true
}

// transientNetworkError reads standard network retry hints while leaving the retry decision with the executor.
func transientNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}
