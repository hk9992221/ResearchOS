//go:build windows

// Real Credential Manager round-trip against the live wincred store
// (P00a acceptance, AC-02.4). It writes one throwaway target and deletes it.
package secret_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"researchos/internal/ai/secret"
)

func TestWincredRoundTrip(t *testing.T) {
	if os.Getenv("RESEARCHOS_RUN_WINCRED") == "" {
		t.Skip("set RESEARCHOS_RUN_WINCRED=1 to exercise the live Windows Credential Manager")
	}
	ctx := context.Background()
	s := secret.NewOS()
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatal(err)
	}
	ref := "researchos/provider/itest-" + hex.EncodeToString(rnd[:]) + "/api-key"
	value := []byte("sk-wincred-itest-" + hex.EncodeToString(rnd[:]))
	t.Cleanup(func() { _ = s.Delete(ctx, ref) })

	if err := s.Set(ctx, ref, value); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(value) {
		t.Fatalf("round trip mismatch")
	}
	if err := s.Set(ctx, ref, []byte("sk-replaced")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ = s.Get(ctx, ref)
	if string(got) != "sk-replaced" {
		t.Fatalf("old key still readable")
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, ref); !errors.Is(err, secret.ErrSecretNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
}
