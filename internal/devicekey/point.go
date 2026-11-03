//go:build (darwin && cgo) || windows || linux

package devicekey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
)

// publicFromPoint reads an uncompressed P-256 point (0x04 ‖ X ‖ Y) and checks
// that it is on the curve.
func publicFromPoint(point []byte) (*ecdsa.PublicKey, error) {
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
}
