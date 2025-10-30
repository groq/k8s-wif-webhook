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

package wif

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExtractWIFConfig(t *testing.T) {
	t.Run("returns impersonation config when annotation is present", func(t *testing.T) {
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "test@project.iam.gserviceaccount.com",
				},
			},
		}

		config := ExtractWIFConfig(sa)

		assert.False(t, config.UseDirectIdentity)
		assert.Equal(t, "test@project.iam.gserviceaccount.com", config.GoogleServiceAccount)
		assert.Equal(t, "wif-credentials-impersonation-test-sa", config.CredentialsConfigMap)
	})

	t.Run("returns direct identity config when annotation is absent", func(t *testing.T) {
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}

		config := ExtractWIFConfig(sa)

		assert.True(t, config.UseDirectIdentity)
		assert.Empty(t, config.GoogleServiceAccount)
		assert.Equal(t, "wif-credentials-direct", config.CredentialsConfigMap)
	})

	t.Run("returns direct identity config when annotations map is nil", func(t *testing.T) {
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test-sa",
				Namespace:   "default",
				Annotations: nil,
			},
		}

		config := ExtractWIFConfig(sa)

		assert.True(t, config.UseDirectIdentity)
		assert.Equal(t, "wif-credentials-direct", config.CredentialsConfigMap)
	})

	t.Run("uses ServiceAccount name in ConfigMap name for impersonation", func(t *testing.T) {
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-service-account",
				Namespace: "production",
				Annotations: map[string]string{
					"iam.gke.io/gcp-service-account": "prod@project.iam.gserviceaccount.com",
				},
			},
		}

		config := ExtractWIFConfig(sa)

		assert.Equal(t, "wif-credentials-impersonation-my-service-account", config.CredentialsConfigMap)
	})
}

func TestGetWorkloadIdentityProvider(t *testing.T) {
	t.Run("returns environment variable when set", func(t *testing.T) {
		expected := "projects/123/locations/global/workloadIdentityPools/pool/providers/provider"
		os.Setenv("WORKLOAD_IDENTITY_PROVIDER", expected)
		defer os.Unsetenv("WORKLOAD_IDENTITY_PROVIDER")

		result := GetWorkloadIdentityProvider()

		assert.Equal(t, expected, result)
	})

	t.Run("returns template placeholder when environment variable is not set", func(t *testing.T) {
		os.Unsetenv("WORKLOAD_IDENTITY_PROVIDER")

		result := GetWorkloadIdentityProvider()

		assert.Equal(t, "${WORKLOAD_IDENTITY_PROVIDER}", result)
	})
}

func TestGenerateCredentialsConfig(t *testing.T) {
	providerPath := "projects/123456789/locations/global/workloadIdentityPools/test-pool/providers/test-provider"
	os.Setenv("WORKLOAD_IDENTITY_PROVIDER", providerPath)
	defer os.Unsetenv("WORKLOAD_IDENTITY_PROVIDER")

	t.Run("generates direct identity configuration", func(t *testing.T) {
		config := &WIFConfig{
			UseDirectIdentity:    true,
			CredentialsConfigMap: "wif-credentials-direct",
		}

		result := GenerateCredentialsConfig(config)

		// Parse JSON to verify structure
		var credConfig map[string]interface{}
		err := json.Unmarshal([]byte(result), &credConfig)
		require.NoError(t, err)

		assert.Equal(t, "external_account", credConfig["type"])
		assert.Equal(t, "//iam.googleapis.com/"+providerPath, credConfig["audience"])
		assert.Equal(t, "urn:ietf:params:oauth:token-type:jwt", credConfig["subject_token_type"])
		assert.Equal(t, "https://sts.googleapis.com/v1/token", credConfig["token_url"])

		credSource, ok := credConfig["credential_source"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "/var/run/service-account/token", credSource["file"])

		// Should not have impersonation URL
		_, hasImpersonation := credConfig["service_account_impersonation_url"]
		assert.False(t, hasImpersonation)
	})

	t.Run("generates impersonation configuration", func(t *testing.T) {
		config := &WIFConfig{
			UseDirectIdentity:    false,
			GoogleServiceAccount: "test@project.iam.gserviceaccount.com",
			CredentialsConfigMap: "wif-credentials-impersonation-test",
		}

		result := GenerateCredentialsConfig(config)

		// Parse JSON to verify structure
		var credConfig map[string]interface{}
		err := json.Unmarshal([]byte(result), &credConfig)
		require.NoError(t, err)

		assert.Equal(t, "external_account", credConfig["type"])
		assert.Equal(t, "googleapis.com", credConfig["universe_domain"])
		assert.Equal(t, "//iam.googleapis.com/"+providerPath, credConfig["audience"])
		assert.Equal(t, "urn:ietf:params:oauth:token-type:jwt", credConfig["subject_token_type"])
		assert.Equal(t, "https://sts.googleapis.com/v1/token", credConfig["token_url"])

		credSource, ok := credConfig["credential_source"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "/var/run/service-account/token", credSource["file"])

		format, ok := credSource["format"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "text", format["type"])

		// Should have impersonation URL
		impersonationURL, ok := credConfig["service_account_impersonation_url"].(string)
		require.True(t, ok)
		assert.Contains(t, impersonationURL, "test@project.iam.gserviceaccount.com")
		assert.Contains(t, impersonationURL, "generateAccessToken")
	})

	t.Run("generates valid JSON for direct identity", func(t *testing.T) {
		config := &WIFConfig{
			UseDirectIdentity:    true,
			CredentialsConfigMap: "wif-credentials-direct",
		}

		result := GenerateCredentialsConfig(config)

		// Verify it's valid JSON
		var jsonData map[string]interface{}
		err := json.Unmarshal([]byte(result), &jsonData)
		assert.NoError(t, err)
	})

	t.Run("generates valid JSON for impersonation", func(t *testing.T) {
		config := &WIFConfig{
			UseDirectIdentity:    false,
			GoogleServiceAccount: "service@project.iam.gserviceaccount.com",
			CredentialsConfigMap: "wif-credentials-impersonation-test",
		}

		result := GenerateCredentialsConfig(config)

		// Verify it's valid JSON
		var jsonData map[string]interface{}
		err := json.Unmarshal([]byte(result), &jsonData)
		assert.NoError(t, err)
	})
}

func TestGetConfigMapType(t *testing.T) {
	t.Run("returns 'direct' for direct identity", func(t *testing.T) {
		config := &WIFConfig{
			UseDirectIdentity: true,
		}

		result := GetConfigMapType(config)

		assert.Equal(t, "direct", result)
	})

	t.Run("returns 'impersonation' for impersonation mode", func(t *testing.T) {
		config := &WIFConfig{
			UseDirectIdentity: false,
		}

		result := GetConfigMapType(config)

		assert.Equal(t, "impersonation", result)
	})
}
