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

package volume

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/groq/k8s-wif-webhook/internal/webhook/v1/config"
)

// DefaultVolumeManager provides default implementation of VolumeManager interface
// Following Single Responsibility Principle - only handles volume operations
type DefaultVolumeManager struct{}

// NewDefaultVolumeManager creates a new DefaultVolumeManager
func NewDefaultVolumeManager() config.VolumeManager {
	return &DefaultVolumeManager{}
}

// AddVolume implements VolumeManager interface
func (vm *DefaultVolumeManager) AddVolume(pod *corev1.Pod, volume corev1.Volume) error {
	pod.Spec.Volumes = append(pod.Spec.Volumes, volume)
	return nil
}

// VolumeExists implements VolumeManager interface
func (vm *DefaultVolumeManager) VolumeExists(pod *corev1.Pod, volumeName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == volumeName {
			return true
		}
	}
	return false
}

// AddVolumeMount implements VolumeManager interface
func (vm *DefaultVolumeManager) AddVolumeMount(container *corev1.Container, volumeMount corev1.VolumeMount) error {
	container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	return nil
}

// VolumeMountExists implements VolumeManager interface
func (vm *DefaultVolumeManager) VolumeMountExists(container *corev1.Container, volumeName string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == volumeName {
			return true
		}
	}
	return false
}

// MountPathExists implements VolumeManager interface
func (vm *DefaultVolumeManager) MountPathExists(container *corev1.Container, mountPath string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.MountPath == mountPath {
			return true
		}
	}
	return false
}
