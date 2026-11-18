//go:build darwin && cgo

package credentials

// macOS: a generic password in the user's default keychain, mirroring
// apple-native-keyring-store 1.0.2's "keychain" module (default
// configuration) on security-framework 3.7.0. The calls are the same legacy
// Keychain Services calls, made the same way:
//
//   - Keychain: SecKeychainCopyDomainDefault(kSecPreferencesDomainUser).
//     (keychain.rs get_keychain L403-414; security-framework
//     os/macos/keychain.rs default_for_domain)
//   - Item: generic password with service = <service>, account = <key>; data
//     is the value's UTF-8 bytes. Empty service or key is rejected, since
//     empty attributes act as wildcards. (keychain.rs Cred::build L190-209)
//   - Read: SecKeychainFindGenericPassword restricted to that keychain;
//     errSecItemNotFound is a missing entry; data must be UTF-8.
//     (keychain.rs get_secret L132-137, decode_error L420-430)
//   - Write: if the item exists, SecKeychainItemModifyAttributesAndData with
//     the new data; otherwise SecKeychainAddGenericPassword.
//     (security-framework os/macos/passwords.rs set_generic_password)
//   - Delete: SecItemDelete with kSecMatchSearchList = [that keychain],
//     kSecClass = generic password, kSecAttrService, kSecAttrAccount;
//     errSecItemNotFound is a missing entry. (keychain.rs delete_credential
//     L140-149; security-framework item.rs ItemSearchOptions::delete)

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <stdint.h>
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

// durin_kc_open returns the user's default keychain and a one-element search
// list holding it. The caller releases both on success.
static int32_t durin_kc_open(SecKeychainRef *keychain, CFArrayRef *list) {
	*keychain = NULL;
	*list = NULL;
	OSStatus status = SecKeychainCopyDomainDefault(kSecPreferencesDomainUser, keychain);
	if (status != errSecSuccess) {
		return status;
	}
	const void *values[1] = { *keychain };
	*list = CFArrayCreate(kCFAllocatorDefault, values, 1, &kCFTypeArrayCallBacks);
	if (*list == NULL) {
		CFRelease(*keychain);
		*keychain = NULL;
		return errSecAllocate;
	}
	return errSecSuccess;
}

static void durin_kc_close(SecKeychainRef keychain, CFArrayRef list) {
	CFRelease(list);
	CFRelease(keychain);
}

// durin_kc_get finds the password. On success *data holds *length bytes that
// the caller frees with durin_kc_free.
static int32_t durin_kc_get(const char *service, uint32_t serviceLength,
		const char *account, uint32_t accountLength, void **data, uint32_t *length) {
	SecKeychainRef keychain;
	CFArrayRef list;
	OSStatus status = durin_kc_open(&keychain, &list);
	if (status != errSecSuccess) {
		return status;
	}
	UInt32 found = 0;
	void *password = NULL;
	SecKeychainItemRef item = NULL;
	status = SecKeychainFindGenericPassword(list, serviceLength, service,
		accountLength, account, &found, &password, &item);
	if (item != NULL) {
		CFRelease(item);
	}
	durin_kc_close(keychain, list);
	if (status == errSecSuccess) {
		*data = password;
		*length = found;
	}
	return status;
}

static void durin_kc_free(void *data) {
	SecKeychainItemFreeContent(NULL, data);
}

static int32_t durin_kc_set(const char *service, uint32_t serviceLength,
		const char *account, uint32_t accountLength, const void *data, uint32_t length) {
	SecKeychainRef keychain;
	CFArrayRef list;
	OSStatus status = durin_kc_open(&keychain, &list);
	if (status != errSecSuccess) {
		return status;
	}
	SecKeychainItemRef item = NULL;
	status = SecKeychainFindGenericPassword(list, serviceLength, service,
		accountLength, account, NULL, NULL, &item);
	if (status == errSecSuccess && item != NULL) {
		status = SecKeychainItemModifyAttributesAndData(item, NULL, length, data);
	} else {
		status = SecKeychainAddGenericPassword(keychain, serviceLength, service,
			accountLength, account, length, data, NULL);
	}
	if (item != NULL) {
		CFRelease(item);
	}
	durin_kc_close(keychain, list);
	return status;
}

static int32_t durin_kc_delete(const char *service, uint32_t serviceLength,
		const char *account, uint32_t accountLength) {
	SecKeychainRef keychain;
	CFArrayRef list;
	OSStatus status = durin_kc_open(&keychain, &list);
	if (status != errSecSuccess) {
		return status;
	}
	CFStringRef serviceString = CFStringCreateWithBytes(kCFAllocatorDefault,
		(const UInt8 *)service, serviceLength, kCFStringEncodingUTF8, false);
	CFStringRef accountString = CFStringCreateWithBytes(kCFAllocatorDefault,
		(const UInt8 *)account, accountLength, kCFStringEncodingUTF8, false);
	CFDictionaryRef query = NULL;
	if (serviceString != NULL && accountString != NULL) {
		const void *keys[4] = { kSecMatchSearchList, kSecClass, kSecAttrService, kSecAttrAccount };
		const void *values[4] = { list, kSecClassGenericPassword, serviceString, accountString };
		query = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 4,
			&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	}
	status = query != NULL ? SecItemDelete(query) : errSecAllocate;
	if (query != NULL) {
		CFRelease(query);
	}
	if (serviceString != NULL) {
		CFRelease(serviceString);
	}
	if (accountString != NULL) {
		CFRelease(accountString);
	}
	durin_kc_close(keychain, list);
	return status;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unicode/utf8"
	"unsafe"
)

var platform backend = keychain{}

const errSecItemNotFound = -25300

type keychain struct{}

// cString copies s into Go memory with a trailing NUL, so the C side always
// gets a valid pointer, even for an empty value. The caller clears it.
func cString(s string) ([]byte, *C.char, C.uint32_t) {
	buf := append([]byte(s), 0)
	return buf, (*C.char)(unsafe.Pointer(&buf[0])), C.uint32_t(len(s))
}

func keychainSpecifiers(service, key string) error {
	if service == "" || key == "" {
		return errors.New("keychain: service and key cannot be empty")
	}
	return nil
}

func (keychain) get(service, key string) (string, bool, error) {
	if err := keychainSpecifiers(service, key); err != nil {
		return "", false, err
	}
	svcBuf, svc, svcLen := cString(service)
	acctBuf, acct, acctLen := cString(key)
	var data unsafe.Pointer
	var length C.uint32_t
	status := C.durin_kc_get(svc, svcLen, acct, acctLen, &data, &length)
	clear(svcBuf)
	clear(acctBuf)
	if status == errSecItemNotFound {
		return "", false, nil
	}
	if status != 0 {
		return "", false, fmt.Errorf("keychain: find failed with OSStatus %d", int32(status))
	}
	value := C.GoBytes(data, C.int(length))
	C.durin_kc_free(data)
	defer clear(value)
	if !utf8.Valid(value) {
		return "", false, errors.New("keychain: stored secret is not UTF-8")
	}
	return string(value), true, nil
}

func (keychain) set(service, key, value string) error {
	if err := keychainSpecifiers(service, key); err != nil {
		return err
	}
	svcBuf, svc, svcLen := cString(service)
	acctBuf, acct, acctLen := cString(key)
	valueBuf, data, dataLen := cString(value)
	status := C.durin_kc_set(svc, svcLen, acct, acctLen, unsafe.Pointer(data), dataLen)
	clear(svcBuf)
	clear(acctBuf)
	clear(valueBuf)
	if status != 0 {
		return fmt.Errorf("keychain: write failed with OSStatus %d", int32(status))
	}
	return nil
}

func (keychain) remove(service, key string) error {
	if err := keychainSpecifiers(service, key); err != nil {
		return err
	}
	svcBuf, svc, svcLen := cString(service)
	acctBuf, acct, acctLen := cString(key)
	status := C.durin_kc_delete(svc, svcLen, acct, acctLen)
	clear(svcBuf)
	clear(acctBuf)
	if status == errSecItemNotFound {
		return nil
	}
	if status != 0 {
		return fmt.Errorf("keychain: delete failed with OSStatus %d", int32(status))
	}
	return nil
}
