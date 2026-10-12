//go:build !darwin && !linux && !windows

package localvalidation

import "errors"

// commitNoReplace fails closed where the adapter has no verified atomic no-replace primitive.
func commitNoReplace(_, _ string) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
