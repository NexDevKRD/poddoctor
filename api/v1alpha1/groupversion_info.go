// Package v1alpha1 contains API Schema definitions for the diagnostics v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=diagnostics.poddoctor.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group-version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "diagnostics.poddoctor.dev", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	// Built directly on apimachinery's runtime.SchemeBuilder rather than
	// controller-runtime's pkg/scheme.Builder wrapper (deprecated for API
	// packages, which should depend on as little outside the standard
	// library and apimachinery as possible).
	SchemeBuilder = runtime.NewSchemeBuilder()

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
