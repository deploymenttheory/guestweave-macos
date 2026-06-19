// Port of tart's KeychainCredentialsProvider, written against the idiomatic
// Security + Foundation layer: the kSec* constants come back as objc.ID values,
// queries are built with the idiomatic mutable-dictionary builder, and the
// SecItem* calls return Go errors — no raw CFDictionary plumbing.
//go:build darwin

package credentials

import (
	"errors"
	"strconv"
	"unsafe"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	security "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/security"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

const errSecItemNotFound = -25300

const keychainCredentialsLabel = "Weave Credentials"

// KeychainCredentialsProvider ports the class of the same name.
type KeychainCredentialsProvider struct{}

var _ CredentialsProvider = (*KeychainCredentialsProvider)(nil)

func (p *KeychainCredentialsProvider) UserFriendlyName() string {
	return "Keychain credentials provider"
}

func (p *KeychainCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	query := foundation.NewMutableDictionary().
		Set(security.KSecClass(), security.KSecClassInternetPassword()).
		Set(security.KSecAttrProtocol(), security.KSecAttrProtocolHTTPS()).
		Set(security.KSecAttrServer(), purego.NSString(host)).
		Set(security.KSecMatchLimit(), security.KSecMatchLimitOne()).
		Set(security.KSecReturnAttributes(), boolValue(true)).
		Set(security.KSecReturnData(), boolValue(true)).
		Set(security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel))

	itemID, err := security.SecItemCopyMatching(query.ID())
	if err != nil {
		if isNotFound(err) {
			return "", "", false, nil
		}
		return "", "", false, credentialsProviderFailed("Keychain lookup failed: %s", secMessage(err))
	}

	item := foundation.DictionaryFromID(itemID)
	userID := item.ObjectForKey(security.KSecAttrAccount())
	dataID := item.ObjectForKey(security.KSecValueData())
	if userID == 0 || dataID == 0 {
		return "", "", false, credentialsProviderFailed("Keychain item has unexpected format")
	}

	data := foundation.DataFromID(purego.Retain(dataID))
	password := string(unsafe.Slice((*byte)(data.Bytes()), data.Length()))
	return purego.GoString(userID), password, true, nil
}

func (p *KeychainCredentialsProvider) Store(host string, user string, password string) error {
	key := foundation.NewMutableDictionary().
		Set(security.KSecClass(), security.KSecClassInternetPassword()).
		Set(security.KSecAttrProtocol(), security.KSecAttrProtocolHTTPS()).
		Set(security.KSecAttrServer(), purego.NSString(host)).
		Set(security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel))

	value := foundation.NewMutableDictionary().
		Set(security.KSecAttrAccount(), purego.NSString(user)).
		Set(security.KSecValueData(), dataID([]byte(password)))

	switch _, err := security.SecItemCopyMatching(key.ID()); {
	case err == nil:
		if err := security.SecItemUpdate(key.ID(), value.ID()); err != nil {
			return credentialsProviderFailed("Keychain failed to update item: %s", secMessage(err))
		}
	case isNotFound(err):
		add := foundation.NewMutableDictionary().
			Set(security.KSecClass(), security.KSecClassInternetPassword()).
			Set(security.KSecAttrProtocol(), security.KSecAttrProtocolHTTPS()).
			Set(security.KSecAttrServer(), purego.NSString(host)).
			Set(security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel)).
			Set(security.KSecAttrAccount(), purego.NSString(user)).
			Set(security.KSecValueData(), dataID([]byte(password)))
		if _, err := security.SecItemAdd(add.ID()); err != nil {
			return credentialsProviderFailed("Keychain failed to add item: %s", secMessage(err))
		}
	default:
		return credentialsProviderFailed("Keychain failed to find item: %s", secMessage(err))
	}
	return nil
}

// Remove ports KeychainCredentialsProvider.remove(host:).
func (p *KeychainCredentialsProvider) Remove(host string) error {
	query := foundation.NewMutableDictionary().
		Set(security.KSecClass(), security.KSecClassInternetPassword()).
		Set(security.KSecAttrServer(), purego.NSString(host)).
		Set(security.KSecAttrLabel(), purego.NSString(keychainCredentialsLabel))

	switch err := security.SecItemDelete(query.ID()); {
	case err == nil, isNotFound(err):
		return nil
	default:
		return credentialsProviderFailed("Failed to remove Keychain item(s): %s", secMessage(err))
	}
}

// boolValue boxes a Go bool as an NSNumber id for the kSecReturn* flags.
func boolValue(b bool) purego.ID { return foundation.NewNumberWithBool(b).ID() }

// dataID boxes secret bytes as an NSData id for kSecValueData.
func dataID(b []byte) purego.ID {
	if len(b) == 0 {
		return foundation.NewDataWithBytesLength(nil, 0).ID()
	}
	return foundation.NewDataWithBytesLength(unsafe.Pointer(&b[0]), uint(len(b))).ID()
}

// isNotFound reports whether err is the errSecItemNotFound OSStatus.
func isNotFound(err error) bool {
	var oserr *purego.OSStatusError
	return errors.As(err, &oserr) && oserr.Status.Int() == errSecItemNotFound
}

// secMessage renders the Security framework's message for an OSStatus error.
func secMessage(err error) string {
	var oserr *purego.OSStatusError
	if !errors.As(err, &oserr) {
		return err.Error()
	}
	if msg := purego.CFString(security.SecCopyErrorMessageString(oserr.Status.Int(), nil)); msg != "" {
		return msg
	}
	return "status " + strconv.Itoa(oserr.Status.Int())
}
