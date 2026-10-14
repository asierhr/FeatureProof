package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

// CryptoRandom supplies cryptographically fresh OAuth attempt values.
type CryptoRandom struct{}

// Value returns an unpadded URL-safe random value of the requested byte length.
func (CryptoRandom) Value(size int) (string, error) {
	if size < 16 {
		return "", fmt.Errorf("oauth random size is too small")
	}
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate oauth random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// LoopbackListener owns one already-bound exact 127.0.0.1 callback socket.
type LoopbackListener struct {
	listener net.Listener
	path     string
}

// callbackDelivery keeps the listener alive until its HTTP response headers are committed.
type callbackDelivery struct {
	callback Callback
	served   chan struct{}
}

// NewLoopbackListener binds the callback before a browser can begin authorization.
func NewLoopbackListener(redirectURI string) (*LoopbackListener, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path == "" {
		return nil, authError("configure loopback listener")
	}
	listener, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		return nil, &modelLoopbackError{}
	}
	return &LoopbackListener{listener: listener, path: parsed.Path}, nil
}

// Await serves one callback, returns bounded query values and shuts down on every exit.
func (l *LoopbackListener) Await(ctx context.Context, _ string) (Callback, error) {
	result := make(chan callbackDelivery, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != l.path {
			http.NotFound(writer, request)
			return
		}
		query := request.URL.Query()
		delivery := callbackDelivery{callback: Callback{State: boundedQuery(query.Get("state")), Code: boundedQuery(query.Get("code")), IssuedClientID: boundedQuery(query.Get("client_id"))}, served: make(chan struct{})}
		select {
		case result <- delivery:
			writer.WriteHeader(http.StatusNoContent)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			close(delivery.served)
		default:
			http.Error(writer, "callback already received", http.StatusConflict)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l.listener) }()
	defer func() { _ = server.Close(); <-done }()
	select {
	case <-ctx.Done():
		return Callback{}, ctxModelError("await oauth callback", ctx.Err())
	case delivery := <-result:
		<-delivery.served
		return delivery.callback, nil
	}
}

// boundedQuery rejects unbounded or non-printable callback values.
func boundedQuery(value string) string {
	if len(value) == 0 || len(value) > 4096 {
		return ""
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return ""
		}
	}
	return value
}

// modelLoopbackError avoids exposing local ports or OS details.
type modelLoopbackError struct{}

// Error reports a controlled callback-listener failure.
func (*modelLoopbackError) Error() string { return "oauth loopback listener failed" }
