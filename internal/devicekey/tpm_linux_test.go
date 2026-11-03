//go:build linux

package devicekey

import (
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpm2/transport/tcp"
)

// TestSoftwareTPM runs the TPM key against a software TPM: swtpm, started with
//
//	swtpm socket --tpm2 --server type=tcp,port=2321 --ctrl type=tcp,port=2322 --tpmstate dir=$(mktemp -d) --flags not-need-init,startup-clear
//
// and EDKA_TEST_SWTPM=localhost:2321. CI starts one.
func TestSoftwareTPM(t *testing.T) {
	address := os.Getenv("EDKA_TEST_SWTPM")
	if address == "" {
		t.Skip("set EDKA_TEST_SWTPM to the command port of a software TPM")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	commandPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	platform := net.JoinHostPort(host, strconv.Itoa(commandPort+1))
	defer func(previous func() (transport.TPMCloser, error)) { openTPM = previous }(openTPM)
	openTPM = func() (transport.TPMCloser, error) {
		return tcp.Open(tcp.Config{CommandAddress: address, PlatformAddress: platform})
	}

	key, err := Create()
	if err != nil {
		t.Fatal(err)
	}
	if key.Protection() != TPM {
		t.Fatalf("protection = %s, hardware error = %v", key.Protection(), key.HardwareError)
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

	// The wrapped private part opens in no other TPM: clear this one's owner
	// seed, as a different chip would have another.
	tpm, err := openTPM()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (tpm2.Clear{AuthHandle: tpm2.TPMRHPlatform}).Execute(tpm); err != nil {
		t.Fatal(err)
	}
	_ = tpm.Close()
	if _, err := reopened.Proof("GET", "https://api.edka.io/api/cli/whoami", ""); err == nil {
		t.Fatal("the key signed after the TPM's owner seed changed")
	}
}
