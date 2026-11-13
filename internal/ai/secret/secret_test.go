package secret_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"researchos/internal/ai/secret"
)

func TestProviderKeyRefShape(t *testing.T) {
	got := secret.ProviderKeyRef("018c0000-0000-7000-8000-000000000000")
	want := "researchos/provider/018c0000-0000-7000-8000-000000000000/api-key"
	if got != want {
		t.Fatalf("ProviderKeyRef = %q want %q", got, want)
	}
}

func TestMemoryStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	m := secret.NewMemory()
	ref := "researchos/provider/p/api-key"

	if _, err := m.Get(ctx, ref); !errors.Is(err, secret.ErrSecretNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	secret1 := []byte("sk-secret1")
	if err := m.Set(ctx, ref, secret1); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(ctx, ref)
	if err != nil || !bytes.Equal(got, secret1) {
		t.Fatalf("Get = %q, %v", got, err)
	}
	// Returned slices are copies; mutating them must not corrupt the store.
	got[0] = 'X'
	again, _ := m.Get(ctx, ref)
	if again[0] != 's' {
		t.Fatal("memory store leaked internal slice")
	}
	if err := m.Set(ctx, ref, []byte("sk-secret2")); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, ref); !errors.Is(err, secret.ErrSecretNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if err := m.Delete(ctx, ref); err != nil {
		t.Fatalf("delete missing must be idempotent: %v", err)
	}
	if m.Has(ref) {
		t.Fatal("Has after delete")
	}
}

func TestMemoryStoreUnavailable(t *testing.T) {
	ctx := context.Background()
	m := secret.NewMemory()
	m.Unavailable = true
	if err := m.Set(ctx, "r", []byte("x")); !errors.Is(err, secret.ErrSecretStoreUnavailable) {
		t.Fatalf("Set: %v", err)
	}
	if _, err := m.Get(ctx, "r"); !errors.Is(err, secret.ErrSecretStoreUnavailable) {
		t.Fatalf("Get: %v", err)
	}
	if err := m.Delete(ctx, "r"); !errors.Is(err, secret.ErrSecretStoreUnavailable) {
		t.Fatalf("Delete: %v", err)
	}
}
