//go:build linux

package devicekey

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/linuxtpm"
)

// The kernel's resource manager lets several programs share the TPM. Most
// distributions give it to the tss group, so a user outside that group gets a
// software key.
const tpmDevice = "/dev/tpmrm0"

var openTPM = func() (transport.TPMCloser, error) { return linuxtpm.Open(tpmDevice) }

// The key is a child of the storage root key, which the TPM derives from its
// owner seed the same way every time, so nothing stays loaded between uses.
// The TPM hands the child's private part out encrypted under the root key:
// only this TPM can load it.
var signingTemplate = tpm2.TPMTPublic{
	Type:    tpm2.TPMAlgECC,
	NameAlg: tpm2.TPMAlgSHA256,
	ObjectAttributes: tpm2.TPMAObject{
		FixedTPM:            true,
		FixedParent:         true,
		SensitiveDataOrigin: true,
		UserWithAuth:        true,
		SignEncrypt:         true,
	},
	Parameters: tpm2.NewTPMUPublicParms(tpm2.TPMAlgECC, &tpm2.TPMSECCParms{
		Scheme: tpm2.TPMTECCScheme{
			Scheme:  tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUAsymScheme(tpm2.TPMAlgECDSA, &tpm2.TPMSSigSchemeECDSA{HashAlg: tpm2.TPMAlgSHA256}),
		},
		CurveID: tpm2.TPMECCNistP256,
	}),
}

func connect() (transport.TPMCloser, error) {
	tpm, err := openTPM()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.Join(errNoHardware, err)
	}
	if errors.Is(err, fs.ErrPermission) {
		return nil, fmt.Errorf("%w; to keep the key in the TPM, add your user to the group that owns %s", err, tpmDevice)
	}
	if err != nil {
		return nil, err
	}
	return retrying{tpm}, nil
}

// retrying sends a command again while the TPM answers that it can't run it
// yet (TPM_RC_RETRY, TPM_RC_YIELDED, TPM_RC_TESTING), as the TSS libraries do.
type retrying struct{ transport.TPMCloser }

func (r retrying) Send(command []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		response, err := r.TPMCloser.Send(command)
		if err != nil || len(response) < 10 || attempt == 20 {
			return response, err
		}
		switch binary.BigEndian.Uint32(response[6:10]) {
		case 0x922, 0x908, 0x90A:
			time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
		default:
			return response, nil
		}
	}
}

// withRoot runs fn with the storage root key loaded.
func withRoot(tpm transport.TPM, fn func(root tpm2.AuthHandle) error) error {
	created, err := tpm2.CreatePrimary{PrimaryHandle: tpm2.TPMRHOwner, InPublic: tpm2.New2B(tpm2.ECCSRKTemplate)}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("TPM storage root key: %w", err)
	}
	defer flush(tpm, created.ObjectHandle)
	return fn(tpm2.AuthHandle{Handle: created.ObjectHandle, Name: created.Name, Auth: tpm2.PasswordAuth(nil)})
}

func flush(tpm transport.TPM, handle tpm2.TPMHandle) {
	_, _ = tpm2.FlushContext{FlushHandle: handle}.Execute(tpm)
}

func platformCreate() (Saved, *ecdsa.PublicKey, signer, error) {
	tpm, err := connect()
	if err != nil {
		return Saved{}, nil, nil, err
	}
	defer func() { _ = tpm.Close() }()
	var saved Saved
	var public *ecdsa.PublicKey
	err = withRoot(tpm, func(root tpm2.AuthHandle) error {
		created, err := tpm2.Create{ParentHandle: root, InPublic: tpm2.New2B(signingTemplate)}.Execute(tpm)
		if err != nil {
			return fmt.Errorf("TPM create key: %w", err)
		}
		if public, err = tpmPublic(created.OutPublic); err != nil {
			return err
		}
		saved = Saved{Protection: TPM, Handle: encodeHandle(created.OutPublic, created.OutPrivate)}
		return nil
	})
	if err != nil {
		return Saved{}, nil, nil, err
	}
	return saved, public, tpmKey{saved.Handle}, nil
}

func platformOpen(saved Saved) (*ecdsa.PublicKey, signer, error) {
	if saved.Protection != TPM {
		return nil, nil, fmt.Errorf("Linux keeps no %s keys", saved.Protection)
	}
	public, _, err := decodeHandle(saved.Handle)
	if err != nil {
		return nil, nil, err
	}
	key, err := tpmPublic(public)
	if err != nil {
		return nil, nil, err
	}
	return key, tpmKey{saved.Handle}, nil
}

// The TPM keeps nothing of the key: it lives in its saved, wrapped handle.
func platformDelete(Saved) error { return nil }

// tpmKey loads the wrapped key for each signature and flushes it after, so the
// CLI holds no TPM handle between requests.
type tpmKey struct{ handle []byte }

func (k tpmKey) sign(digest []byte) ([]byte, error) {
	public, private, err := decodeHandle(k.handle)
	if err != nil {
		return nil, err
	}
	tpm, err := connect()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tpm.Close() }()
	var signature []byte
	err = withRoot(tpm, func(root tpm2.AuthHandle) error {
		loaded, err := tpm2.Load{ParentHandle: root, InPrivate: private, InPublic: public}.Execute(tpm)
		if err != nil {
			return fmt.Errorf("TPM load key (it was made by another TPM): %w", err)
		}
		defer flush(tpm, loaded.ObjectHandle)
		signed, err := tpm2.Sign{
			KeyHandle: tpm2.AuthHandle{Handle: loaded.ObjectHandle, Name: loaded.Name, Auth: tpm2.PasswordAuth(nil)},
			Digest:    tpm2.TPM2BDigest{Buffer: digest},
			InScheme: tpm2.TPMTSigScheme{
				Scheme:  tpm2.TPMAlgECDSA,
				Details: tpm2.NewTPMUSigScheme(tpm2.TPMAlgECDSA, &tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256}),
			},
			Validation: tpm2.TPMTTKHashCheck{Tag: tpm2.TPMSTHashCheck, Hierarchy: tpm2.TPMRHNull},
		}.Execute(tpm)
		if err != nil {
			return fmt.Errorf("TPM sign: %w", err)
		}
		ecc, err := signed.Signature.Signature.ECDSA()
		if err != nil {
			return err
		}
		r, s := ecc.SignatureR.Buffer, ecc.SignatureS.Buffer
		if len(r) > 32 || len(s) > 32 {
			return errors.New("the TPM answered a signature too long for P-256")
		}
		signature = make([]byte, 64)
		copy(signature[32-len(r):32], r)
		copy(signature[64-len(s):], s)
		return nil
	})
	return signature, err
}

func (tpmKey) close() {}

func tpmPublic(public tpm2.TPM2BPublic) (*ecdsa.PublicKey, error) {
	contents, err := public.Contents()
	if err != nil {
		return nil, err
	}
	point, err := contents.Unique.ECC()
	if err != nil {
		return nil, err
	}
	if len(point.X.Buffer) > 32 || len(point.Y.Buffer) > 32 {
		return nil, errors.New("the TPM answered a public key that is not P-256")
	}
	uncompressed := make([]byte, 65)
	uncompressed[0] = 4
	copy(uncompressed[33-len(point.X.Buffer):33], point.X.Buffer)
	copy(uncompressed[65-len(point.Y.Buffer):], point.Y.Buffer)
	return publicFromPoint(uncompressed)
}

// encodeHandle saves the key's public part and its wrapped private part,
// each as the TPM marshals it, the first after its length.
func encodeHandle(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) []byte {
	pub, priv := tpm2.Marshal(public), tpm2.Marshal(private)
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(pub))), append(pub, priv...)...)
}

func decodeHandle(handle []byte) (tpm2.TPM2BPublic, tpm2.TPM2BPrivate, error) {
	invalid := errors.New("the saved TPM key is damaged")
	if len(handle) < 4 {
		return tpm2.TPM2BPublic{}, tpm2.TPM2BPrivate{}, invalid
	}
	size := binary.BigEndian.Uint32(handle)
	if uint64(size) > uint64(len(handle)-4) {
		return tpm2.TPM2BPublic{}, tpm2.TPM2BPrivate{}, invalid
	}
	public, err := tpm2.Unmarshal[tpm2.TPM2BPublic](handle[4 : 4+size])
	if err != nil {
		return tpm2.TPM2BPublic{}, tpm2.TPM2BPrivate{}, errors.Join(invalid, err)
	}
	private, err := tpm2.Unmarshal[tpm2.TPM2BPrivate](handle[4+size:])
	if err != nil {
		return tpm2.TPM2BPublic{}, tpm2.TPM2BPrivate{}, errors.Join(invalid, err)
	}
	return *public, *private, nil
}
