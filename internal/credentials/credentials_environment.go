// Port of tart's Credentials/EnvironmentCredentialsProvider.swift.
//go:build darwin

package credentials

import "github.com/deploymenttheory/guestweave/internal/objcutil"

// EnvironmentCredentialsProvider reads GUESTWEAVE_REGISTRY_* environment
// variables (env-only by design: never file-settable, never persisted).
type EnvironmentCredentialsProvider struct{}

var _ CredentialsProvider = (*EnvironmentCredentialsProvider)(nil)

func (p *EnvironmentCredentialsProvider) UserFriendlyName() string {
	return "environment variable credentials provider"
}

func (p *EnvironmentCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	if registryHostname, ok := objcutil.EnvironmentValue("GUESTWEAVE_REGISTRY_HOSTNAME"); ok && registryHostname != host {
		return "", "", false, nil
	}

	username, hasUsername := objcutil.EnvironmentValue("GUESTWEAVE_REGISTRY_USERNAME")
	password, hasPassword := objcutil.EnvironmentValue("GUESTWEAVE_REGISTRY_PASSWORD")
	if hasUsername && hasPassword {
		return username, password, true, nil
	}
	return "", "", false, nil
}

func (p *EnvironmentCredentialsProvider) Store(host string, user string, password string) error {
	return nil
}
