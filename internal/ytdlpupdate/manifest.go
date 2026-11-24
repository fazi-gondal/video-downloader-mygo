// Package ytdlpupdate defines the signed update manifest shared by the app and
// the publisher command.
package ytdlpupdate

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
)

// File is one platform's update asset.
type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is signed as exact bytes; verify before decoding.
type Manifest struct {
	Version     string          `json:"version"`
	GeneratedAt string          `json:"generatedAt,omitempty"`
	Files       map[string]File `json:"files"`
}

var (
	ErrSignature      = errors.New("update manifest signature is invalid")
	ErrFormat         = errors.New("update manifest format is invalid")
	ErrNoPlatformFile = errors.New("update manifest has no file for this platform")
)

// PlatformKey is the manifest key for a GOOS/GOARCH pair.
func PlatformKey(goos, goarch string) string {
	return strings.ToLower(goos) + "-" + strings.ToLower(goarch)
}

func CurrentPlatformKey() string {
	return PlatformKey(runtime.GOOS, runtime.GOARCH)
}

// Sign signs the exact manifest bytes with an Ed25519 private key.
func Sign(data []byte, privateKey ed25519.PrivateKey) []byte {
	return ed25519.Sign(privateKey, data)
}

// Verify checks the signature before parsing the manifest.
func Verify(data, signature []byte, publicKey ed25519.PublicKey) (Manifest, error) {
	if len(publicKey) != ed25519.PublicKeySize ||
		len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(publicKey, data, signature) {
		return Manifest{}, ErrSignature
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrFormat, err)
	}
	manifest.Version = strings.TrimSpace(manifest.Version)
	if manifest.Version == "" || len(manifest.Files) == 0 {
		return Manifest{}, fmt.Errorf("%w: version or files are missing", ErrFormat)
	}
	for key, file := range manifest.Files {
		if err := validateFile(file); err != nil {
			return Manifest{}, fmt.Errorf("%w: %s: %v", ErrFormat, key, err)
		}
	}
	return manifest, nil
}

// FileForCurrentPlatform returns the file for the running platform.
func (m Manifest) FileForCurrentPlatform() (File, error) {
	return m.FileFor(runtime.GOOS, runtime.GOARCH)
}

// FileFor returns the file for a platform.
func (m Manifest) FileFor(goos, goarch string) (File, error) {
	file, ok := m.Files[PlatformKey(goos, goarch)]
	if !ok {
		return File{}, fmt.Errorf("%w: %s", ErrNoPlatformFile, PlatformKey(goos, goarch))
	}
	return file, nil
}

func validateFile(file File) error {
	file.URL = strings.TrimSpace(file.URL)
	parsed, err := url.Parse(file.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("url must be an https URL without credentials")
	}
	if len(file.SHA256) != 64 {
		return errors.New("sha256 must be 64 hex characters")
	}
	if _, err := hex.DecodeString(file.SHA256); err != nil {
		return errors.New("sha256 must be 64 hex characters")
	}
	if file.Size <= 0 {
		return errors.New("size must be positive")
	}
	return nil
}
