//go:build windows

package credentials

// Windows: a generic credential in Credential Manager, mirroring
// windows-native-keyring-store 1.1.0 with its default configuration
// (Store::new_with_configuration with no options):
//
//   - TargetName "<key>.<service>": prefix "", divider ".", suffix "".
//     (store.rs new_with_configuration L146-170, cred.rs
//     build_from_specifiers L307-344)
//   - Type CRED_TYPE_GENERIC, Flags 0, Persist CRED_PERSIST_ENTERPRISE, no
//     attributes. (store.rs build L207-228, utils.rs save_credential L129-175)
//   - A new credential gets UserName <key>, empty TargetAlias and Comment; an
//     existing one keeps its UserName, TargetAlias and Comment.
//     (cred.rs set_secret L363-385)
//   - The blob is the value as UTF-16LE without a terminating NUL; reads
//     reject odd lengths and invalid UTF-16. (utils.rs validate_password
//     L79-94, extract_password L275-297)
//   - Limits checked before any call: key at most 513 UTF-8 bytes, target at
//     most 32767 UTF-8 bytes, blob at most 2560 bytes. (utils.rs
//     validate_target L57-77, validate_password)
//   - ERROR_NOT_FOUND is a missing entry; every other error is a failure.
//     (utils.rs decode_error L394-400)

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var platform backend = credentialManager{}

const (
	credTypeGeneric           = 1
	credPersistEnterprise     = 3
	credMaxUsernameLength     = 513
	credMaxGenericTargetName  = 32767
	credMaxCredentialBlobSize = 5 * 512
)

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW  = advapi32.NewProc("CredReadW")
	procCredWriteW = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

// credentialW is the Win32 CREDENTIALW structure.
type credentialW struct {
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

type credentialManager struct{}

// targetName builds and validates the TargetName for an entry.
func targetName(service, key string) (string, error) {
	target := key + "." + service
	if len(key) > credMaxUsernameLength {
		return "", errors.New("credential manager: key too long")
	}
	if len(target) > credMaxGenericTargetName {
		return "", errors.New("credential manager: target name too long")
	}
	return target, nil
}

func (credentialManager) get(service, key string) (string, bool, error) {
	target, err := targetName(service, key)
	if err != nil {
		return "", false, err
	}
	cred, found, err := credRead(target)
	if err != nil || !found {
		return "", false, err
	}
	defer credFree(cred)
	blob := credBlob(cred)
	defer clear(blob)
	value, err := decodeUTF16LE(blob)
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (credentialManager) set(service, key, value string) error {
	target, err := targetName(service, key)
	if err != nil {
		return err
	}
	blob := encodeUTF16LE(value)
	defer clear(blob)
	if len(blob) > credMaxCredentialBlobSize {
		return errors.New("credential manager: value too long")
	}
	username, alias, comment := key, "", ""
	if existing, found, err := credRead(target); err == nil && found {
		username = windows.UTF16PtrToString(existing.UserName)
		alias = windows.UTF16PtrToString(existing.TargetAlias)
		comment = windows.UTF16PtrToString(existing.Comment)
		clear(credBlob(existing))
		credFree(existing)
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	usernamePtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	aliasPtr, err := windows.UTF16PtrFromString(alias)
	if err != nil {
		return err
	}
	commentPtr, err := windows.UTF16PtrFromString(comment)
	if err != nil {
		return err
	}
	cred := credentialW{
		Type:               credTypeGeneric,
		TargetName:         targetPtr,
		Comment:            commentPtr,
		CredentialBlobSize: uint32(len(blob)),
		Persist:            credPersistEnterprise,
		TargetAlias:        aliasPtr,
		UserName:           usernamePtr,
	}
	if len(blob) > 0 {
		cred.CredentialBlob = &blob[0]
	}
	if r, _, e := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0); r == 0 {
		return fmt.Errorf("credential manager: CredWriteW: %w", e)
	}
	return nil
}

func (credentialManager) remove(service, key string) error {
	target, err := targetName(service, key)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if r, _, e := procCredDelete.Call(uintptr(unsafe.Pointer(targetPtr)), credTypeGeneric, 0); r == 0 {
		if errors.Is(e, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("credential manager: CredDeleteW: %w", e)
	}
	return nil
}

// credRead reads a generic credential; the caller must credFree it.
func credRead(target string) (*credentialW, bool, error) {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, false, err
	}
	var cred *credentialW
	if r, _, e := procCredReadW.Call(uintptr(unsafe.Pointer(targetPtr)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&cred))); r == 0 {
		if errors.Is(e, windows.ERROR_NOT_FOUND) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("credential manager: CredReadW: %w", e)
	}
	return cred, true, nil
}

func credFree(cred *credentialW) {
	_, _, _ = procCredFree.Call(uintptr(unsafe.Pointer(cred)))
}

// credBlob aliases the credential's blob memory (owned by Windows).
func credBlob(cred *credentialW) []byte {
	if cred.CredentialBlobSize == 0 || cred.CredentialBlob == nil {
		return nil
	}
	return unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)
}
