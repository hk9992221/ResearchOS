// Package secret is the OS key-storage adapter (SPEC-02 §5.4, SPEC-00 §7).
//
// API keys live only in the operating-system credential store; databases,
// settings, logs and exports hold nothing but the opaque reference produced by
// ProviderKeyRef. There is deliberately no List method: keys must not be
// enumerable. Memory is a test-only store and must never be selected by
// release builds (SPEC-02 §5.4 rules).
package secret

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel errors for the store contract.
var (
	// ErrSecretNotFound reports a reference that has no entry (deleted or lost).
	ErrSecretNotFound = errors.New("secret not found")
	// ErrSecretStoreUnavailable reports that the OS secret store itself cannot
	// be reached on this machine.
	ErrSecretStoreUnavailable = errors.New("os secret store unavailable")
)

// Store is the minimal secret persistence surface.
type Store interface {
	Set(ctx context.Context, ref string, secret []byte) error
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
}

// ProviderKeyRef derives the fixed, opaque reference for one provider's key
// (SPEC-02 §4.5). The reference contains no key material.
func ProviderKeyRef(providerID string) string {
	return fmt.Sprintf("researchos/provider/%s/api-key", providerID)
}

// NewOS returns the platform secret store: Windows Credential Manager, macOS
// Keychain via the security CLI, freedesktop Secret Service via secret-tool.
// Platforms without a verified backend return calls that fail with
// ErrSecretStoreUnavailable; there is no plaintext file fallback.
func NewOS() Store {
	return osStore()
}
