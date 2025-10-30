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
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
)

// WIFConfig holds workload identity federation configuration
type WIFConfig struct {
	UseDirectIdentity    bool
	GoogleServiceAccount string
	CredentialsConfigMap string
}

// ExtractWIFConfig extracts WIF configuration from a ServiceAccount
func ExtractWIFConfig(sa *corev1.ServiceAccount) *WIFConfig {
	// Check for GKE-style service account impersonation annotation (takes precedence)
	if sa.Annotations != nil {
		if gcpSA, ok := sa.Annotations["iam.gke.io/gcp-service-account"]; ok {
			// Use k8s ServiceAccount name for ConfigMap to enable owner references
			configMapName := fmt.Sprintf("wif-credentials-impersonation-%s", sa.Name)
			return &WIFConfig{
				UseDirectIdentity:    false,
				GoogleServiceAccount: gcpSA,
				CredentialsConfigMap: configMapName,
			}
		}
	}

	// Default: Direct federated identity (opt-out behavior)
	// Users can disable via namespace or pod labels
	return &WIFConfig{
		UseDirectIdentity:    true,
		CredentialsConfigMap: "wif-credentials-direct",
	}
}

// GetWorkloadIdentityProvider returns the full workload identity provider path from environment
func GetWorkloadIdentityProvider() string {
	if provider := os.Getenv("WORKLOAD_IDENTITY_PROVIDER"); provider != "" {
		return provider
	}
	return "${WORKLOAD_IDENTITY_PROVIDER}" // Fallback to template processing
}

// GenerateCredentialsConfig generates the credentials JSON configuration for WIF
func GenerateCredentialsConfig(config *WIFConfig) string {
	// Get the full provider path and format as audience
	providerPath := GetWorkloadIdentityProvider()
	audience := "//iam.googleapis.com/" + providerPath

	if config.UseDirectIdentity {
		// Direct federated identity configuration
		credConfig := map[string]interface{}{
			"type":               "external_account",
			"audience":           audience,
			"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			"token_url":          "https://sts.googleapis.com/v1/token",
			"credential_source": map[string]interface{}{
				"file": "/var/run/service-account/token",
			},
		}
		data, _ := json.MarshalIndent(credConfig, "", "  ")
		return string(data)
	} else {
		// Service account impersonation configuration
		credConfig := map[string]interface{}{
			"universe_domain":    "googleapis.com",
			"type":               "external_account",
			"audience":           audience,
			"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			"token_url":          "https://sts.googleapis.com/v1/token",
			"credential_source": map[string]interface{}{
				"file": "/var/run/service-account/token",
				"format": map[string]interface{}{
					"type": "text",
				},
			},
			"service_account_impersonation_url": fmt.Sprintf("https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:generateAccessToken", config.GoogleServiceAccount),
		}
		data, _ := json.MarshalIndent(credConfig, "", "  ")
		return string(data)
	}
}

// GetConfigMapType returns the type label for the ConfigMap
func GetConfigMapType(config *WIFConfig) string {
	if config.UseDirectIdentity {
		return "direct"
	}
	return "impersonation"
}
