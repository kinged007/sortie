//go:build windows

package usagesource

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// credTypeGeneric is CRED_TYPE_GENERIC, the type the runtime's keychain
// module stores its entries as.
const credTypeGeneric = 1

var (
	advapi32     = windows.NewLazySystemDLL("advapi32.dll")
	procCredRead = advapi32.NewProc("CredReadW")
	procCredFree = advapi32.NewProc("CredFree")
)

// credential is the head of the CREDENTIALW structure up to the blob, which
// is all the read needs; the system allocates and frees the whole record.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
}

// geminiCredentialManagerSecret returns the secret the runtime's keychain
// module stored under target, "service/account", or "" when the Credential
// Manager has no such entry or will not return it. The module writes the
// secret as UTF-8 bytes.
func geminiCredentialManagerSecret(target string) string {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return ""
	}
	var cred *credential
	ret, _, _ := procCredRead.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&cred))) //nolint:gosec // G103: the documented CredReadW call
	if ret == 0 || cred == nil {
		return ""
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred))) //nolint:errcheck,gosec // G103: releases the record CredReadW allocated

	return string(unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)) //nolint:gosec // G103: the blob lies in the record CredReadW returned
}
