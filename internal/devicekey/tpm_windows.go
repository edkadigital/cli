//go:build windows

package devicekey

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows reaches the TPM through the Microsoft Platform Crypto Provider of
// CNG. A key it makes there stays in the TPM's protection: the provider keeps
// it for the user under a name, and it can't be exported.
const platformProvider = "Microsoft Platform Crypto Provider"

var (
	ncrypt                    = windows.NewLazySystemDLL("ncrypt.dll")
	ncryptOpenStorageProvider = ncrypt.NewProc("NCryptOpenStorageProvider")
	ncryptCreatePersistedKey  = ncrypt.NewProc("NCryptCreatePersistedKey")
	ncryptFinalizeKey         = ncrypt.NewProc("NCryptFinalizeKey")
	ncryptOpenKey             = ncrypt.NewProc("NCryptOpenKey")
	ncryptExportKey           = ncrypt.NewProc("NCryptExportKey")
	ncryptSignHash            = ncrypt.NewProc("NCryptSignHash")
	ncryptDeleteKey           = ncrypt.NewProc("NCryptDeleteKey")
	ncryptFreeObject          = ncrypt.NewProc("NCryptFreeObject")
)

// Answers of a computer without a TPM the provider can use.
var noTPMStatus = map[uint32]bool{
	0x80090030: true, // NTE_DEVICE_NOT_READY
	0x80090035: true, // NTE_DEVICE_NOT_FOUND
	0x80284008: true, // TBS_E_SERVICE_NOT_RUNNING
	0x8028400F: true, // TBS_E_TPM_NOT_FOUND
	0x80284010: true, // TBS_E_SERVICE_DISABLED
	0x80090017: true, // NTE_PROV_TYPE_NOT_DEF
	0x80090019: true, // NTE_PROV_DLL_NOT_FOUND
}

type ncryptError struct {
	op     string
	status uint32
}

func (e ncryptError) Error() string {
	return fmt.Sprintf("TPM %s failed (0x%08X)", e.op, e.status)
}

func call(op string, proc *windows.LazyProc, args ...uintptr) error {
	if err := proc.Find(); err != nil {
		return errors.Join(errNoHardware, err)
	}
	status, _, _ := proc.Call(args...)
	if uint32(status) != 0 {
		err := ncryptError{op, uint32(status)}
		if noTPMStatus[uint32(status)] {
			return errors.Join(errNoHardware, err)
		}
		return err
	}
	return nil
}

func utf16(s string) uintptr {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return uintptr(unsafe.Pointer(p))
}

func openProvider() (uintptr, error) {
	var provider uintptr
	if err := call("open the platform provider", ncryptOpenStorageProvider, uintptr(unsafe.Pointer(&provider)), utf16(platformProvider), 0); err != nil {
		return 0, err
	}
	return provider, nil
}

func free(handle uintptr) {
	if handle != 0 {
		_, _, _ = ncryptFreeObject.Call(handle)
	}
}

type tpmKey struct {
	mu       sync.Mutex
	provider uintptr
	key      uintptr
}

func platformCreate() (Saved, *ecdsa.PublicKey, signer, error) {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return Saved{}, nil, nil, err
	}
	name := "edka-cli-" + hex.EncodeToString(suffix)
	provider, err := openProvider()
	if err != nil {
		return Saved{}, nil, nil, err
	}
	var key uintptr
	if err := call("create a key", ncryptCreatePersistedKey, provider, uintptr(unsafe.Pointer(&key)), utf16("ECDSA_P256"), utf16(name), 0, 0); err != nil {
		free(provider)
		return Saved{}, nil, nil, err
	}
	if err := call("finalize the key", ncryptFinalizeKey, key, 0); err != nil {
		free(key)
		free(provider)
		return Saved{}, nil, nil, err
	}
	public, err := exportPublic(key)
	if err != nil {
		_, _, _ = ncryptDeleteKey.Call(key, 0)
		free(provider)
		return Saved{}, nil, nil, err
	}
	return Saved{Protection: TPM, Handle: []byte(name)}, public, &tpmKey{provider: provider, key: key}, nil
}

func platformOpen(saved Saved) (*ecdsa.PublicKey, signer, error) {
	if saved.Protection != TPM {
		return nil, nil, fmt.Errorf("Windows keeps no %s keys", saved.Protection)
	}
	provider, err := openProvider()
	if err != nil {
		return nil, nil, err
	}
	var key uintptr
	if err := call("open the key", ncryptOpenKey, provider, uintptr(unsafe.Pointer(&key)), utf16(string(saved.Handle)), 0, 0); err != nil {
		free(provider)
		return nil, nil, err
	}
	public, err := exportPublic(key)
	if err != nil {
		free(key)
		free(provider)
		return nil, nil, err
	}
	return public, &tpmKey{provider: provider, key: key}, nil
}

func platformDelete(saved Saved) error {
	provider, err := openProvider()
	if err != nil {
		return err
	}
	defer free(provider)
	var key uintptr
	if err := call("open the key", ncryptOpenKey, provider, uintptr(unsafe.Pointer(&key)), utf16(string(saved.Handle)), 0, 0); err != nil {
		var status ncryptError
		// NTE_BAD_KEYSET: the key is already gone.
		if errors.As(err, &status) && status.status == 0x80090016 {
			return nil
		}
		return err
	}
	// NCryptDeleteKey frees the handle, whether it succeeds or not.
	return call("delete the key", ncryptDeleteKey, key, 0)
}

// exportPublic reads a BCRYPT_ECCPUBLIC_BLOB: the magic ECS1 and the length of
// a coordinate, then X and Y.
func exportPublic(key uintptr) (*ecdsa.PublicKey, error) {
	blob := make([]byte, 8+64)
	var size uint32
	if err := call("export the public key", ncryptExportKey, key, 0, utf16("ECCPUBLICBLOB"), 0, uintptr(unsafe.Pointer(&blob[0])), uintptr(len(blob)), uintptr(unsafe.Pointer(&size)), 0); err != nil {
		return nil, err
	}
	if size != 72 || binary.LittleEndian.Uint32(blob[0:4]) != 0x31534345 || binary.LittleEndian.Uint32(blob[4:8]) != 32 {
		return nil, errors.New("the TPM answered a public key that is not P-256")
	}
	return publicFromPoint(append([]byte{4}, blob[8:72]...))
}

// sign answers r‖s, the form NCrypt signs ECDSA in.
func (k *tpmKey) sign(digest []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key == 0 {
		return nil, errors.New("the key is closed")
	}
	signature := make([]byte, 64)
	var size uint32
	if err := call("sign", ncryptSignHash, k.key, 0, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), uintptr(unsafe.Pointer(&signature[0])), uintptr(len(signature)), uintptr(unsafe.Pointer(&size)), 0); err != nil {
		return nil, err
	}
	if size != 64 {
		return nil, fmt.Errorf("the TPM answered a %d-byte signature", size)
	}
	return signature, nil
}

func (k *tpmKey) close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	free(k.key)
	free(k.provider)
	k.key, k.provider = 0, 0
}
