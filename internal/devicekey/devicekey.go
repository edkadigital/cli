// Package devicekey keeps the private key a signed-in CLI proves it holds with
// every request (RFC 9449 DPoP). Edka binds the tokens of the login to that
// key, so a copy of them works only together with a proof the key signs.
//
// The key is made in the security hardware of the device when it has some:
// the Secure Enclave of a Mac, or the TPM of a Windows or Linux machine. There
// the key signs but can't be read, by anyone. What the CLI saves to reopen it
// is a handle only that chip can use, so saved credentials copied to another
// machine authenticate nothing. A device without usable hardware gets a key
// saved with the credentials, which still keeps a token that leaks on its own
// from being used.
package devicekey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Protection names where a key lives.
type Protection string

const (
	SecureEnclave Protection = "secure-enclave"
	TPM           Protection = "tpm"
	Software      Protection = "software"
)

// Describe says where the key lives, for a sentence such as "Your tokens are
// bound to …".
func (p Protection) Describe() string {
	switch p {
	case SecureEnclave:
		return "this Mac's Secure Enclave"
	case TPM:
		return "this computer's TPM"
	default:
		return "a key saved with your credentials"
	}
}

// Saved is what the credential store keeps to open the key again. For a key in
// hardware, Handle is data only that device's chip can use: a key wrapped by
// the Secure Enclave or the TPM, or the name the TPM provider keeps the key
// under. For a software key it is the key itself.
type Saved struct {
	Protection Protection `json:"protection"`
	Handle     []byte     `json:"handle"`
}

// signer signs a SHA-256 digest and answers the signature as r‖s, 32 bytes each.
type signer interface {
	sign(digest []byte) ([]byte, error)
	close()
}

// errNoHardware means the device has no security hardware the CLI can use.
var errNoHardware = errors.New("no security hardware for keys")

// The hardware of the platform this binary runs on; tests replace them.
var (
	createHardware = platformCreate
	openHardware   = platformOpen
	deleteHardware = platformDelete
)

type Key struct {
	saved  Saved
	public *ecdsa.PublicKey
	signer signer
	// HardwareError says why a software key stands in for one in hardware,
	// when the device has hardware the CLI could not use.
	HardwareError error

	mu sync.Mutex
	// offset corrects the time proofs carry when the server's clock differs.
	offset time.Duration
}

func (k *Key) Saved() Saved           { return k.saved }
func (k *Key) Protection() Protection { return k.saved.Protection }

// Close releases the key's handle in the hardware, when it holds one.
func (k *Key) Close() { k.signer.close() }

// Create makes a key in the device's security hardware or, without usable
// hardware, a software key. A hardware key signs a test proof first: one that
// fails is deleted, and a software key takes its place.
func Create() (*Key, error) {
	saved, public, s, err := createHardware()
	if err == nil {
		key := &Key{saved: saved, public: public, signer: s}
		if err = key.selfTest(); err == nil {
			return key, nil
		}
		s.close()
		if deleteErr := deleteHardware(saved); deleteErr != nil {
			err = errors.Join(err, deleteErr)
		}
	}
	key, softwareErr := createSoftware()
	if softwareErr != nil {
		return nil, softwareErr
	}
	if !errors.Is(err, errNoHardware) {
		key.HardwareError = err
	}
	return key, nil
}

// Open reopens a saved key.
func Open(saved Saved) (*Key, error) {
	var (
		public *ecdsa.PublicKey
		s      signer
		err    error
	)
	switch saved.Protection {
	case Software:
		public, s, err = openSoftware(saved.Handle)
	case SecureEnclave, TPM:
		public, s, err = openHardware(saved)
	default:
		return nil, fmt.Errorf("unknown device key protection %q; run `edka login`", saved.Protection)
	}
	if err != nil {
		return nil, fmt.Errorf("open the device key: %w; run `edka login`", err)
	}
	return &Key{saved: saved, public: public, signer: s}, nil
}

// Delete removes a key the hardware keeps by name. A wrapped or software key
// is gone once its saved handle is.
func Delete(saved Saved) error {
	if saved.Protection == Software {
		return nil
	}
	return deleteHardware(saved)
}

func (k *Key) selfTest() error {
	digest := sha256.Sum256([]byte("device key self-test"))
	signature, err := k.signer.sign(digest[:])
	if err != nil {
		return err
	}
	if len(signature) != 64 {
		return fmt.Errorf("the key answered a %d-byte signature", len(signature))
	}
	r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(k.public, digest[:], r, s) {
		return errors.New("the key's signature does not verify")
	}
	return nil
}

// JWK is the public key as RFC 7517 names it in a proof's header.
func (k *Key) JWK() map[string]string {
	// An uncompressed P-256 point: 0x04, then X and Y in 32 bytes each.
	point, err := k.public.Bytes()
	if err != nil || len(point) != 65 {
		return nil
	}
	return map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(point[1:33]),
		"y":   base64.RawURLEncoding.EncodeToString(point[33:]),
	}
}

// Proof signs the DPoP proof of one request: its method and URL, without query
// or fragment, and the access token it carries, if any (RFC 9449 §4.2).
func (k *Key) Proof(method, rawURL, accessToken string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	k.mu.Lock()
	now := time.Now().Add(k.offset)
	k.mu.Unlock()
	claims := map[string]any{
		"jti": base64.RawURLEncoding.EncodeToString(id),
		"htm": method,
		"htu": u.Scheme + "://" + u.Host + u.EscapedPath(),
		"iat": now.Unix(),
	}
	if accessToken != "" {
		sum := sha256.Sum256([]byte(accessToken))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(sum[:])
	}
	jwk := k.JWK()
	if jwk == nil {
		return "", errors.New("the device key has no P-256 public key")
	}
	header, err := json.Marshal(map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": jwk})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	signature, err := k.signer.sign(digest[:])
	if err != nil {
		return "", fmt.Errorf("sign with the device key: %w", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// SyncClock takes the server's time from the Date header of a response that
// refused a proof. Edka accepts a proof dated at most 5 seconds ahead of its
// clock, so a computer whose clock runs ahead fails every request. SyncClock
// reports whether the time proofs carry changed, so the request is worth
// sending once more.
func (k *Key) SyncClock(header http.Header) bool {
	date, err := http.ParseTime(header.Get("Date"))
	if err != nil {
		return false
	}
	// Date has whole seconds, so it trails the server's clock by up to one.
	offset := time.Until(date)
	k.mu.Lock()
	defer k.mu.Unlock()
	if (offset - k.offset).Abs() < 2*time.Second {
		return false
	}
	k.offset = offset
	return true
}

// software keeps a P-256 key in memory, saved as PKCS #8.
type software struct{ key *ecdsa.PrivateKey }

func (s software) sign(digest []byte) ([]byte, error) {
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	sig.FillBytes(out[32:])
	return out, nil
}
func (software) close() {}

func createSoftware() (*Key, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	return &Key{saved: Saved{Protection: Software, Handle: der}, public: &private.PublicKey, signer: software{private}}, nil
}

func openSoftware(der []byte) (*ecdsa.PublicKey, signer, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, nil, err
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || private.Curve != elliptic.P256() {
		return nil, nil, errors.New("saved key is not a P-256 key")
	}
	return &private.PublicKey, software{private}, nil
}
