package appplugin

import (
	"context"

	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
)

// createUpdateStrategy is implemented by the single strategy instance that
// grafanaregistry.NewRegistryStore assigns to both Store.CreateStrategy and
// Store.UpdateStrategy, letting schemaValidatingStrategy wrap it without
// re-implementing every method of both interfaces.
type createUpdateStrategy interface {
	rest.RESTCreateStrategy
	rest.RESTUpdateStrategy
}

// schemaValidatingStrategy layers manifest-defined OpenAPI schema validation on
// top of an existing create/update strategy. It is needed because manifest
// kinds are stored as unstructured.Unstructured, so there is no Go type to
// carry field validation -- the schema from the manifest is the only source
// of truth for what a valid object looks like.
type schemaValidatingStrategy struct {
	createUpdateStrategy
	validator validation.SchemaValidator
}

// withSchemaValidation wraps a store's create/update strategy so that objects
// are validated against the given manifest schema in addition to the store's
// existing validation.
func withSchemaValidation(store *registry.Store, validator validation.SchemaValidator) {
	strategy := &schemaValidatingStrategy{
		createUpdateStrategy: store.CreateStrategy.(createUpdateStrategy), //nolint:forcetypeassert
		validator:            validator,
	}
	store.CreateStrategy = strategy
	store.UpdateStrategy = strategy
}

func (s *schemaValidatingStrategy) Validate(ctx context.Context, obj runtime.Object) field.ErrorList {
	errs := s.createUpdateStrategy.Validate(ctx, obj)
	return append(errs, validateAgainstSchema(obj, s.validator)...)
}

func (s *schemaValidatingStrategy) ValidateUpdate(ctx context.Context, obj, old runtime.Object) field.ErrorList {
	errs := s.createUpdateStrategy.ValidateUpdate(ctx, obj, old)
	return append(errs, validateAgainstSchema(obj, s.validator)...)
}

func validateAgainstSchema(obj runtime.Object, validator validation.SchemaValidator) field.ErrorList {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	return validation.ValidateCustomResource(nil, u.UnstructuredContent(), validator)
}
