//go:build !windows

package store

// Non-Windows secret backend: AES-256-GCM encrypted files, BYTE-COMPATIBLE
// with the Electron app's fallback format ({iv,tag,data} base64 JSON), so
// a token.enc.json written by either build decrypts in the other. The data
// key is a random 32-byte value in token.key (0600), a separate file from
// the ciphertext — best-effort against casual snooping, not a determined
// local attacker. That caveat is inherited from the Electron design and
// stays stated, not hidden.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const secretBackend = "file"

type packedSecret struct {
	IV   string `json:"iv"`
	Tag  string `json:"tag"`
	Data string `json:"data"`
}

func loadOrCreateFileKey(dir string) ([]byte, error) {
	keyPath := filepath.Join(dir, keyFile)
	if raw, err := os.ReadFile(keyPath); err == nil && len(raw) == 32 {
		return raw, nil
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, fresh, 0o600); err != nil {
		return nil, err
	}
	return fresh, nil
}

func encryptSecret(plaintext, dir string) (string, error) {
	key, err := loadOrCreateFileKey(dir)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	// Seal appends the auth tag; the Electron format stores it separately,
	// so split it off the tail (GCM tag = last 16 bytes).
	sealed := gcm.Seal(nil, iv, []byte(plaintext), nil)
	if len(sealed) < 16 {
		return "", errors.New("encryption produced truncated output")
	}
	data, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	raw, err := json.Marshal(packedSecret{
		IV:   base64.StdEncoding.EncodeToString(iv),
		Tag:  base64.StdEncoding.EncodeToString(tag),
		Data: base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decryptSecret(packed, dir string) (string, error) {
	var obj packedSecret
	if err := json.Unmarshal([]byte(packed), &obj); err != nil {
		return "", err
	}
	key, err := loadOrCreateFileKey(dir)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv, err := base64.StdEncoding.DecodeString(obj.IV)
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(obj.Data)
	if err != nil {
		return "", err
	}
	tag, err := base64.StdEncoding.DecodeString(obj.Tag)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, iv, append(data, tag...), nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
