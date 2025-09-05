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
	corev1 "k8s.io/api/core/v1"
)

// Mode represents the webhook operation mode
type Mode string

const (
	// ProductionMode uses Workload Identity Federation
	ProductionMode Mode = "production"
	// DevelopmentMode uses local gcloud credentials
	DevelopmentMode Mode = "development"
)

// Config holds the webhook configuration
type Config struct {
	Mode                     Mode   `json:"mode"`
	WorkloadIdentityProvider string `json:"workloadIdentityProvider,omitempty"`
	GoogleCloudProject       string `json:"googleCloudProject,omitempty"`
	DevCredentialsPath       string `json:"devCredentialsPath,omitempty"`
}

// CredentialsProvider defines the interface for credential injection strategies
// Following Open/Closed Principle - open for extension, closed for modification
type CredentialsProvider interface {
	// InjectCredentials modifies the pod spec to include appropriate credentials
	InjectCredentials(pod *corev1.Pod, namespace string) error

	// GetCredentialType returns a string identifying the credential type for logging
	GetCredentialType() string

	// RequiresSetup indicates if this provider needs initialization/setup
	RequiresSetup() bool

	// Setup performs any necessary initialization for the provider
	Setup() error
}

// CredentialsConfig holds configuration specific to credential injection
type CredentialsConfig struct {
	UseDirectIdentity    bool
	GoogleServiceAccount string
	CredentialsConfigMap string
	CredentialsSecret    string
	MountPath            string
}

// PodInjector defines the interface for injecting configurations into pods
// Following Interface Segregation Principle
type PodInjector interface {
	InjectVolumes(pod *corev1.Pod) error
	InjectEnvironmentVars(pod *corev1.Pod, targetContainers []string) error
}

// VolumeManager defines the interface for managing Kubernetes volumes and mounts
type VolumeManager interface {
	AddVolume(pod *corev1.Pod, volume corev1.Volume) error
	VolumeExists(pod *corev1.Pod, volumeName string) bool
	AddVolumeMount(container *corev1.Container, volumeMount corev1.VolumeMount) error
	VolumeMountExists(container *corev1.Container, volumeName string) bool
	MountPathExists(container *corev1.Container, mountPath string) bool
}
