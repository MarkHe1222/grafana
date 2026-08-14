package appplugin

import (
	"fmt"
	"maps"
	"net/http"
	"strings"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/spec3"
	openapiutil "k8s.io/kube-openapi/pkg/util"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/grafana/grafana-plugin-sdk-go/experimental/pluginschema"
	kcommon "github.com/grafana/grafana/pkg/apimachinery/apis/common/v0alpha1"
	apppluginV0 "github.com/grafana/grafana/pkg/apis/appplugin/v0alpha1"
	"github.com/grafana/grafana/pkg/plugins/openapi"
)

func (b *AppPluginAPIBuilder) GetOpenAPIDefinitions() common.GetOpenAPIDefinitions {
	return func(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
		base := apppluginV0.GetOpenAPIDefinitions(ref)
		if b.manifest != nil {
			// TODO: this should likely be moved to a utility in the SDK that would collect
			// all definitions across the manifest
			for _, version := range b.manifest.Versions {
				if version.Name != b.groupVersion.Version {
					continue
				}
				if !version.Served {
					continue
				}

				for _, kind := range version.Kinds {
					gvk := schema.GroupVersionKind{
						Group:   b.manifest.Group,
						Version: version.Name,
						Kind:    kind.Kind,
					}
					k, err := kind.Schema.AsKubeOpenAPI(gvk, ref, fmt.Sprintf("%s.%s", gvk.Group, gvk.Version))
					if err != nil {
						fmt.Printf("ERROR getting KubeOpenAPI! %v >>> %+v", gvk, err)
						continue
					}
					maps.Copy(base, k)
				}
			}
		}
		return base
	}
}

func (b *AppPluginAPIBuilder) PostProcessOpenAPI(oas *spec3.OpenAPI) (*spec3.OpenAPI, error) {
	var schema *pluginschema.PluginSchema
	if b.schemas != nil {
		schema = b.schemas[b.GetGroupVersion().Version]
	}

	// The plugin description
	oas.Info.Description = b.pluginJSON.Info.Description

	// Add plugin information
	info := map[string]any{
		"id": b.pluginJSON.ID,
	}
	if b.pluginJSON.Info.Version != "" {
		info["version"] = b.pluginJSON.Info.Version
	}
	if b.pluginJSON.Info.Build.Time > 0 {
		info["build"] = b.pluginJSON.Info.Build.Time
	}
	oas.Info.AddExtension("x-grafana-plugin", info)

	// The root api URL
	root := "/apis/" + b.groupVersion.String() + "/"

	b.postProcessManifestKinds(oas, root)

	// Hide the resource+proxy routes -- explicit ones will be added if defined below
	for _, v := range []string{"resources", "proxy"} {
		prefix := root + "namespaces/{namespace}/app/{name}/" + v
		r := oas.Paths.Paths[prefix]
		if r != nil && r.Get != nil {
			r.Get.Description = "Get resources in the " + v + " plugin. NOTE, additional routes may exist, but are not exposed via OpenAPI"
			r.Delete = nil
			r.Head = nil
			r.Patch = nil
			r.Post = nil
			r.Put = nil
			r.Options = nil
		}
		delete(oas.Paths.Paths, prefix+"/{path}")
	}

	// Set explicit apiVersion and kind on the datasource
	ps, ok := oas.Components.Schemas[apppluginV0.Settings{}.OpenAPIModelName()]
	if !ok {
		return nil, fmt.Errorf("missing settings type")
	}
	ps.Properties["apiVersion"] = *spec.StringProperty().WithEnum(b.GetGroupVersion().String())
	ps.Properties["kind"] = *spec.StringProperty().WithEnum("Settings")

	// Always transform results
	switch {
	case schema.IsZero():
		schema = defaultSchema()
	case schema.SettingsSchema.IsZero():
		schema.SettingsSchema = defaultSchema().SettingsSchema
	}

	return openapi.AugmentOpenAPI(oas, openapi.PluginOptions{
		Schema:   schema,
		Resource: ps,
		SpecName: "SettingsSpec",
		Path:     root + "namespaces/{namespace}/app",
		IsApp:    true,
	})
}

// postProcessManifestKinds fixes the routes of manifest-defined kinds that
// the endpoint installer documents from scheme-created zero-value unstructured
// objects, which have no per-kind identity: create/update request bodies
// (rest.StorageMetadata only covers responses) and the list response. The
// <Kind>List component is injected here as well since no route references it,
// so the spec builder never includes it on its own.
func (b *AppPluginAPIBuilder) postProcessManifestKinds(oas *spec3.OpenAPI, root string) {
	if b.manifest == nil {
		return
	}
	for _, v := range b.manifest.Versions {
		if v.Name != b.groupVersion.Version || !v.Served {
			continue
		}
		for _, kind := range v.Kinds {
			// Same canonical name the kindDocsSample carries, converted the same
			// way the definition namer converts it into a component key.
			friendly := fmt.Sprintf("%s.%s.%s", b.manifest.Group, v.Name, kind.Kind)
			ref := spec.MustCreateRef("#/components/schemas/" + friendly)
			base := root + "namespaces/{namespace}/" + strings.ToLower(kind.Plural)
			if p := oas.Paths.Paths[base]; p != nil && p.Post != nil {
				setRequestBodySchemaRef(p.Post.RequestBody, ref)
			}
			if p := oas.Paths.Paths[base+"/{name}"]; p != nil && p.Put != nil {
				setRequestBodySchemaRef(p.Put.RequestBody, ref)
			}

			if oas.Components == nil || oas.Components.Schemas == nil {
				continue
			}
			listName := friendly + "List"
			oas.Components.Schemas[listName] = kindListSchema(kind.Kind, ref)
			if p := oas.Paths.Paths[base]; p != nil && p.Get != nil {
				setResponseSchemaRef(p.Get, http.StatusOK,
					spec.MustCreateRef("#/components/schemas/"+listName))
			}
		}
	}
}

// kindListSchema mirrors the <Kind>List definition AsKubeOpenAPI produces,
// with refs pointing at components already present in the built spec. The
// ListMeta component is always reachable through the settings kind, so the
// metadata ref never dangles.
func kindListSchema(kind string, itemRef spec.Ref) *spec.Schema {
	return &spec.Schema{
		SchemaProps: spec.SchemaProps{
			Description: kind + "List is a list of " + kind,
			Type:        []string{"object"},
			Properties: map[string]spec.Schema{
				"kind":       *spec.StringProperty(),
				"apiVersion": *spec.StringProperty(),
				"metadata": {SchemaProps: spec.SchemaProps{
					Ref: spec.MustCreateRef("#/components/schemas/" + v1.ListMeta{}.OpenAPIModelName()),
				}},
				"items": {SchemaProps: spec.SchemaProps{
					Type: []string{"array"},
					Items: &spec.SchemaOrArray{
						Schema: &spec.Schema{SchemaProps: spec.SchemaProps{Ref: itemRef}},
					},
				}},
			},
			Required: []string{"metadata", "items"},
		},
	}
}

// setResponseSchemaRef replaces the schema of every media type in the
// response for the given status code with a reference to the given schema.
func setResponseSchemaRef(op *spec3.Operation, code int, ref spec.Ref) {
	if op.Responses == nil {
		return
	}
	resp := op.Responses.StatusCodeResponses[code]
	if resp == nil {
		return
	}
	for _, mt := range resp.Content {
		mt.Schema = &spec.Schema{SchemaProps: spec.SchemaProps{Ref: ref}}
	}
}

// setRequestBodySchemaRef replaces the schema of every media type in a
// request body with a reference to the given schema.
func setRequestBodySchemaRef(body *spec3.RequestBody, ref spec.Ref) {
	if body == nil {
		return
	}
	for _, mt := range body.Content {
		mt.Schema = &spec.Schema{SchemaProps: spec.SchemaProps{Ref: ref}}
	}
}

var (
	_ openapiutil.OpenAPICanonicalTypeNamer = (*kindDocsSample)(nil)
	_ rest.StorageMetadata                  = (*kindStorage)(nil)
)

// kindDocsSample is the OpenAPI documentation sample for a manifest-defined
// kind. Manifest kinds are served as unstructured.Unstructured, which would
// normally make every kind reflect to the same generic OpenAPI definition;
// kube-openapi checks OpenAPICanonicalTypeNamer on the sample instance before
// falling back to reflection, so this carries the per-kind definition name.
// It is never stored or served -- documentation only.
type kindDocsSample struct {
	unstructured.Unstructured
}

// OpenAPICanonicalTypeName returns the canonical name (group.version.Kind),
// matching the definition keys produced by AsKubeOpenAPI in
// GetOpenAPIDefinitions above. The definition namer no longer rewrites names,
// so this is also the component name in the served spec -- it must not
// contain a slash.
func (u *kindDocsSample) OpenAPICanonicalTypeName() string {
	gvk := u.GroupVersionKind()
	return gvk.Group + "." + gvk.Version + "." + gvk.Kind
}

// kindStorage adds rest.StorageMetadata to a manifest kind's store so the
// endpoint installer documents responses with the kind's own schema rather
// than the zero-value unstructured object it would otherwise create through
// the scheme. This hook only covers single-object responses; request bodies
// and list responses are rewritten in postProcessManifestKinds.
type kindStorage struct {
	*registry.Store
	sample *kindDocsSample
}

func (s *kindStorage) ProducesMIMETypes(verb string) []string { return nil }

func (s *kindStorage) ProducesObject(verb string) interface{} { return s.sample }

func defaultSchema() *pluginschema.PluginSchema {
	return &pluginschema.PluginSchema{
		SettingsSchema: &pluginschema.Settings{
			Spec: &spec.Schema{
				SchemaProps: spec.SchemaProps{ // The jsonSchema object
					Type:                 []string{"object"},
					AdditionalProperties: &spec.SchemaOrBool{Allows: true},
				},
			},
		},
		SettingsExamples: &pluginschema.SettingsExamples{
			Examples: map[string]*spec3.Example{
				"empty": {
					ExampleProps: spec3.ExampleProps{
						Summary: "example",
						Value: apppluginV0.Settings{
							ObjectMeta: v1.ObjectMeta{
								Name: apppluginV0.INSTANCE_NAME,
							},
							Spec: apppluginV0.SettingsSpec{
								Enabled:  true,
								Pinned:   true,
								JsonData: kcommon.Unstructured{},
							},
						},
					},
				},
			},
		},
	}
}
