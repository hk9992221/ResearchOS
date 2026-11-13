//go:build darwin

package secret

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// keychainStore drives the macOS security CLI. The key material crosses the
// process boundary only through stdin/stdout pipes, never argv (SPEC-02 §5.4).
type keychainStore struct{}

func osStore() Store { return keychainStore{} }

const keychainAccount = "researchos"

func (keychainStore) Set(ctx context.Context, ref string, secret []byte) error {
	// -w with no value makes security read the password from stdin.
	cmd := exec.CommandContext(ctx, "security", "add-generic-password",
		"-a", keychainAccount, "-s", ref, "-l", "ResearchOS AI provider key", "-U", "-w")
	cmd.Stdin = bytes.NewReader(append(secret, '\n'))
	if err := cmd.Run(); err != nil {
		return cliError("security add-generic-password", err)
	}
	return nil
}

func (keychainStore) Get(ctx context.Context, ref string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-a", keychainAccount, "-s", ref, "-w")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if strings.Contains(errBuf.String(), "could not be found") {
			return nil, ErrSecretNotFound
		}
		return nil, cliError("security find-generic-password", err)
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

func (keychainStore) Delete(ctx context.Context, ref string) error {
	cmd := exec.CommandContext(ctx, "security", "delete-generic-password",
		"-a", keychainAccount, "-s", ref)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if strings.Contains(errBuf.String(), "could not be found") {
			return nil
		}
		return cliError("security delete-generic-password", err)
	}
	return nil
}

func cliError(op string, err error) error {
	var ee *exec.Error
	if errors.As(err, &ee) {
		return ErrSecretStoreUnavailable
	}
	return errors.New("secret: " + op + ": " + err.Error())
}
