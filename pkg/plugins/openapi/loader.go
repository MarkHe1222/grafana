package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/grafana/grafana-app-sdk/app"
	appmanifestV1alpha2 "github.com/grafana/grafana-app-sdk/app/appmanifest/v1alpha2"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/experimental/pluginschema"
	"github.com/grafana/grafana/pkg/plugins"
	"github.com/grafana/grafana/pkg/plugins/manager/sources"
)

// appSDKManifestFile is the statically-named file, read from the root of an app plugin's
// bundle, that holds the plugin's app-sdk manifest (an AppManifest custom resource).
const appSDKManifestFile = "app-sdk-manifest.json"

type PluginInfo struct {
	JSONData plugins.JSONData

	// apiVersion -> schema (currently only v0alpha1)
	// This will be nil if no schemas are found, or if withSchemas is false when loading.
	Schemas map[string]*pluginschema.PluginSchema

	// When an app manifest is defined, we can use that
	Manifest *app.ManifestData
}

func LoadPlugins(ctx context.Context, pluginSources sources.Registry, filter func(plugins.JSONData) bool, withSchemas bool, withManifest bool) ([]PluginInfo, error) {
	var pluginInfo []PluginInfo

	// It's possible that the same plugin will be found in different sources.
	// Registering the same plugin twice in the API is Probably A Bad Thing,
	// so this map keeps track of uniques, so we can skip duplicates.
	var uniquePlugins = map[string]bool{}

	for _, pluginSource := range pluginSources.List(ctx) {
		res, err := pluginSource.Discover(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range res {
			if filter(p.Primary.JSONData) {
				if _, found := uniquePlugins[p.Primary.JSONData.ID]; found {
					backend.Logger.Info("Found duplicate plugin %s when registering API groups.", p.Primary.JSONData.ID)
					continue
				}
				info, err := loadInfo(p.Primary.FS, p.Primary.JSONData, withSchemas, withManifest)
				if err != nil {
					return nil, err
				}
				uniquePlugins[info.JSONData.ID] = true
				pluginInfo = append(pluginInfo, info)
			}

			for _, child := range p.Children {
				if filter(child.JSONData) {
					if _, found := uniquePlugins[child.JSONData.ID]; found {
						backend.Logger.Info("Found duplicate plugin %s when registering API groups.", child.JSONData.ID)
						continue
					}

					info, err := loadInfo(child.FS, child.JSONData, withSchemas, withManifest)
					if err != nil {
						return nil, err
					}
					uniquePlugins[info.JSONData.ID] = true
					pluginInfo = append(pluginInfo, info)
				}
			}
		}
	}
	return pluginInfo, nil
}

func loadInfo(rootfs fs.FS, jsondata plugins.JSONData, withSchemas bool, withManifest bool) (PluginInfo, error) {
	info := PluginInfo{
		JSONData: jsondata,
	}

	if withManifest {
		m, err := loadManifest(rootfs)
		if err != nil {
			return info, err
		}
		info.Manifest = m
	}

	if !withSchemas {
		return info, nil
	}

	fss, err := fs.Sub(rootfs, "schema")
	if err != nil {
		return info, fmt.Errorf("error accessing plugin fs %s: %w", jsondata.ID, err)
	}

	p := pluginschema.NewSchemaProvider(fss)
	schema, err := p.Get("v0alpha1")
	if err != nil {
		return info, fmt.Errorf("error loading schema %s: %w", jsondata.ID, err)
	}
	if !schema.IsZero() {
		info.Schemas = map[string]*pluginschema.PluginSchema{
			"v0alpha1": schema,
		}
	}
	return info, nil
}

func loadManifest(rootfs fs.FS) (*app.ManifestData, error) {
	f, err := rootfs.Open(appSDKManifestFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", appSDKManifestFile, err)
	}
	defer f.Close() //nolint:errcheck

	// TODO... this loads a specific version, but we should support any flavor and convert to the latest version.
	// For now, we only support v1alpha2.
	var cr appmanifestV1alpha2.AppManifest
	if err := json.NewDecoder(f).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decoding AppManifest CR: %w", err)
	}

	manifest, err := cr.Spec.ToManifestData()
	if err != nil {
		return nil, fmt.Errorf("converting AppManifestSpec to ManifestData: %w", err)
	}
	return &manifest, nil
}
