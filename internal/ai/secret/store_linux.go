//go:build linux

package secret

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// secretServiceStore talks to the freedesktop Secret Service through the
// libsecret secret-tool CLI, attributes service=researchos target=<ref>.
// Without a desktop key service secret-tool fails and the store reports
// ErrSecretStoreUnavailable (SPEC-02 §5.4); keys never appear in argv.
type secretServiceStore struct{}

func osStore() Store { return secretServiceStore{} }

func (secretServiceStore) Set(ctx context.Context, ref string, secret []byte) error {
	cmd := exec.CommandContext(ctx, "secret-tool", "store",
		"--label", "ResearchOS AI provider key",
		"service", "researchos", "target", ref)
	cmd.Stdin = bytes.NewReader(secret)
	if err := cmd.Run(); err != nil {
		return ssError("secret-tool store", err)
	}
	return nil
}

func (secretServiceStore) Get(ctx context.Context, ref string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup",
		"service", "researchos", "target", ref)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, ErrSecretStoreUnavailable
	}
	if err != nil {
		// exit 1 with no output: no matching item.
		if errBuf.Len() == 0 && out.Len() == 0 {
			return nil, ErrSecretNotFound
		}
		return nil, ssError("secret-tool lookup", err)
	}
	return out.Bytes(), nil
}

func (secretServiceStore) Delete(ctx context.Context, ref string) error {
	cmd := exec.CommandContext(ctx, "secret-tool", "clear",
		"service", "researchos", "target", ref)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return ErrSecretStoreUnavailable
	}
	if err != nil {
		if errBuf.Len() == 0 {
			return nil // already absent: idempotent (§5.3.3)
		}
		return ssError("secret-tool clear", err)
	}
	return nil
}

func ssError(op string, err error) error {
	var ee *exec.Error
	if errors.As(err, &ee) {
		return ErrSecretStoreUnavailable
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 &&
		(bytes.Contains(exitErr.Stderr, []byte("Cannot establish")) ||
			bytes.Contains(exitErr.Stderr, []byte("connection"))) {
		return ErrSecretStoreUnavailable
	}
	return errors.New("secret: " + op + ": " + err.Error())
}
