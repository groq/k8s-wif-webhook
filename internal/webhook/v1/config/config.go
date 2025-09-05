/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"os"
	"path/filepath"
)

// DefaultDevCredentialsPath returns the default path for gcloud credentials
func DefaultDevCredentialsPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "gcloud", "application_default_credentials.json")
}

// LoadFromEnvironment creates a Config from environment variables
// Following Single Responsibility Principle
func LoadFromEnvironment() *Config {
	config := &Config{
		Mode:                     ProductionMode, // Default to production
		WorkloadIdentityProvider: os.Getenv("WORKLOAD_IDENTITY_PROVIDER"),
		GoogleCloudProject:       os.Getenv("GOOGLE_CLOUD_PROJECT"),
		DevCredentialsPath:       DefaultDevCredentialsPath(),
	}

	// Check for development mode
	if mode := os.Getenv("WEBHOOK_MODE"); mode == "development" {
		config.Mode = DevelopmentMode
	}

	// Override dev credentials path if specified
	if devPath := os.Getenv("DEV_CREDENTIALS_PATH"); devPath != "" {
		config.DevCredentialsPath = devPath
	}

	return config
}

// IsProductionMode returns true if the webhook is running in production mode
func (c *Config) IsProductionMode() bool {
	return c.Mode == ProductionMode
}

// IsDevelopmentMode returns true if the webhook is running in development mode
func (c *Config) IsDevelopmentMode() bool {
	return c.Mode == DevelopmentMode
}

// Validate checks if the configuration is valid for the current mode
func (c *Config) Validate() error {
	switch c.Mode {
	case ProductionMode:
		if c.WorkloadIdentityProvider == "" {
			return NewConfigError("WORKLOAD_IDENTITY_PROVIDER is required in production mode")
		}
		if c.GoogleCloudProject == "" {
			return NewConfigError("GOOGLE_CLOUD_PROJECT is required in production mode")
		}
	case DevelopmentMode:
		if c.DevCredentialsPath == "" {
			return NewConfigError("DEV_CREDENTIALS_PATH is required in development mode")
		}
		// Check if credentials file exists
		if _, err := os.Stat(c.DevCredentialsPath); os.IsNotExist(err) {
			return NewConfigError("development credentials file does not exist: " + c.DevCredentialsPath)
		}
	default:
		return NewConfigError("invalid webhook mode: " + string(c.Mode))
	}
	return nil
}

// ConfigError represents configuration-related errors
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return "webhook configuration error: " + e.Message
}

// NewConfigError creates a new ConfigError
func NewConfigError(message string) *ConfigError {
	return &ConfigError{Message: message}
}
