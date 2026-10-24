//go:build windows

package store

// Windows secret backend: DPAPI (CryptProtectData / CryptUnprotectData,
// the same OS protection Windows Credential Manager itself uses) with the
// ciphertext in token.enc.json / secret-<name>.enc.json as
// {"dpapi":"<base64>"}. Blobs are bound to the owner's Windows user account —
// copying the files to another machine or user makes them undecryptable,
// which is the point.
//
// DIFFERENCE FROM THE ELECTRON BUILD (honest, documented in HANDOFF.md):
// Electron stored secrets in Credential Manager via keytar. DPAPI blobs
// in files are NOT the same store, so keys saved by the Electron build do
// not appear here — they are pasted once into the Go build's Settings.
// settings.json (not secret) is shared and DOES carry over.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const secretBackend = "dpapi"

type packedSecret struct {
	DPAPI string `json:"dpapi"`
}

func dpapiBlob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func dpapiOutBytes(out *windows.DataBlob) []byte {
	if out.Data == nil || out.Size == 0 {
		return nil
	}
	b := make([]byte, out.Size)
	copy(b, unsafe.Slice(out.Data, out.Size))
	return b
}

func encryptSecret(plaintext, _ string) (string, error) {
	in := dpapiBlob([]byte(plaintext))
	var out windows.DataBlob
	// CRYPTPROTECT_UI_FORBIDDEN (0x1): never show a prompt from a server process.
	if err := windows.CryptProtectData(in, nil, nil, 0, nil, 0x1, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	raw, err := json.Marshal(packedSecret{DPAPI: base64.StdEncoding.EncodeToString(dpapiOutBytes(&out))})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decryptSecret(packed, _ string) (string, error) {
	var obj packedSecret
	if err := json.Unmarshal([]byte(packed), &obj); err != nil {
		return "", err
	}
	blob, err := base64.StdEncoding.DecodeString(obj.DPAPI)
	if err != nil {
		return "", err
	}
	if len(blob) == 0 {
		return "", errors.New("empty dpapi blob")
	}
	in := dpapiBlob(blob)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(in, nil, nil, 0, nil, 0x1, &out); err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return string(dpapiOutBytes(&out)), nil
}
