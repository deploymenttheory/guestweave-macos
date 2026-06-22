// Port of tart's KeychainCredentialsProvider, written against the idiomatic
// Security + Foundation layer. Queries are built with the mutable-dictionary
// builder using typed Foundation values; the SecItem* calls take and return
// idiomatic objects and report failures as Go errors. No raw CFDictionary
// plumbing, objc.ID values, or unsafe pointers are needed.
//go:build darwin

package credentials

import (
	"errors"
	"strconv"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	foundation "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/foundation"
	security "github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/framework/security"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/idiomatic/rt"
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
		Set(security.KSecAttrServer(), foundation.NewStringWithUTF8String(host)).
		Set(security.KSecMatchLimit(), security.KSecMatchLimitOne()).
		Set(security.KSecReturnAttributes(), foundation.NewNumberWithBool(true)).
		Set(security.KSecReturnData(), foundation.NewNumberWithBool(true)).
		Set(security.KSecAttrLabel(), foundation.NewStringWithUTF8String(keychainCredentialsLabel))

	itemObj, err := security.SecItemCopyMatching(query)
	if err != nil {
		if isNotFound(err) {
			return "", "", false, nil
		}
		return "", "", false, credentialsProviderFailed("Keychain lookup failed: %s", secMessage(err))
	}

	item, ok := obj.As(itemObj, "NSDictionary", foundation.DictionaryFromID)
	if !ok {
		return "", "", false, credentialsProviderFailed("Keychain item has unexpected format")
	}
	user := item.ObjectForKey(security.KSecAttrAccount())
	data := item.ObjectForKey(security.KSecValueData())
	if user == nil || data == nil {
		return "", "", false, credentialsProviderFailed("Keychain item has unexpected format")
	}
	return user.Description(), string(obj.Bytes(data)), true, nil
}

func (p *KeychainCredentialsProvider) Store(host string, user string, password string) error {
	key := foundation.NewMutableDictionary().
		Set(security.KSecClass(), security.KSecClassInternetPassword()).
		Set(security.KSecAttrProtocol(), security.KSecAttrProtocolHTTPS()).
		Set(security.KSecAttrServer(), foundation.NewStringWithUTF8String(host)).
		Set(security.KSecAttrLabel(), foundation.NewStringWithUTF8String(keychainCredentialsLabel))

	value := foundation.NewMutableDictionary().
		Set(security.KSecAttrAccount(), foundation.NewStringWithUTF8String(user)).
		Set(security.KSecValueData(), dataValue(password))

	switch _, err := security.SecItemCopyMatching(key); {
	case err == nil:
		if err := security.SecItemUpdate(key, value); err != nil {
			return credentialsProviderFailed("Keychain failed to update item: %s", secMessage(err))
		}
	case isNotFound(err):
		add := foundation.NewMutableDictionary().
			Set(security.KSecClass(), security.KSecClassInternetPassword()).
			Set(security.KSecAttrProtocol(), security.KSecAttrProtocolHTTPS()).
			Set(security.KSecAttrServer(), foundation.NewStringWithUTF8String(host)).
			Set(security.KSecAttrLabel(), foundation.NewStringWithUTF8String(keychainCredentialsLabel)).
			Set(security.KSecAttrAccount(), foundation.NewStringWithUTF8String(user)).
			Set(security.KSecValueData(), dataValue(password))
		if _, err := security.SecItemAdd(add); err != nil {
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
		Set(security.KSecAttrServer(), foundation.NewStringWithUTF8String(host)).
		Set(security.KSecAttrLabel(), foundation.NewStringWithUTF8String(keychainCredentialsLabel))

	switch err := security.SecItemDelete(query); {
	case err == nil, isNotFound(err):
		return nil
	default:
		return credentialsProviderFailed("Failed to remove Keychain item(s): %s", secMessage(err))
	}
}

// dataValue boxes secret bytes as an idiomatic NSData for kSecValueData.
func dataValue(password string) *foundation.Data {
	return foundation.DataFromID(rt.BytesToNSData([]byte(password)))
}

// isNotFound reports whether err is the errSecItemNotFound OSStatus.
func isNotFound(err error) bool {
	var oserr *purego.OSStatusError
	return errors.As(err, &oserr) && oserr.Status.Int() == errSecItemNotFound
}

// secMessage renders a human-readable message for an OSStatus error.
func secMessage(err error) string {
	var oserr *purego.OSStatusError
	if !errors.As(err, &oserr) {
		return err.Error()
	}
	return "status " + strconv.Itoa(oserr.Status.Int())
}
