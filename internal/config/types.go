package config

import (
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so it can be parsed from YAML strings like "3s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

type Settings struct {
	MaxParallelUpdates int `yaml:"max_parallel_updates"`
}

type HTTPProbe struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

type TCPProbe struct {
	Port int `yaml:"port"`
}

type ExecProbe struct {
	Cmd []string `yaml:"cmd"`
}

type Readiness struct {
	HTTP             *HTTPProbe `yaml:"http,omitempty"`
	TCP              *TCPProbe  `yaml:"tcp,omitempty"`
	Exec             *ExecProbe `yaml:"exec,omitempty"`
	InitialDelay     Duration   `yaml:"initial_delay"`
	Period           Duration   `yaml:"period"`
	Timeout          Duration   `yaml:"timeout"`
	SuccessThreshold int        `yaml:"success_threshold"`
}

type Service struct {
	Name      string            `yaml:"name"`
	Cmd       string            `yaml:"cmd,omitempty"`
	ServeCmd  string            `yaml:"serve_cmd,omitempty"`
	Dir       string            `yaml:"dir,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Labels    []string          `yaml:"labels,omitempty"`
	DependsOn []string          `yaml:"depends_on,omitempty"`
	Deps      []string          `yaml:"deps,omitempty"`
	AutoInit  *bool             `yaml:"auto_init,omitempty"`
	Readiness *Readiness        `yaml:"readiness,omitempty"`
}

// IsServe reports whether this service is long-running (serve_cmd) as
// opposed to one-shot (cmd).
func (s Service) IsServe() bool { return s.ServeCmd != "" }

// Command returns whichever of cmd / serve_cmd was set.
func (s Service) Command() string {
	if s.ServeCmd != "" {
		return s.ServeCmd
	}
	return s.Cmd
}

// AutoInitEnabled defaults to true when auto_init is unset.
func (s Service) AutoInitEnabled() bool {
	if s.AutoInit == nil {
		return true
	}
	return *s.AutoInit
}

type Config struct {
	Settings Settings  `yaml:"settings"`
	Include  []string  `yaml:"include,omitempty"`
	Services []Service `yaml:"services"`
}
