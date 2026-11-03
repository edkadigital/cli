//go:build (darwin && !cgo) || !(darwin || windows || linux)

package devicekey

import (
	"crypto/ecdsa"
	"fmt"
)

// A macOS build without cgo can't reach the Secure Enclave. Releases build
// with cgo; release.yml checks the binaries for it.
func platformCreate() (Saved, *ecdsa.PublicKey, signer, error) {
	return Saved{}, nil, nil, errNoHardware
}

func platformOpen(saved Saved) (*ecdsa.PublicKey, signer, error) {
	return nil, nil, fmt.Errorf("this build of the CLI can't open %s keys", saved.Protection)
}

func platformDelete(Saved) error { return nil }
