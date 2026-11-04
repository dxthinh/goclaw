package agent

import (
	"encoding/json"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type capabilityProvider struct {
	providers.Provider
	vision bool
}

func (p capabilityProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{Vision: p.vision}
}

type plainProvider struct{ providers.Provider }

func TestDeferImagesToReadImage(t *testing.T) {
	const chain = `"providers":[{"provider":"openai-codex"}]`
	tests := []struct {
		name     string
		settings string
		provider providers.Provider
		want     bool
	}{
		{"no settings stay inline", "", capabilityProvider{vision: true}, false},
		{"no provider stays inline without vision", `{}`, capabilityProvider{vision: false}, false},
		{"provider with vision main stays inline", `{` + chain + `}`, capabilityProvider{vision: true}, false},
		{"provider without vision main defers", `{` + chain + `}`, capabilityProvider{vision: false}, true},
		{"provider with undeclared main defers", `{` + chain + `}`, plainProvider{}, true},
		{"explicit file_ref wins over vision", `{` + chain + `,"image_input":"file_ref"}`, capabilityProvider{vision: true}, true},
		{"explicit inline wins over no vision", `{` + chain + `,"image_input":"inline"}`, capabilityProvider{vision: false}, false},
		{"unknown value falls back to auto", `{` + chain + `,"image_input":"x"}`, capabilityProvider{vision: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loop := Loop{provider: tt.provider}
			if tt.settings != "" {
				loop.builtinToolSettings = tools.BuiltinToolSettings{"read_image": json.RawMessage(tt.settings)}
			}
			if got := loop.deferImagesToReadImage(); got != tt.want {
				t.Fatalf("deferImagesToReadImage() = %v, want %v", got, tt.want)
			}
		})
	}
}
