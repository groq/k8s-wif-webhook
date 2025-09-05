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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromEnvironment(t *testing.T) {
	tests := []struct {
		name         string
		envVars      map[string]string
		expectedMode Mode
	}{
		{
			name:         "default production mode",
			envVars:      map[string]string{},
			expectedMode: ProductionMode,
		},
		{
			name: "explicit production mode",
			envVars: map[string]string{
				"WEBHOOK_MODE": "production",
			},
			expectedMode: ProductionMode,
		},
		{
			name: "development mode",
			envVars: map[string]string{
				"WEBHOOK_MODE": "development",
			},
			expectedMode: DevelopmentMode,
		},
		{
			name: "production mode with WIF config",
			envVars: map[string]string{
				"WORKLOAD_IDENTITY_PROVIDER": "projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
				"GOOGLE_CLOUD_PROJECT":       "test-project",
			},
			expectedMode: ProductionMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment
			clearEnv()

			// Set test environment variables
			for key, value := range tt.envVars {
				os.Setenv(key, value)
			}
			defer clearEnv()

			config := LoadFromEnvironment()

			assert.Equal(t, tt.expectedMode, config.Mode)
			assert.Equal(t, tt.envVars["WORKLOAD_IDENTITY_PROVIDER"], config.WorkloadIdentityProvider)
			assert.Equal(t, tt.envVars["GOOGLE_CLOUD_PROJECT"], config.GoogleCloudProject)

			// Dev credentials path should always be set to default
			assert.NotEmpty(t, config.DevCredentialsPath)
		})
	}
}

func TestConfigValidate(t *testing.T) {
	// Create a temporary credentials file for development mode tests
	tempDir := t.TempDir()
	credentialsFile := filepath.Join(tempDir, "application_default_credentials.json")
	err := os.WriteFile(credentialsFile, []byte(`{"type": "authorized_user"}`), 0644)
	require.NoError(t, err)

	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid production config",
			config: &Config{
				Mode:                     ProductionMode,
				WorkloadIdentityProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
				GoogleCloudProject:       "test-project",
			},
			wantErr: false,
		},
		{
			name: "production config missing WIF provider",
			config: &Config{
				Mode:               ProductionMode,
				GoogleCloudProject: "test-project",
			},
			wantErr: true,
			errMsg:  "WORKLOAD_IDENTITY_PROVIDER is required in production mode",
		},
		{
			name: "production config missing GCP project",
			config: &Config{
				Mode:                     ProductionMode,
				WorkloadIdentityProvider: "projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
			},
			wantErr: true,
			errMsg:  "GOOGLE_CLOUD_PROJECT is required in production mode",
		},
		{
			name: "valid development config",
			config: &Config{
				Mode:               DevelopmentMode,
				DevCredentialsPath: credentialsFile,
			},
			wantErr: false,
		},
		{
			name: "development config missing credentials path",
			config: &Config{
				Mode:               DevelopmentMode,
				DevCredentialsPath: "",
			},
			wantErr: true,
			errMsg:  "DEV_CREDENTIALS_PATH is required in development mode",
		},
		{
			name: "development config with non-existent credentials file",
			config: &Config{
				Mode:               DevelopmentMode,
				DevCredentialsPath: "/path/that/does/not/exist",
			},
			wantErr: true,
			errMsg:  "development credentials file does not exist",
		},
		{
			name: "invalid mode",
			config: &Config{
				Mode: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid webhook mode: invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigModeCheckers(t *testing.T) {
	prodConfig := &Config{Mode: ProductionMode}
	devConfig := &Config{Mode: DevelopmentMode}

	assert.True(t, prodConfig.IsProductionMode())
	assert.False(t, prodConfig.IsDevelopmentMode())

	assert.False(t, devConfig.IsProductionMode())
	assert.True(t, devConfig.IsDevelopmentMode())
}

func TestDefaultDevCredentialsPath(t *testing.T) {
	path := DefaultDevCredentialsPath()
	assert.Contains(t, path, ".config/gcloud/application_default_credentials.json")
	assert.NotEmpty(t, path)
}

// clearEnv clears all environment variables used by the webhook
func clearEnv() {
	envVars := []string{
		"WEBHOOK_MODE",
		"WORKLOAD_IDENTITY_PROVIDER",
		"GOOGLE_CLOUD_PROJECT",
		"DEV_CREDENTIALS_PATH",
	}
	for _, env := range envVars {
		os.Unsetenv(env)
	}
}
