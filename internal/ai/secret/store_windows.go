//go:build windows

package secret

import (
	"context"
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wincredStore stores one generic credential per reference in the current
// user's Windows Credential Manager (SPEC-02 §5.4). Pure Go: advapi32 is
// called through a lazy DLL because x/sys exposes no Cred* wrappers.
//
// The CREDENTIAL layout below is the NATIVE wincred.h shape, not the member
// list shown by the online docs (that ordering is the managed projection).
// Verified against danieljoos/wincred and a live round-trip on Windows 11
// 24H2: Flags, Type, TargetName, Comment, LastWritten, BlobSize, Blob,
// Persist, AttributeCount, Attributes, TargetAlias, UserName.
type wincredStore struct{}

var (
	advapi32                = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW          = advapi32.NewProc("CredWriteW")
	procCredReadW           = advapi32.NewProc("CredReadW")
	procCredDeleteW         = advapi32.NewProc("CredDeleteW")
	procCredFree            = advapi32.NewProc("CredFree")
	errCredentialNotFound   = syscall.Errno(1168) // ERROR_NOT_FOUND
	credTypeGeneric         = uint32(1)           // CRED_TYPE_GENERIC
	credPersistLocalMachine = uint32(2)           // CRED_PERSIST_LOCAL_MACHINE
)

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func osStore() Store { return wincredStore{} }

func (wincredStore) Set(_ context.Context, ref string, secret []byte) error {
	target, err := windows.UTF16PtrFromString(ref)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString("researchos")
	if err != nil {
		return err
	}
	comment, err := windows.UTF16PtrFromString("ResearchOS AI provider key")
	if err != nil {
		return err
	}
	cred := credential{
		Type:               credTypeGeneric,
		TargetName:         target,
		Comment:            comment,
		CredentialBlobSize: uint32(len(secret)),
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	if len(secret) > 0 {
		cred.CredentialBlob = &secret[0]
	}
	r1, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	runtime.KeepAlive(secret)
	if r1 == 0 {
		return translateCredError(callErr, "wincred: set")
	}
	return nil
}

func (wincredStore) Get(_ context.Context, ref string) ([]byte, error) {
	target, err := windows.UTF16PtrFromString(ref)
	if err != nil {
		return nil, err
	}
	var credPtr *credential
	r1, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric), 0,
		uintptr(unsafe.Pointer(&credPtr)),
	)
	if r1 == 0 {
		return nil, translateCredError(callErr, "wincred: get")
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credPtr)))
	if credPtr == nil || credPtr.CredentialBlob == nil || credPtr.CredentialBlobSize == 0 {
		return []byte{}, nil
	}
	out := make([]byte, credPtr.CredentialBlobSize)
	copy(out, unsafe.Slice(credPtr.CredentialBlob, credPtr.CredentialBlobSize))
	return out, nil
}

func (wincredStore) Delete(_ context.Context, ref string) error {
	target, err := windows.UTF16PtrFromString(ref)
	if err != nil {
		return err
	}
	r1, _, callErr := procCredDeleteW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric), 0,
	)
	if r1 == 0 {
		// An already-absent entry is an idempotent success: provider removal
		// must still be able to clean up its config row (SPEC-02 §5.3.3).
		if errors.Is(callErr, errCredentialNotFound) {
			return nil
		}
		return translateCredError(callErr, "wincred: delete")
	}
	return nil
}

func translateCredError(err error, op string) error {
	if errno, ok := err.(syscall.Errno); ok {
		switch errno {
		case errCredentialNotFound:
			return ErrSecretNotFound
		case syscall.Errno(1115), syscall.Errno(1701), syscall.Errno(6):
			// Vault service stopped / RPC endpoint missing → store outage.
			return ErrSecretStoreUnavailable
		}
		return errors.New(op + ": " + errno.Error())
	}
	return err
}
