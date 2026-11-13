//go:build !windows && !darwin && !linux

package secret

import "context"

// unsupportedStore has no verified pure Go / CLI backend on this platform.
// SPEC-02 §5.4 forbids a plaintext file fallback, so every operation reports
// the store as unavailable and key-requiring providers cannot be enabled.
type unsupportedStore struct{}

func osStore() Store { return unsupportedStore{} }

func (unsupportedStore) Set(context.Context, string, []byte) error {
	return ErrSecretStoreUnavailable
}

func (unsupportedStore) Get(context.Context, string) ([]byte, error) {
	return nil, ErrSecretStoreUnavailable
}

func (unsupportedStore) Delete(context.Context, string) error {
	return ErrSecretStoreUnavailable
}
