package ai

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// File is the optional seed for a database with no providers or routes yet.
//
// Routes are administrator-owned state now: they are created and edited from Settings
// against a live model catalog, and they live in SQLite. This file exists so an
// existing deployment keeps the routes it already declared, and so a fresh one can be
// brought up with a known configuration. It is read once, when the tables are empty.
type File struct {
	Providers map[string]ProviderYAML `yaml:"providers"`
	Routes    map[string]RouteYAML    `yaml:"routes"`
}

type ProviderYAML struct {
	Kind       string `yaml:"kind"`
	BaseURL    string `yaml:"baseUrl"`
	Credential string `yaml:"credential"`
	Billing    string `yaml:"billing"`
}

type RouteYAML struct {
	Capabilities    []string      `yaml:"capabilities"`
	MaxInputTokens  int           `yaml:"maxInputTokens"`
	MaxOutputTokens int           `yaml:"maxOutputTokens"`
	Attempts        []AttemptYAML `yaml:"attempts"`
}

type AttemptYAML struct {
	Provider                 string `yaml:"provider"`
	Model                    string `yaml:"model"`
	Credential               string `yaml:"credential"`
	InputMicroUSDPerMillion  int64  `yaml:"inputMicroUSDPerMillion"`
	OutputMicroUSDPerMillion int64  `yaml:"outputMicroUSDPerMillion"`
}

// Seed is a provider set and a route set ready to be written to an empty database.
type Seed struct {
	Providers []ProviderConfig
	Routes    []RouteInput
}

// LoadSeed reads the optional seed file. A missing file is not an error; it means the
// deployment configures everything from Settings.
func LoadSeed(path string) (Seed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Seed{}, nil
		}
		return Seed{}, err
	}
	var file File
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Seed{}, fmt.Errorf("ai: parse %s: %w", path, err)
	}
	return compileSeed(file)
}

// compileSeed turns the file into provider and route records. An attempt that names a
// provider the file does not declare implies one: older files named a provider and a
// credential per attempt, with no provider section at all.
func compileSeed(file File) (Seed, error) {
	var seed Seed
	providers := map[string]ProviderConfig{}

	for id, y := range file.Providers {
		p := ProviderConfig{
			ID: id, Kind: ProviderKind(y.Kind), BaseURL: y.BaseURL,
			CredentialID: y.Credential, Billing: Billing(y.Billing),
		}.normalize()
		if err := p.validate(); err != nil {
			return Seed{}, err
		}
		providers[id] = p
	}

	for name, y := range file.Routes {
		in := RouteInput{
			Name: name, Capabilities: y.Capabilities,
			MaxInputTokens: y.MaxInputTokens, MaxOutputTokens: y.MaxOutputTokens,
		}
		for _, a := range y.Attempts {
			if a.Provider == "" || a.Model == "" {
				return Seed{}, fmt.Errorf("%w: route %q: provider and model are required", ErrInvalidRoute, name)
			}
			if _, ok := providers[a.Provider]; !ok {
				if a.Credential == "" {
					return Seed{}, fmt.Errorf("%w: route %q: provider %q is not declared and the attempt names no credential",
						ErrInvalidRoute, name, a.Provider)
				}
				kind, base := impliedProvider(a.Provider)
				implied := ProviderConfig{
					ID: a.Provider, Kind: kind, BaseURL: base,
					CredentialID: a.Credential, Billing: BillingMetered,
				}.normalize()
				if err := implied.validate(); err != nil {
					return Seed{}, err
				}
				providers[a.Provider] = implied
			}
			in.Attempts = append(in.Attempts, RouteAttemptInput{
				Provider: a.Provider, Model: a.Model,
				InputMicroUSDPerMillion:  a.InputMicroUSDPerMillion,
				OutputMicroUSDPerMillion: a.OutputMicroUSDPerMillion,
			})
		}
		seed.Routes = append(seed.Routes, in)
	}

	for _, p := range providers {
		seed.Providers = append(seed.Providers, p)
	}
	return seed, nil
}

// impliedProvider guesses the adapter and endpoint for a provider a seed file names
// but does not declare. Only well-known ids get a base URL; anything else has to
// declare one and fails validation with that message if it does not.
func impliedProvider(id string) (ProviderKind, string) {
	switch id {
	case string(KindFake):
		return KindFake, ""
	case string(KindCodex):
		return KindCodex, CodexBaseURL
	case "openai":
		return KindOpenAICompatible, "https://api.openai.com/v1"
	case "openrouter":
		return KindOpenAICompatible, "https://openrouter.ai/api/v1"
	default:
		return KindOpenAICompatible, ""
	}
}
