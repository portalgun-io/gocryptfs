// +build !without_openssl

// Package stupidgcm is a thin wrapper for OpenSSL's GCM encryption and
// decryption functions. It only support 32-byte keys and 16-bit IVs.
package stupidgcm

// #include <openssl/evp.h>
// #cgo pkg-config: libcrypto
import "C"

import (
	"crypto/cipher"
	"fmt"
	"log"
	"unsafe"
)

const (
	// BuiltWithoutOpenssl indicates if openssl been disabled at compile-time
	BuiltWithoutOpenssl = false

	keyLen = 32
	ivLen  = 16
	tagLen = 16
)

// StupidGCM implements the cipher.AEAD interface
type StupidGCM struct {
	key         []byte
	forceDecode bool
}

// Verify that we satisfy the cipher.AEAD interface
var _ cipher.AEAD = &StupidGCM{}

// New returns a new cipher.AEAD implementation..
func New(keyIn []byte, forceDecode bool) cipher.AEAD {
	if len(keyIn) != keyLen {
		log.Panicf("Only %d-byte keys are supported", keyLen)
	}
	// Create a private copy of the key
	key := append([]byte{}, keyIn...)
	return &StupidGCM{key: key, forceDecode: forceDecode}
}

// NonceSize returns the required size of the nonce / IV.
func (g *StupidGCM) NonceSize() int {
	return ivLen
}

// Overhead returns the number of bytes that are added for authentication.
func (g *StupidGCM) Overhead() int {
	return tagLen
}

// Seal encrypts "in" using "iv" and "authData" and append the result to "dst"
func (g *StupidGCM) Seal(dst, iv, in, authData []byte) []byte {
	if len(iv) != ivLen {
		log.Panicf("Only %d-byte IVs are supported", ivLen)
	}
	if len(in) == 0 {
		log.Panic("Zero-length input data is not supported")
	}
	if len(g.key) != keyLen {
		log.Panicf("Wrong key length: %d. Key has been wiped?", len(g.key))
	}

	// If the "dst" slice is large enough we can use it as our output buffer
	outLen := len(in) + tagLen
	var buf []byte
	inplace := false
	if cap(dst)-len(dst) >= outLen {
		inplace = true
		buf = dst[len(dst) : len(dst)+outLen]
	} else {
		buf = make([]byte, outLen)
	}

	// https://wiki.openssl.org/index.php/EVP_Authenticated_Encryption_and_Decryption#Authenticated_Encryption_using_GCM_mode

	// Create scratch space "context"
	ctx := C.EVP_CIPHER_CTX_new()
	if ctx == nil {
		log.Panic("EVP_CIPHER_CTX_new failed")
	}

	// Set cipher to AES-256
	if C.EVP_EncryptInit_ex(ctx, C.EVP_aes_256_gcm(), nil, nil, nil) != 1 {
		log.Panic("EVP_EncryptInit_ex I failed")
	}

	// Use 16-byte IV
	if C.EVP_CIPHER_CTX_ctrl(ctx, C.EVP_CTRL_GCM_SET_IVLEN, ivLen, nil) != 1 {
		log.Panic("EVP_CIPHER_CTX_ctrl EVP_CTRL_GCM_SET_IVLEN failed")
	}

	// Set key and IV
	if C.EVP_EncryptInit_ex(ctx, nil, nil, (*C.uchar)(&g.key[0]), (*C.uchar)(&iv[0])) != 1 {
		log.Panic("EVP_EncryptInit_ex II failed")
	}

	// Provide authentication data
	var resultLen C.int
	if C.EVP_EncryptUpdate(ctx, nil, &resultLen, (*C.uchar)(&authData[0]), C.int(len(authData))) != 1 {
		log.Panic("EVP_EncryptUpdate authData failed")
	}
	if int(resultLen) != len(authData) {
		log.Panicf("Unexpected length %d", resultLen)
	}

	// Encrypt "in" into "buf"
	if C.EVP_EncryptUpdate(ctx, (*C.uchar)(&buf[0]), &resultLen, (*C.uchar)(&in[0]), C.int(len(in))) != 1 {
		log.Panic("EVP_EncryptUpdate failed")
	}
	if int(resultLen) != len(in) {
		log.Panicf("Unexpected length %d", resultLen)
	}

	// Finalise encryption
	// Because GCM is a stream encryption, this will not write out any data.
	dummy := make([]byte, 16)
	if C.EVP_EncryptFinal_ex(ctx, (*C.uchar)(&dummy[0]), &resultLen) != 1 {
		log.Panic("EVP_EncryptFinal_ex failed")
	}
	if resultLen != 0 {
		log.Panicf("Unexpected length %d", resultLen)
	}

	// Get GMAC tag and append it to the ciphertext in "buf"
	if C.EVP_CIPHER_CTX_ctrl(ctx, C.EVP_CTRL_GCM_GET_TAG, tagLen, (unsafe.Pointer)(&buf[len(in)])) != 1 {
		log.Panic("EVP_CIPHER_CTX_ctrl EVP_CTRL_GCM_GET_TAG failed")
	}

	// Free scratch space
	C.EVP_CIPHER_CTX_free(ctx)

	if inplace {
		return dst[:len(dst)+outLen]
	}
	return append(dst, buf...)
}

// Open decrypts "in" using "iv" and "authData" and append the result to "dst"
func (g *StupidGCM) Open(dst, iv, in, authData []byte) ([]byte, error) {
	if len(iv) != ivLen {
		log.Panicf("Only %d-byte IVs are supported", ivLen)
	}
	if len(g.key) != keyLen {
		log.Panicf("Wrong key length: %d. Key has been wiped?", len(g.key))
	}
	if len(in) <= tagLen {
		return nil, fmt.Errorf("stupidgcm: input data too short (%d bytes)", len(in))
	}

	// If the "dst" slice is large enough we can use it as our output buffer
	outLen := len(in) - tagLen
	var buf []byte
	inplace := false
	if cap(dst)-len(dst) >= outLen {
		inplace = true
		buf = dst[len(dst) : len(dst)+outLen]
	} else {
		buf = make([]byte, len(in)-tagLen)
	}

	ciphertext := in[:len(in)-tagLen]
	tag := in[len(in)-tagLen:]

	// https://wiki.openssl.org/index.php/EVP_Authenticated_Encryption_and_Decryption#Authenticated_Encryption_using_GCM_mode

	// Create scratch space "context"
	ctx := C.EVP_CIPHER_CTX_new()
	if ctx == nil {
		log.Panic("EVP_CIPHER_CTX_new failed")
	}

	// Set cipher to AES-256
	if C.EVP_DecryptInit_ex(ctx, C.EVP_aes_256_gcm(), nil, nil, nil) != 1 {
		log.Panic("EVP_DecryptInit_ex I failed")
	}

	// Use 16-byte IV
	if C.EVP_CIPHER_CTX_ctrl(ctx, C.EVP_CTRL_GCM_SET_IVLEN, ivLen, nil) != 1 {
		log.Panic("EVP_CIPHER_CTX_ctrl EVP_CTRL_GCM_SET_IVLEN failed")
	}

	// Set key and IV
	if C.EVP_DecryptInit_ex(ctx, nil, nil, (*C.uchar)(&g.key[0]), (*C.uchar)(&iv[0])) != 1 {
		log.Panic("EVP_DecryptInit_ex II failed")
	}

	// Set expected GMAC tag
	if C.EVP_CIPHER_CTX_ctrl(ctx, C.EVP_CTRL_GCM_SET_TAG, tagLen, (unsafe.Pointer)(&tag[0])) != 1 {
		log.Panic("EVP_CIPHER_CTX_ctrl failed")
	}

	// Provide authentication data
	var resultLen C.int
	if C.EVP_DecryptUpdate(ctx, nil, &resultLen, (*C.uchar)(&authData[0]), C.int(len(authData))) != 1 {
		log.Panic("EVP_DecryptUpdate authData failed")
	}
	if int(resultLen) != len(authData) {
		log.Panicf("Unexpected length %d", resultLen)
	}

	// Decrypt "ciphertext" into "buf"
	if C.EVP_DecryptUpdate(ctx, (*C.uchar)(&buf[0]), &resultLen, (*C.uchar)(&ciphertext[0]), C.int(len(ciphertext))) != 1 {
		log.Panic("EVP_DecryptUpdate failed")
	}
	if int(resultLen) != len(ciphertext) {
		log.Panicf("Unexpected length %d", resultLen)
	}

	// Check GMAC
	dummy := make([]byte, 16)
	res := C.EVP_DecryptFinal_ex(ctx, (*C.uchar)(&dummy[0]), &resultLen)
	if resultLen != 0 {
		log.Panicf("Unexpected length %d", resultLen)
	}

	// Free scratch space
	C.EVP_CIPHER_CTX_free(ctx)

	if res != 1 {
		// The error code must always be checked by the calling function, because the decrypted buffer
		// may contain corrupted data that we are returning in case the user forced reads
		if g.forceDecode == true {
			return append(dst, buf...), ErrAuth
		}
		return nil, ErrAuth
	}

	if inplace {
		return dst[:len(dst)+outLen], nil
	}
	return append(dst, buf...), nil
}

// Wipe tries to wipe the AES key from memory by overwriting it with zeros
// and setting the reference to nil.
//
// This is not bulletproof due to possible GC copies, but
// still raises to bar for extracting the key.
func (g *StupidGCM) Wipe() {
	for i := range g.key {
		g.key[i] = 0
	}
	g.key = nil
}
