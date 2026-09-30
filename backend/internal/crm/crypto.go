package crm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// keyCipher encrypts secrets (LLM API keys) with AES-256-GCM. The stored
// form is base64(nonce || ciphertext||tag).
type keyCipher struct {
	aead cipher.AEAD
}

func newKeyCipher(key []byte) *keyCipher {
	k := key
	if len(k) != 32 {
		sum := sha256.Sum256(key)
		k = sum[:]
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		// Unreachable: k is always 32 bytes.
		panic(fmt.Sprintf("crm: aes.NewCipher: %v", err))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("crm: cipher.NewGCM: %v", err))
	}
	return &keyCipher{aead: aead}
}

// encrypt returns "" for an empty plaintext so "no key" stays representable.
func (c *keyCipher) encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize(), c.aead.NonceSize()+len(plain)+c.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("crm: generate nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

var errCiphertext = errors.New("crm: malformed api key ciphertext")

func (c *keyCipher) decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCiphertext, err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns+c.aead.Overhead() {
		return "", errCiphertext
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("crm: decrypt api key (wrong CALLGO encryption key?): %w", err)
	}
	return string(plain), nil
}

// apiKeyHint renders a key as "sk-…q9Zt" (first 3 + last 4 characters). Keys
// too short to reveal anything safely are fully masked.
func apiKeyHint(key string) string {
	r := []rune(key)
	switch {
	case len(r) == 0:
		return ""
	case len(r) < 8:
		return "…"
	case len(r) < 12:
		return "…" + string(r[len(r)-2:])
	default:
		return string(r[:3]) + "…" + string(r[len(r)-4:])
	}
}
