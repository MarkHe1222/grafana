package appplugin

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// The OpenAPI spec builder looks up definitions by the canonical name of each
// route's sample object. This test guards the contract that kindDocsSample
// and GetOpenAPIDefinitions agree on that name -- a mismatch silently falls
// back to a generic object schema in the served spec.
func TestManifestKindOpenAPINames(t *testing.T) {
	group := exampleManifestData.Group
	version := "v1alpha1"

	b := &AppPluginAPIBuilder{
		manifest:     &exampleManifestData,
		groupVersion: schema.GroupVersion{Group: group, Version: version},
	}
	defs := b.GetOpenAPIDefinitions()(func(path string) spec.Ref {
		return spec.MustCreateRef("#/definitions/" + path)
	})

	sample := &kindDocsSample{}
	sample.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: "TestKind"})

	name := sample.OpenAPICanonicalTypeName()
	require.Equal(t, "example.ext.grafana.com.v1alpha1.TestKind", name)

	def, ok := defs[name]
	require.True(t, ok, "definition map must contain the sample's canonical name, got keys: %v", keys(defs))
	require.Contains(t, def.Schema.Properties, "spec")
	require.Contains(t, def.Schema.Properties, "status")

	_, ok = defs[name+"List"]
	require.True(t, ok, "expected a list definition for the kind")
}

func TestPostProcessManifestKindRequestBodies(t *testing.T) {
	group := exampleManifestData.Group
	version := "v1alpha1"

	b := &AppPluginAPIBuilder{
		manifest:     &exampleManifestData,
		groupVersion: schema.GroupVersion{Group: group, Version: version},
	}

	root := "/apis/" + b.groupVersion.String() + "/"
	base := root + "namespaces/{namespace}/testkinds"
	body := func() *spec3.RequestBody {
		return &spec3.RequestBody{
			RequestBodyProps: spec3.RequestBodyProps{
				Content: map[string]*spec3.MediaType{
					"application/json": {MediaTypeProps: spec3.MediaTypeProps{Schema: spec.MapProperty(nil)}},
					"application/yaml": {MediaTypeProps: spec3.MediaTypeProps{Schema: spec.MapProperty(nil)}},
				},
			},
		}
	}
	listResponse := &spec3.Responses{
		ResponsesProps: spec3.ResponsesProps{
			StatusCodeResponses: map[int]*spec3.Response{
				200: {ResponseProps: spec3.ResponseProps{
					Content: map[string]*spec3.MediaType{
						"application/json": {MediaTypeProps: spec3.MediaTypeProps{Schema: spec.MapProperty(nil)}},
					},
				}},
			},
		},
	}
	oas := &spec3.OpenAPI{
		Paths: &spec3.Paths{
			Paths: map[string]*spec3.Path{
				base: {PathProps: spec3.PathProps{
					Get:  &spec3.Operation{OperationProps: spec3.OperationProps{Responses: listResponse}},
					Post: &spec3.Operation{OperationProps: spec3.OperationProps{RequestBody: body()}},
				}},
				base + "/{name}": {PathProps: spec3.PathProps{
					Put: &spec3.Operation{OperationProps: spec3.OperationProps{RequestBody: body()}},
				}},
			},
		},
		Components: &spec3.Components{Schemas: map[string]*spec.Schema{}},
	}

	b.postProcessManifestKinds(oas, root)

	expected := "#/components/schemas/example.ext.grafana.com.v1alpha1.TestKind"
	for _, mt := range oas.Paths.Paths[base].Post.RequestBody.Content {
		require.Equal(t, expected, mt.Schema.Ref.String())
	}
	for _, mt := range oas.Paths.Paths[base+"/{name}"].Put.RequestBody.Content {
		require.Equal(t, expected, mt.Schema.Ref.String())
	}

	// The list component must be injected and the list response repointed at it
	listSchema, ok := oas.Components.Schemas["example.ext.grafana.com.v1alpha1.TestKindList"]
	require.True(t, ok, "expected the TestKindList component to be injected")
	require.Equal(t, expected, listSchema.Properties["items"].Items.Schema.Ref.String())
	for _, mt := range oas.Paths.Paths[base].Get.Responses.StatusCodeResponses[200].Content {
		require.Equal(t, expected+"List", mt.Schema.Ref.String())
	}
}

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
