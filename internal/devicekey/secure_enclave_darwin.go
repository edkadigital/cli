//go:build darwin && cgo

package devicekey

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>

static CFMutableDictionaryRef edka_dictionary(void) {
	return CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
}

// The Secure Enclave wraps the private key of a key it makes with a key of its
// own and hands the result out as the key's token object ID ("toid", the
// attribute CryptoKit's dataRepresentation is). Only this Mac's Secure Enclave
// can unwrap it, so the key needs no keychain item and no entitlement.
static int edka_se_create(CFDataRef *wrapped, CFDataRef *point, CFErrorRef *error) {
	int bits = 256;
	CFNumberRef size = CFNumberCreate(NULL, kCFNumberIntType, &bits);
	CFMutableDictionaryRef private = edka_dictionary();
	CFDictionarySetValue(private, kSecAttrIsPermanent, kCFBooleanFalse);
	CFMutableDictionaryRef attributes = edka_dictionary();
	CFDictionarySetValue(attributes, kSecAttrKeyType, kSecAttrKeyTypeECSECPrimeRandom);
	CFDictionarySetValue(attributes, kSecAttrKeySizeInBits, size);
	CFDictionarySetValue(attributes, kSecAttrTokenID, kSecAttrTokenIDSecureEnclave);
	CFDictionarySetValue(attributes, kSecPrivateKeyAttrs, private);
	SecKeyRef key = SecKeyCreateRandomKey(attributes, error);
	CFRelease(attributes);
	CFRelease(private);
	CFRelease(size);
	if (key == NULL) return 0;
	int ok = 0;
	CFDictionaryRef keyAttributes = SecKeyCopyAttributes(key);
	SecKeyRef public = SecKeyCopyPublicKey(key);
	if (keyAttributes != NULL && public != NULL) {
		CFTypeRef toid = CFDictionaryGetValue(keyAttributes, CFSTR("toid"));
		if (toid != NULL && CFGetTypeID(toid) == CFDataGetTypeID()) {
			*point = SecKeyCopyExternalRepresentation(public, error);
			if (*point != NULL) {
				*wrapped = CFDataCreateCopy(NULL, toid);
				ok = 1;
			}
		}
	}
	if (public != NULL) CFRelease(public);
	if (keyAttributes != NULL) CFRelease(keyAttributes);
	CFRelease(key);
	return ok;
}

static SecKeyRef edka_se_open(const UInt8 *bytes, CFIndex length, CFErrorRef *error) {
	CFDataRef wrapped = CFDataCreate(NULL, bytes, length);
	CFDataRef empty = CFDataCreate(NULL, NULL, 0);
	CFMutableDictionaryRef attributes = edka_dictionary();
	CFDictionarySetValue(attributes, kSecAttrKeyType, kSecAttrKeyTypeECSECPrimeRandom);
	CFDictionarySetValue(attributes, kSecAttrKeyClass, kSecAttrKeyClassPrivate);
	CFDictionarySetValue(attributes, kSecAttrTokenID, kSecAttrTokenIDSecureEnclave);
	CFDictionarySetValue(attributes, CFSTR("toid"), wrapped);
	SecKeyRef key = SecKeyCreateWithData(empty, attributes, error);
	CFRelease(attributes);
	CFRelease(empty);
	CFRelease(wrapped);
	return key;
}

static CFDataRef edka_se_public(SecKeyRef key, CFErrorRef *error) {
	SecKeyRef public = SecKeyCopyPublicKey(key);
	if (public == NULL) return NULL;
	CFDataRef point = SecKeyCopyExternalRepresentation(public, error);
	CFRelease(public);
	return point;
}

static CFDataRef edka_se_sign(SecKeyRef key, const UInt8 *digest, CFIndex length, CFErrorRef *error) {
	CFDataRef data = CFDataCreate(NULL, digest, length);
	CFDataRef signature = SecKeyCreateSignature(key, kSecKeyAlgorithmECDSASignatureDigestX962SHA256, data, error);
	CFRelease(data);
	return signature;
}
*/
import "C"

import (
	"crypto/ecdsa"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"unsafe"
)

type secureEnclave struct {
	mu  sync.Mutex
	key C.SecKeyRef
}

func platformCreate() (Saved, *ecdsa.PublicKey, signer, error) {
	var wrapped, point C.CFDataRef
	var cfError C.CFErrorRef
	if C.edka_se_create(&wrapped, &point, &cfError) == 0 {
		// A Mac without a Secure Enclave (an Intel Mac without a T2 chip, a
		// virtual machine) answers that the token is not available.
		return Saved{}, nil, nil, errors.Join(errNoHardware, takeError(cfError))
	}
	saved := Saved{Protection: SecureEnclave, Handle: takeData(wrapped)}
	public, err := publicFromPoint(takeData(point))
	if err != nil {
		return Saved{}, nil, nil, err
	}
	_, s, err := platformOpen(saved)
	if err != nil {
		return Saved{}, nil, nil, err
	}
	return saved, public, s, nil
}

func platformOpen(saved Saved) (*ecdsa.PublicKey, signer, error) {
	if saved.Protection != SecureEnclave {
		return nil, nil, fmt.Errorf("a Mac keeps no %s keys", saved.Protection)
	}
	if len(saved.Handle) == 0 {
		return nil, nil, errors.New("the saved key is empty")
	}
	var cfError C.CFErrorRef
	key := C.edka_se_open((*C.UInt8)(unsafe.Pointer(&saved.Handle[0])), C.CFIndex(len(saved.Handle)), &cfError)
	if key == 0 {
		return nil, nil, fmt.Errorf("the Secure Enclave refused the saved key (it was made on another Mac): %w", takeError(cfError))
	}
	point := C.edka_se_public(key, &cfError)
	if point == 0 {
		C.CFRelease(C.CFTypeRef(key))
		return nil, nil, takeError(cfError)
	}
	public, err := publicFromPoint(takeData(point))
	if err != nil {
		C.CFRelease(C.CFTypeRef(key))
		return nil, nil, err
	}
	return public, &secureEnclave{key: key}, nil
}

// The Secure Enclave keeps nothing of a key it wrapped: deleting the saved
// handle deletes the key.
func platformDelete(Saved) error { return nil }

func (s *secureEnclave) sign(digest []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == 0 {
		return nil, errors.New("the key is closed")
	}
	var cfError C.CFErrorRef
	signature := C.edka_se_sign(s.key, (*C.UInt8)(unsafe.Pointer(&digest[0])), C.CFIndex(len(digest)), &cfError)
	if signature == 0 {
		return nil, takeError(cfError)
	}
	return rawSignature(takeData(signature))
}

func (s *secureEnclave) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != 0 {
		C.CFRelease(C.CFTypeRef(s.key))
		s.key = 0
	}
}

// takeData copies a CFData the caller owns into Go memory and releases it.
func takeData(data C.CFDataRef) []byte {
	defer C.CFRelease(C.CFTypeRef(data))
	length := C.CFDataGetLength(data)
	if length == 0 {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(data)), C.int(length))
}

// takeError reads a CFError the caller owns and releases it.
func takeError(cfError C.CFErrorRef) error {
	if cfError == 0 {
		return errors.New("the Secure Enclave answered no error")
	}
	defer C.CFRelease(C.CFTypeRef(cfError))
	description := C.CFErrorCopyDescription(cfError)
	if description == 0 {
		return fmt.Errorf("Secure Enclave error %d", int(C.CFErrorGetCode(cfError)))
	}
	defer C.CFRelease(C.CFTypeRef(description))
	buffer := make([]byte, 512)
	if C.CFStringGetCString(description, (*C.char)(unsafe.Pointer(&buffer[0])), C.CFIndex(len(buffer)), C.kCFStringEncodingUTF8) == 0 {
		return fmt.Errorf("Secure Enclave error %d", int(C.CFErrorGetCode(cfError)))
	}
	return errors.New(C.GoString((*C.char)(unsafe.Pointer(&buffer[0]))))
}

// rawSignature turns the ASN.1 ECDSA signature the Secure Enclave answers into r‖s.
func rawSignature(der []byte) ([]byte, error) {
	var parsed struct{ R, S *big.Int }
	if rest, err := asn1.Unmarshal(der, &parsed); err != nil || len(rest) != 0 {
		return nil, errors.New("signature is not an ASN.1 ECDSA signature")
	}
	if parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 || parsed.R.BitLen() > 256 || parsed.S.BitLen() > 256 {
		return nil, errors.New("signature is out of range for P-256")
	}
	out := make([]byte, 64)
	parsed.R.FillBytes(out[:32])
	parsed.S.FillBytes(out[32:])
	return out, nil
}
