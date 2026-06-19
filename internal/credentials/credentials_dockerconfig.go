// Port of tart's Credentials/DockerConfigCredentialsProvider.swift: reads
// ~/.docker/config.json and falls back to docker-credential-* helpers.
//go:build darwin

package credentials

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DockerConfig ports the DockerConfig Codable struct.
type DockerConfig struct {
	Auths       map[string]DockerAuthConfig `json:"auths"`
	CredHelpers map[string]string           `json:"credHelpers"`
}

// FindCredHelper ports DockerConfig.findCredHelper(host:). Tart supports
// wildcards in credHelpers, similar to docker/cli#2928.
func (c *DockerConfig) FindCredHelper(host string) string {
	for hostPattern, helperProgram := range c.CredHelpers {
		if hostPattern == host {
			return helperProgram
		}
		compiledPattern, err := regexp.Compile(hostPattern)
		if err == nil && compiledPattern.FindString(host) == host {
			return helperProgram
		}
	}
	return ""
}

// DockerAuthConfig ports the DockerAuthConfig Codable struct.
type DockerAuthConfig struct {
	Auth string `json:"auth"`
}

// DecodeCredentials ports DockerAuthConfig.decodeCredentials(): auth is
// base64("username:password").
func (c DockerAuthConfig) DecodeCredentials() (string, string, bool) {
	if c.Auth == "" {
		return "", "", false
	}
	data, err := base64.StdEncoding.DecodeString(c.Auth)
	if err != nil {
		return "", "", false
	}
	components := strings.Split(string(data), ":")
	if len(components) != 2 {
		return "", "", false
	}
	return components[0], components[1], true
}

type dockerGetOutput struct {
	Username string `json:"Username"`
	Secret   string `json:"Secret"`
}

// DockerConfigCredentialsProvider ports the class of the same name.
type DockerConfigCredentialsProvider struct{}

var _ CredentialsProvider = (*DockerConfigCredentialsProvider)(nil)

func (p *DockerConfigCredentialsProvider) UserFriendlyName() string {
	return "Docker configuration credentials provider"
}

func (p *DockerConfigCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false, err
	}
	dockerConfigPath := filepath.Join(home, ".docker", "config.json")

	configData, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	var config DockerConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return "", "", false, err
	}

	if auth, ok := config.Auths[host]; ok {
		if user, password, ok := auth.DecodeCredentials(); ok {
			return user, password, true, nil
		}
	}
	if helperProgram := config.FindCredHelper(host); helperProgram != "" {
		return p.executeHelper("docker-credential-"+helperProgram, host)
	}

	return "", "", false, nil
}

func (p *DockerConfigCredentialsProvider) executeHelper(binaryName string, host string) (string, string, bool, error) {
	if _, err := exec.LookPath(binaryName); err != nil {
		return "", "", false, credentialsProviderFailed("%s not found in PATH", binaryName)
	}

	cmd := exec.Command(binaryName, "get")
	cmd.Stdin = strings.NewReader(host + "\n")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	runErr := cmd.Run()
	outputData := output.Bytes()

	if runErr != nil {
		if len(outputData) > 0 {
			fmt.Println(string(outputData))
		}
		return "", "", false, credentialsProviderFailed("Docker helper failed!")
	}
	if len(outputData) == 0 {
		return "", "", false, credentialsProviderFailed("Docker helper output is empty!")
	}

	var out dockerGetOutput
	if err := json.Unmarshal(outputData, &out); err != nil {
		return "", "", false, err
	}
	return out.Username, out.Secret, true, nil
}

func (p *DockerConfigCredentialsProvider) Store(host string, user string, password string) error {
	return credentialsProviderFailed("Docker helpers don't support storing!")
}
