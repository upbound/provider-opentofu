/*
SPDX-FileCopyrightText: 2025 Upbound Inc. <https://upbound.io>

SPDX-License-Identifier: Apache-2.0
*/

package v1beta1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	extensionsV1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// A Var represents a tofu configuration variable.
type Var struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// A VarFileSource specifies the source of a Terraform vars file.
// +kubebuilder:validation:Enum=ConfigMapKey;SecretKey
type VarFileSource string

// Vars file sources.
const (
	VarFileSourceConfigMapKey VarFileSource = "ConfigMapKey"
	VarFileSourceSecretKey    VarFileSource = "SecretKey"
)

// A FileFormat specifies the format of a Terraform file.
// +kubebuilder:validation:Enum=HCL;JSON
type FileFormat string

// Vars file formats.
var (
	FileFormatHCL  FileFormat = "HCL"
	FileFormatJSON FileFormat = "JSON"
)

// A VarFile is a file containing many Terraform variables.
type VarFile struct {
	// Source of this vars file.
	Source VarFileSource `json:"source"`

	// Format of this vars file.
	// +kubebuilder:default=HCL
	// +optional
	Format *FileFormat `json:"format,omitempty"`

	// A ConfigMap key containing the vars file.
	// +optional
	ConfigMapKeyReference *KeyReference `json:"configMapKeyRef,omitempty"`

	// A Secret key containing the vars file.
	// +optional
	SecretKeyReference *KeyReference `json:"secretKeyRef,omitempty"`
}

// An EnvVar specifies an environment variable to be set for the workspace.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`

	// A ConfigMap key containing the desired env var value.
	ConfigMapKeyReference *KeyReference `json:"configMapKeyRef,omitempty"`

	// A Secret key containing the desired env var value.
	SecretKeyReference *KeyReference `json:"secretKeyRef,omitempty"`
}

// A KeyReference references a key within a Secret or a ConfigMap.
type KeyReference struct {
	// Namespace of the referenced resource.
	Namespace string `json:"namespace"`

	// Name of the referenced resource.
	Name string `json:"name"`

	// Key within the referenced resource.
	Key string `json:"key"`
}

// A ModuleSource represents the source of a Terraform module.
// +kubebuilder:validation:Enum=Remote;Inline
type ModuleSource string

// Module sources.
const (
	ModuleSourceRemote ModuleSource = "Remote"
	ModuleSourceInline ModuleSource = "Inline"
)

// WorkspaceParameters are the configurable fields of a Workspace.
type WorkspaceParameters struct {
	// The root module of this workspace; i.e. the module containing its main.tf
	// file. When the workspace's source is 'Remote' (the default) this can be
	// any address supported by tofu init -from-module, for example a git
	// repository or an S3 bucket. When the workspace's source is 'Inline' the
	// content of a simple main.tf or main.tf.json file may be written inline.
	Module string `json:"module"`

	// Specifies the format of the inline Terraform content
	// if Source is 'Inline'
	InlineFormat FileFormat `json:"inlineFormat,omitempty"`

	// Source of the root module of this workspace.
	Source ModuleSource `json:"source"`

	// Entrypoint for `tofu init` within the module
	// +kubebuilder:default=""
	// +optional
	Entrypoint string `json:"entrypoint"`

	// Environment variables.
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Configuration variables.
	// +optional
	Vars []Var `json:"vars,omitempty"`

	// Terraform Variable Map. Should be a valid JSON representation of the input vars
	// +optional
	VarMap *runtime.RawExtension `json:"varmap,omitempty"`

	// Files of configuration variables. Explicitly declared vars take
	// precedence.
	// +optional
	VarFiles []VarFile `json:"varFiles,omitempty"`

	// Arguments to be included in the tofu init CLI command
	InitArgs []string `json:"initArgs,omitempty"`

	// Arguments to be included in the tofu plan CLI command
	PlanArgs []string `json:"planArgs,omitempty"`

	// Arguments to be included in the tofu apply CLI command
	ApplyArgs []string `json:"applyArgs,omitempty"`

	// Arguments to be included in the tofu destroy CLI command
	DestroyArgs []string `json:"destroyArgs,omitempty"`

	// Boolean value to indicate CLI logging of tofu execution is enabled or not
	// +optional
	EnableTofuCLILogging bool `json:"enableTofuCLILogging,omitempty"`
}

// WorkspaceObservation are the observable fields of a Workspace.
type WorkspaceObservation struct {
	Checksum string                       `json:"checksum,omitempty"`
	Outputs  map[string]extensionsV1.JSON `json:"outputs,omitempty"`
}

// A WorkspaceSpec defines the desired state of a Workspace.
type WorkspaceSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       WorkspaceParameters `json:"forProvider"`
}

// A WorkspaceStatus represents the observed state of a Workspace.
type WorkspaceStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          WorkspaceObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Workspace of OpenTofu Configuration.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,opentofu}
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}
