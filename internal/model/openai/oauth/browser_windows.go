//go:build windows

package oauth

import (
	"context"
	"os/exec"

	"github.com/asierhr/featureProof/internal/model"
)

// SystemBrowser opens the user-visible OAuth URL with the operating system browser.
type SystemBrowser struct{}

// Open starts the Windows URL handler without logging the authorization URL.
func (SystemBrowser) Open(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	if err := command.Start(); err != nil {
		return oauthBoundaryError(ctx, "open system browser", err, model.CategoryAuthentication)
	}
	if err := command.Process.Release(); err != nil {
		return oauthBoundaryError(ctx, "release system browser", err, model.CategoryAuthentication)
	}
	return nil
}
