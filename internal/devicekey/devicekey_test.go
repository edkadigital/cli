package devicekey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

// verifyProof checks a proof the way RFC 9449 §4.3 has a server check it, and
// answers its claims.
func verifyProof(t *testing.T, proof string) map[string]any {
	t.Helper()
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		t.Fatalf("proof has %d parts", len(parts))
	}
	decode := func(part string) []byte {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var header struct {
		Typ string            `json:"typ"`
		Alg string            `json:"alg"`
		JWK map[string]string `json:"jwk"`
	}
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Typ != "dpop+jwt" || header.Alg != "ES256" || header.JWK["kty"] != "EC" || header.JWK["crv"] != "P-256" || len(header.JWK) != 4 {
		t.Fatalf("header = %+v", header)
	}
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, decode(header.JWK["x"])...), decode(header.JWK["y"])...))
	if err != nil {
		t.Fatal(err)
	}
	signature := decode(parts[2])
	if len(signature) != 64 {
		t.Fatalf("signature has %d bytes", len(signature))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("proof signature does not verify with the key in its header")
	}
	var claims map[string]any
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func testKey(t *testing.T, key *Key) {
	t.Helper()
	proof, err := key.Proof("POST", "https://api.edka.io/api/clusters/a%2Fb?x=1#frag", "token")
	if err != nil {
		t.Fatal(err)
	}
	claims := verifyProof(t, proof)
	sum := sha256.Sum256([]byte("token"))
	if claims["htm"] != "POST" || claims["htu"] != "https://api.edka.io/api/clusters/a%2Fb" || claims["ath"] != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("claims = %v", claims)
	}
	if iat, _ := claims["iat"].(float64); time.Since(time.Unix(int64(iat), 0)).Abs() > 5*time.Second {
		t.Fatalf("iat = %v", claims["iat"])
	}
	other, err := key.Proof("GET", "https://api.edka.io/api/auth/oauth2/token", "")
	if err != nil {
		t.Fatal(err)
	}
	otherClaims := verifyProof(t, other)
	if _, ok := otherClaims["ath"]; ok || otherClaims["jti"] == claims["jti"] {
		t.Fatalf("second proof claims = %v", otherClaims)
	}
}

func TestSoftwareKeySignsProofsAndReopens(t *testing.T) {
	key, err := createSoftware()
	if err != nil {
		t.Fatal(err)
	}
	testKey(t, key)
	reopened, err := Open(key.Saved())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.JWK()["x"] != key.JWK()["x"] {
		t.Fatal("reopened key has another public key")
	}
	testKey(t, reopened)
}

// TestCreateUsesHardware runs on the device's own hardware. A device without
// usable hardware, such as a CI virtual machine, gets a software key.
func TestCreateUsesHardware(t *testing.T) {
	key, err := Create()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		key.Close()
		if err := Delete(key.Saved()); err != nil {
			t.Error(err)
		}
	}()
	testKey(t, key)
	t.Logf("protection: %s (hardware error: %v)", key.Protection(), key.HardwareError)
	if key.Protection() == Software {
		return
	}
	reopened, err := Open(key.Saved())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.JWK()["x"] != key.JWK()["x"] || reopened.JWK()["y"] != key.JWK()["y"] {
		t.Fatal("reopened key has another public key")
	}
	testKey(t, reopened)
	tampered := key.Saved()
	tampered.Handle = append([]byte(nil), tampered.Handle...)
	tampered.Handle[len(tampered.Handle)/2] ^= 1
	if broken, err := Open(tampered); err == nil {
		defer broken.Close()
		if _, err := broken.Proof("GET", "https://api.edka.io/api/cli/whoami", ""); err == nil && broken.JWK()["x"] == key.JWK()["x"] {
			t.Fatal("a tampered handle opened the same key")
		}
	}
}

func TestOpenRefusesUnknownProtection(t *testing.T) {
	if _, err := Open(Saved{Protection: "paper"}); err == nil {
		t.Fatal("opened a key of unknown protection")
	}
}

func TestSyncClockFollowsTheServerDate(t *testing.T) {
	key, err := createSoftware()
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	if key.SyncClock(header) {
		t.Fatal("a matching clock changed the offset")
	}
	header.Set("Date", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))
	if !key.SyncClock(header) {
		t.Fatal("a clock a minute ahead kept its offset")
	}
	proof, err := key.Proof("GET", "https://api.edka.io/api/cli/whoami", "")
	if err != nil {
		t.Fatal(err)
	}
	iat, _ := verifyProof(t, proof)["iat"].(float64)
	if behind := time.Since(time.Unix(int64(iat), 0)); behind < 55*time.Second || behind > 65*time.Second {
		t.Fatalf("proof is dated %s behind the local clock", behind)
	}
	if key.SyncClock(http.Header{}) {
		t.Fatal("a response without a date changed the offset")
	}
}

// brokenSigner is hardware whose signatures don't verify.
type brokenSigner struct{ closed *bool }

func (brokenSigner) sign([]byte) ([]byte, error) { return make([]byte, 64), nil }
func (b brokenSigner) close()                    { *b.closed = true }

func TestCreateFallsBackToSoftware(t *testing.T) {
	restore := func(c func() (Saved, *ecdsa.PublicKey, signer, error), d func(Saved) error) func() {
		return func() { createHardware, deleteHardware = c, d }
	}
	defer restore(createHardware, deleteHardware)()

	createHardware = func() (Saved, *ecdsa.PublicKey, signer, error) { return Saved{}, nil, nil, errNoHardware }
	key, err := Create()
	if err != nil || key.Protection() != Software || key.HardwareError != nil {
		t.Fatalf("no hardware: key = %+v, err = %v", key, err)
	}

	denied := errors.New("open /dev/tpmrm0: permission denied")
	createHardware = func() (Saved, *ecdsa.PublicKey, signer, error) { return Saved{}, nil, nil, denied }
	key, err = Create()
	if err != nil || key.Protection() != Software || !errors.Is(key.HardwareError, denied) {
		t.Fatalf("unusable hardware: key = %+v, err = %v", key, err)
	}

	software, _ := createSoftware()
	closed, deleted := false, false
	createHardware = func() (Saved, *ecdsa.PublicKey, signer, error) {
		return Saved{Protection: TPM, Handle: []byte("name")}, software.public, brokenSigner{&closed}, nil
	}
	deleteHardware = func(saved Saved) error { deleted = string(saved.Handle) == "name"; return nil }
	key, err = Create()
	if err != nil || key.Protection() != Software || key.HardwareError == nil {
		t.Fatalf("failing hardware: key = %+v, err = %v", key, err)
	}
	if !closed || !deleted {
		t.Fatalf("the failing hardware key was not released (closed %v, deleted %v)", closed, deleted)
	}
}
