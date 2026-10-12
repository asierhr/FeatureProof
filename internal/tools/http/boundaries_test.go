package httptool

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// readerConn gives header-bound tests a synchronous in-memory net.Conn with no socket effects.
type readerConn struct{ io.Reader }

// Write is unused by read-boundary tests and rejects accidental protocol writes.
func (readerConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// Close has no resource work because the reader is entirely in memory.
func (readerConn) Close() error { return nil }

// LocalAddr supplies a harmless address required by the net.Conn contract.
func (readerConn) LocalAddr() net.Addr { return testAddr("local") }

// RemoteAddr supplies a harmless address required by the net.Conn contract.
func (readerConn) RemoteAddr() net.Addr { return testAddr("remote") }

// SetDeadline has no effect because in-memory reads cannot block.
func (readerConn) SetDeadline(time.Time) error { return nil }

// SetReadDeadline has no effect because in-memory reads cannot block.
func (readerConn) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline has no effect because this test connection never writes.
func (readerConn) SetWriteDeadline(time.Time) error { return nil }

// testAddr identifies the inert endpoints used by readerConn.
type testAddr string

// Network labels the address as an in-memory test boundary.
func (testAddr) Network() string { return "test" }

// String returns the stable endpoint label used only in diagnostics.
func (a testAddr) String() string { return string(a) }

// TestHeaderLimitConnHandlesSplitBoundaryAndStopsCountingBody verifies exact header accounting across reads.
func TestHeaderLimitConnHandlesSplitBoundaryAndStopsCountingBody(t *testing.T) {
	connection := &headerLimitConn{Conn: readerConn{Reader: io.MultiReader(bytes.NewBufferString("HTTP/1.1 200 OK\r\nX: y\r\n\r"), bytes.NewBufferString("\nbody-that-is-larger-than-the-header-limit"))}, limit: 28}
	buffer := make([]byte, 27)
	if _, err := connection.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if !connection.finished || connection.seen != 25 {
		t.Fatalf("header boundary not detected: %#v", connection)
	}
}

// TestHeaderLimitConnRejectsBeforeParser verifies oversized metadata never crosses the adapter boundary.
func TestHeaderLimitConnRejectsBeforeParser(t *testing.T) {
	connection := &headerLimitConn{Conn: readerConn{Reader: bytes.NewBufferString("HTTP/1.1 200 OK\r\nX-Large: value\r\n\r\n")}, limit: 8}
	buffer := make([]byte, 128)
	if n, err := connection.Read(buffer); n != 0 || !errors.Is(err, errResponseHeaderLimit) {
		t.Fatalf("oversized header passed: n=%d err=%v", n, err)
	}
}

// TestResolveProducesOneDeduplicatedOperationSnapshot covers empty and duplicate resolver observations.
func TestResolveProducesOneDeduplicatedOperationSnapshot(t *testing.T) {
	config := baseConfig("http://app.test")
	resolver := &fixedResolver{addrs: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("127.0.0.1")}, {}}}
	config.Resolver = resolver
	tool := mustTool(t, config)
	addresses, err := tool.resolve(context.Background())
	if err != nil || len(addresses) != 1 {
		t.Fatalf("snapshot not deduplicated: %v %v", addresses, err)
	}
	config.Resolver = &fixedResolver{}
	tool = mustTool(t, config)
	if _, err := tool.resolve(context.Background()); err == nil {
		t.Fatal("empty DNS result accepted")
	}
}

// TestRedirectPolicyValidatesHeadersAndDestination exercises all per-hop policy checks without sockets.
func TestRedirectPolicyValidatesHeadersAndDestination(t *testing.T) {
	tool := mustTool(t, baseConfig("http://app.test/api"))
	addresses := []net.IP{net.ParseIP("127.0.0.1")}
	hook := tool.redirectPolicy(addresses)
	previous := &http.Request{Method: "GET", URL: &url.URL{Scheme: "http", Host: "app.test", Path: "/api/start"}}
	valid := &http.Request{Method: "GET", URL: &url.URL{Scheme: "http", Host: "app.test", Path: "/api/end"}, Header: http.Header{"X-Request": {"ok"}, "Referer": {"secret-query"}}}
	if err := hook(valid, []*http.Request{previous}); err != nil || valid.Header.Get("Referer") != "" {
		t.Fatalf("valid redirect rejected or referer retained: %v %#v", err, valid.Header)
	}
	for name, header := range map[string]http.Header{
		"secret":  {"Authorization": {"secret"}},
		"unknown": {"X-Unknown": {"value"}},
		"newline": {"X-Request": {"bad\r\nvalue"}},
	} {
		t.Run(name, func(t *testing.T) {
			request := &http.Request{Method: "GET", URL: &url.URL{Scheme: "http", Host: "app.test", Path: "/api/end"}, Header: header}
			if !errors.Is(hook(request, []*http.Request{previous}), errRedirectDenied) {
				t.Fatal("invalid redirect header accepted")
			}
		})
	}
	tool.redirectMethods = map[string]struct{}{}
	if !errors.Is(tool.redirectPolicy(addresses)(valid, []*http.Request{previous}), http.ErrUseLastResponse) {
		t.Fatal("disabled redirect method was followed")
	}
}

// TestDestinationChecksEveryScopeDimension covers userinfo, fragment, scheme, authority, path, and pinned literal IP.
func TestDestinationChecksEveryScopeDimension(t *testing.T) {
	tool := mustTool(t, baseConfig("http://127.0.0.1/api"))
	pinned := []net.IP{net.ParseIP("127.0.0.2")}
	cases := []*url.URL{
		nil,
		{Scheme: "https", Host: "127.0.0.1", Path: "/api/x"},
		{Scheme: "http", Host: "user@127.0.0.1", Path: "/api/x", User: url.User("user")},
		{Scheme: "http", Host: "127.0.0.1", Path: "/api/x", Fragment: "fragment"},
		{Scheme: "http", Host: "127.0.0.2", Path: "/api/x"},
		{Scheme: "http", Host: "127.0.0.1", Path: "/outside"},
		{Scheme: "http", Host: "127.0.0.1", Path: "/api/x"},
	}
	for _, destination := range cases {
		if err := tool.validateDestination(destination, pinned); !errors.Is(err, errDestinationDenied) {
			t.Fatalf("destination %#v accepted: %v", destination, err)
		}
	}
	if !containsIP([]net.IP{net.ParseIP("127.0.0.1")}, net.ParseIP("127.0.0.1")) || containsIP(pinned, net.ParseIP("127.0.0.1")) {
		t.Fatal("pinned IP membership is incorrect")
	}
}

// TestErrorClassificationAndClockAnomalies keeps stable categories independent of diagnostic text and clock skew.
func TestErrorClassificationAndClockAnomalies(t *testing.T) {
	if category, transient := classifyExchangeError(x509.UnknownAuthorityError{}); category != "tls" || transient {
		t.Fatalf("TLS error misclassified: %s %v", category, transient)
	}
	if category, transient := classifyExchangeError(errDestinationDenied); category != "destination_denied" || transient {
		t.Fatalf("destination error misclassified: %s %v", category, transient)
	}
	if got := durationMillis(time.Unix(2, 0), time.Unix(1, 0)); got != 0 {
		t.Fatalf("negative duration escaped: %d", got)
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := classifiedContextError(parent, context.Background(), errors.New("x"), emptyResult()); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation lost: %v", err)
	}
}

// TestIPv6DefaultAuthorityKeepsURLSyntaxValid covers bracket normalization without granting another port.
func TestIPv6DefaultAuthorityKeepsURLSyntaxValid(t *testing.T) {
	scope, err := parseTarget("http://[::1]/api")
	if err != nil || scope.authority() != "[::1]" {
		t.Fatalf("IPv6 authority malformed: %#v %v", scope, err)
	}
}

// TestSerializedRequestHeadersIncludeControlledDefaults proves the preflight count matches every header the configured transport can emit.
func TestSerializedRequestHeadersIncludeControlledDefaults(t *testing.T) {
	tool := mustTool(t, baseConfig("http://app.test:8080"))
	input := map[string]any{
		"method": "POST", "path": "items", "headers": map[string][]string{"X-Request": {"yes"}}, "body": nil,
	}
	validated, err := tool.validateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := tool.request(context.Background(), validated)
	if err != nil {
		t.Fatal(err)
	}
	counter := &requestHeaderCounter{limit: 4096}
	if err := request.Write(counter); !errors.Is(err, errRequestHeaderMeasured) {
		t.Fatalf("request headers were not measured: %v", err)
	}
	expected := headerBytes(map[string][]string{
		"Host":           {"app.test:8080"},
		"User-Agent":     {"Go-http-client/1.1"},
		"Content-Length": {"4"},
		"Content-Type":   {"application/json"},
		"X-Request":      {"yes"},
	}) + 2
	if counter.count != expected {
		t.Fatalf("serialized headers = %d, want %d", counter.count, expected)
	}
	transport := tool.transport([]net.IP{net.ParseIP("127.0.0.1")})
	defer transport.CloseIdleConnections()
	if !transport.DisableCompression || transport.DisableKeepAlives {
		t.Fatalf("transport may inject unmeasured defaults: %#v", transport)
	}
	tool.maxRequestHeader = expected - 1
	if err := tool.Validate(input); err == nil {
		t.Fatal("serialized automatic headers escaped the configured limit")
	}
}
