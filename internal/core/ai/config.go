package ai

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

type File struct {
	Routes map[string]RouteYAML `yaml:"routes"`
}

type RouteYAML struct {
	Capabilities    []string       `yaml:"capabilities"`
	MaxInputTokens  int            `yaml:"maxInputTokens"`
	MaxOutputTokens int            `yaml:"maxOutputTokens"`
	Attempts        []AttemptYAML  `yaml:"attempts"`
}

type AttemptYAML struct {
	Provider                 string `yaml:"provider"`
	Model                    string `yaml:"model"`
	Credential               string `yaml:"credential"`
	InputMicroUSDPerMillion  int64  `yaml:"inputMicroUSDPerMillion"`
	OutputMicroUSDPerMillion int64  `yaml:"outputMicroUSDPerMillion"`
}

type route struct {
	name            string
	capabilities    map[string]struct{}
	capList         []string
	maxInputTokens  int
	maxOutputTokens int
	attempts        []attempt
	lastError       string
}

type attempt struct {
	provider, model, credential string
	inPerM, outPerM             policy.MicroUSD
}

func LoadRoutes(path string) ([]route, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var file File
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("ai: parse %s: %w", path, err)
	}
	return compileRoutes(file)
}

func compileRoutes(file File) ([]route, error) {
	var out []route
	for name, y := range file.Routes {
		if y.MaxInputTokens <= 0 || y.MaxOutputTokens <= 0 {
			return nil, fmt.Errorf("ai: route %q: limits must be finite and positive", name)
		}
		if len(y.Attempts) == 0 {
			return nil, fmt.Errorf("ai: route %q: attempt plan is empty", name)
		}
		r := route{
			name: name, maxInputTokens: y.MaxInputTokens, maxOutputTokens: y.MaxOutputTokens,
			capabilities: map[string]struct{}{}, capList: y.Capabilities,
		}
		for _, c := range y.Capabilities {
			r.capabilities[c] = struct{}{}
		}
		for _, a := range y.Attempts {
			if a.Provider == "" || a.Model == "" || a.Credential == "" {
				return nil, fmt.Errorf("ai: route %q: provider, model, and credential are required", name)
			}
			if a.InputMicroUSDPerMillion <= 0 || a.OutputMicroUSDPerMillion <= 0 {
				return nil, fmt.Errorf("%w: route %q", ErrMissingPrice, name)
			}
			r.attempts = append(r.attempts, attempt{
				provider: a.Provider, model: a.Model, credential: a.Credential,
				inPerM: policy.MicroUSD(a.InputMicroUSDPerMillion),
				outPerM: policy.MicroUSD(a.OutputMicroUSDPerMillion),
			})
		}
		out = append(out, r)
	}
	return out, nil
}

func (r route) has(cap string) bool {
	_, ok := r.capabilities[cap]
	return ok
}

func (r route) estimateChat(maxTokens int) (policy.MicroUSD, error) {
	outTok := r.maxOutputTokens
	if maxTokens > 0 && maxTokens < outTok {
		outTok = maxTokens
	}
	var total policy.MicroUSD
	for _, a := range r.attempts {
		in, err := tokensCost(int64(r.maxInputTokens), a.inPerM)
		if err != nil {
			return 0, err
		}
		out, err := tokensCost(int64(outTok), a.outPerM)
		if err != nil {
			return 0, err
		}
		sum := in + out
		if sum < in {
			return 0, ErrUnbounded
		}
		next := total + sum
		if next < total {
			return 0, ErrUnbounded
		}
		total = next
	}
	if total <= 0 {
		return 0, ErrUnbounded
	}
	return total, nil
}

func tokensCost(tokens int64, perMillion policy.MicroUSD) (policy.MicroUSD, error) {
	if tokens < 0 || perMillion <= 0 {
		return 0, ErrUnbounded
	}
	// Round up: (tokens * perMillion + 999_999) / 1_000_000
	n := tokens * int64(perMillion)
	if tokens != 0 && n/tokens != int64(perMillion) {
		return 0, ErrUnbounded
	}
	return policy.MicroUSD((n + 999_999) / 1_000_000), nil
}

func (a attempt) charge(inTok, outTok int64) (policy.MicroUSD, error) {
	in, err := tokensCost(inTok, a.inPerM)
	if err != nil {
		return 0, err
	}
	out, err := tokensCost(outTok, a.outPerM)
	if err != nil {
		return 0, err
	}
	sum := in + out
	if sum < in {
		return 0, ErrUnbounded
	}
	return sum, nil
}
