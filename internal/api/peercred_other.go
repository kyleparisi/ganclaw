//go:build !linux && !darwin

package api

import "errors"

// sockPeerUID has no implementation here, so every API connection is
// refused rather than let through unchecked.
func sockPeerUID(int) (uint32, error) {
	return 0, errors.New("peer credentials not supported on this platform")
}
