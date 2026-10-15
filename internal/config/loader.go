package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Load reads the config at path, resolving `include:` directives and running
// each file through text/template before parsing (so `{{env "FOO"}}` /
// `{{if}}` / `{{range}}` are available without a new config language).
func Load(path string) (*Config, error) {
	merged, err := loadFile(path, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if err := Validate(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

func loadFile(path string, visited map[string]bool) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", path, err)
	}
	if visited[abs] {
		return nil, fmt.Errorf("circular include detected at %s", abs)
	}
	visited[abs] = true

	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", abs, err)
	}

	rendered, err := renderTemplate(abs, raw)
	if err != nil {
		return nil, fmt.Errorf("templating %s: %w", abs, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(rendered, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", abs, err)
	}

	merged := &Config{Settings: cfg.Settings}
	baseDir := filepath.Dir(abs)

	for _, inc := range cfg.Include {
		incPath := inc
		if !filepath.IsAbs(incPath) {
			incPath = filepath.Join(baseDir, incPath)
		}
		sub, err := loadFile(incPath, visited)
		if err != nil {
			return nil, err
		}
		if sub.Settings.MaxParallelUpdates != 0 {
			merged.Settings = sub.Settings
		}
		merged.Services = append(merged.Services, sub.Services...)
	}

	merged.Services = append(merged.Services, cfg.Services...)
	return merged, nil
}

func renderTemplate(name string, raw []byte) ([]byte, error) {
	funcs := template.FuncMap{
		"env": os.Getenv,
		"envOr": func(key, def string) string {
			if v, ok := os.LookupEnv(key); ok {
				return v
			}
			return def
		},
	}
	tmpl, err := template.New(name).Funcs(funcs).Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Validate checks name uniqueness and that every depends_on target exists.
// Cycle detection happens later in graph.Build, which has the full picture.
func Validate(cfg *Config) error {
	if cfg.Settings.MaxParallelUpdates <= 0 {
		cfg.Settings.MaxParallelUpdates = 5
	}

	seen := map[string]bool{}
	for _, s := range cfg.Services {
		if s.Name == "" {
			return fmt.Errorf("service with empty name")
		}
		if seen[s.Name] {
			return fmt.Errorf("duplicate service name: %s", s.Name)
		}
		seen[s.Name] = true
		if s.Cmd == "" && s.ServeCmd == "" {
			return fmt.Errorf("service %q must set cmd or serve_cmd", s.Name)
		}
		if s.Cmd != "" && s.ServeCmd != "" {
			return fmt.Errorf("service %q cannot set both cmd and serve_cmd", s.Name)
		}
	}
	for _, s := range cfg.Services {
		for _, dep := range s.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("service %q depends on unknown service %q", s.Name, dep)
			}
			if dep == s.Name {
				return fmt.Errorf("service %q depends on itself", s.Name)
			}
		}
	}
	return nil
}
