package imagegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DefaultModel     string           `yaml:"default_model"`
	OutputDir        string           `yaml:"output_dir"`
	AllowedInputDirs []string         `yaml:"allowed_input_dirs"`
	Concurrency      int              `yaml:"concurrency"`
	QueueSize        int              `yaml:"queue_size"`
	TaskTimeout      string           `yaml:"task_timeout"`
	WaitTimeout      string           `yaml:"wait_timeout"`
	Retention        string           `yaml:"retention"`
	PreviewSize      int              `yaml:"preview_size"`
	MaxImageBytes    int64            `yaml:"max_image_bytes"`
	MaxImagePixels   int64            `yaml:"max_image_pixels"`
	HTTP             HTTPConfig       `yaml:"http"`
	Models           map[string]Model `yaml:"models"`
}
type HTTPConfig struct {
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"`
	TokenEnv  string `yaml:"token_env"`
}
type Model struct {
	Provider       string               `yaml:"provider" json:"provider"`
	BaseURL        string               `yaml:"base_url" json:"base_url"`
	Token          string               `yaml:"token,omitempty" json:"-"`
	TokenEnv       string               `yaml:"token_env" json:"token_env"`
	Model          string               `yaml:"model" json:"model"`
	Capabilities   []string             `yaml:"capabilities" json:"capabilities"`
	Parameters     map[string][]string  `yaml:"parameters" json:"parameters"`
	Extensions     map[string]Extension `yaml:"extensions" json:"extensions"`
	MaxImages      int                  `yaml:"max_images" json:"max_images"`
	MaxInputImages int                  `yaml:"max_input_images" json:"max_input_images"`
}
type Extension struct {
	Type string   `yaml:"type" json:"type"`
	Enum []string `yaml:"enum,omitempty" json:"enum,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	c := Config{OutputDir: "data", Concurrency: 2, QueueSize: 20, TaskTimeout: "10m", WaitTimeout: "3s", Retention: "0s", PreviewSize: 768, MaxImageBytes: 20 << 20, MaxImagePixels: 40_000_000, HTTP: HTTPConfig{Listen: "127.0.0.1:8080", TokenEnv: "IMAGE_MCP_TOKEN"}}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err = d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("config must contain exactly one YAML document")
	}
	if c.OutputDir == "" {
		return c, fmt.Errorf("output_dir cannot be empty")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.OutputDir) {
		c.OutputDir = filepath.Join(base, c.OutputDir)
	}
	for i, p := range c.AllowedInputDirs {
		if p == "" {
			return c, fmt.Errorf("allowed_input_dirs cannot contain an empty path")
		}
		if !filepath.IsAbs(p) {
			c.AllowedInputDirs[i] = filepath.Join(base, p)
		}
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.OutputDir == "" || c.Concurrency < 1 || c.Concurrency > 64 || c.QueueSize < 1 || c.QueueSize > 10000 {
		return fmt.Errorf("output_dir, concurrency (1..64), and queue_size (1..10000) are required")
	}
	for k, v := range map[string]string{"task_timeout": c.TaskTimeout, "wait_timeout": c.WaitTimeout, "retention": c.Retention} {
		d, e := time.ParseDuration(v)
		if e != nil || d < 0 || (k == "task_timeout" && d == 0) {
			return fmt.Errorf("invalid %s", k)
		}
	}
	if duration(c.WaitTimeout) > 30*time.Second {
		return fmt.Errorf("wait_timeout must be at most 30s")
	}
	if c.PreviewSize < 64 || c.PreviewSize > 2048 || c.MaxImageBytes < 1024 || c.MaxImageBytes > 100<<20 || c.MaxImagePixels < 1 || c.MaxImagePixels > 100_000_000 {
		return fmt.Errorf("invalid preview_size or image limits")
	}
	if _, ok := c.Models[c.DefaultModel]; !ok {
		return fmt.Errorf("default_model must name a configured model")
	}
	for alias, m := range c.Models {
		if m.Provider != "openai" && m.Provider != "openai-compatible" && m.Provider != "gemini" {
			return fmt.Errorf("model %s: unsupported provider", alias)
		}
		if alias == "" || m.Model == "" || (m.Token == "" && m.TokenEnv == "") {
			return fmt.Errorf("model %s: model and either token or token_env are required", alias)
		}
		if m.Token != "" && (strings.TrimSpace(m.Token) != m.Token || strings.ContainsAny(m.Token, "\r\n")) {
			return fmt.Errorf("model %s: token must not contain surrounding whitespace or line breaks", alias)
		}
		if err := validateBaseURL(m.BaseURL); err != nil {
			return fmt.Errorf("model %s: %w", alias, err)
		}
		if m.MaxImages < 1 || m.MaxImages > 16 || m.MaxInputImages < 1 || m.MaxInputImages > 16 {
			return fmt.Errorf("model %s: image limits must be 1..16", alias)
		}
		if m.Provider == "gemini" && m.MaxImages != 1 {
			return fmt.Errorf("model %s: Gemini adapter supports n=1", alias)
		}
		for _, cap := range m.Capabilities {
			if !slices.Contains([]string{"generate", "edit", "mask"}, cap) {
				return fmt.Errorf("model %s: unknown capability %s", alias, cap)
			}
		}
		if len(m.Capabilities) == 0 {
			return fmt.Errorf("model %s: capabilities required", alias)
		}
		if slices.Contains(m.Capabilities, "mask") && (!slices.Contains(m.Capabilities, "edit") || m.Provider == "gemini") {
			return fmt.Errorf("model %s: mask requires an OpenAI edit adapter", alias)
		}
		for p := range m.Parameters {
			allowed := []string{"size", "quality", "output_format"}
			if m.Provider == "gemini" {
				allowed = []string{"size", "aspect_ratio"}
			}
			if !slices.Contains(allowed, p) {
				return fmt.Errorf("model %s: unsupported parameter %s for adapter", alias, p)
			}
		}
		for key, r := range m.Extensions {
			if !extensionAllowed(m.Provider, key) || !slices.Contains([]string{"string", "number", "integer", "boolean"}, r.Type) {
				return fmt.Errorf("model %s: unsupported extension %s or type", alias, key)
			}
		}
	}
	return nil
}

// Inline credentials are never JSON-serialized into task snapshots. The
// configuration remains the credential source after a process restart.
func (m Model) credential() string {
	if m.Token != "" {
		return m.Token
	}
	return os.Getenv(m.TokenEnv)
}

func (c Config) taskCredential(alias string, snapshot Model) (string, error) {
	current, ok := c.Models[alias]
	if !ok || current.Provider != snapshot.Provider || strings.TrimRight(current.BaseURL, "/") != strings.TrimRight(snapshot.BaseURL, "/") || current.Model != snapshot.Model {
		return "", fail("configuration_error", "queued model provider, address or model has changed; credentials cannot be applied to its saved request")
	}
	token := current.credential()
	if token == "" {
		return "", fail("configuration_error", "selected model has no token; configure token or its token_env variable")
	}
	return token, nil
}
func validateBaseURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("base_url must be an http(s) URL without credentials, query, or fragment")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("URL port must be between 1 and 65535")
		}
	}
	return nil
}
func (c Config) ValidateHTTP() error {
	host, port, e := net.SplitHostPort(c.HTTP.Listen)
	if e != nil {
		return fmt.Errorf("http.listen must be host:port")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("http.listen port must be between 0 and 65535")
	}
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && os.Getenv(c.HTTP.TokenEnv) == "" {
		return fmt.Errorf("remote HTTP requires token in %s", c.HTTP.TokenEnv)
	}
	if c.HTTP.PublicURL == "" {
		return fmt.Errorf("http.public_url is required for downloadable image URLs")
	}
	if e = validateBaseURL(c.HTTP.PublicURL); e != nil {
		return fmt.Errorf("http.public_url: %w", e)
	}
	public, _ := url.Parse(c.HTTP.PublicURL)
	if public.Path != "" && public.Path != "/" {
		return fmt.Errorf("http.public_url must be an origin without a path")
	}
	return nil
}
func duration(v string) time.Duration { d, _ := time.ParseDuration(v); return d }
func extensionAllowed(provider, key string) bool {
	if provider == "gemini" {
		return slices.Contains([]string{"temperature", "topP", "topK", "seed"}, key)
	}
	return slices.Contains([]string{"background", "moderation", "output_compression", "input_fidelity", "response_format", "style", "user"}, key)
}
func (c Config) Normalize(operation string, r Request) (Request, Model, error) {
	if r.Model == "" {
		r.Model = c.DefaultModel
	}
	m, ok := c.Models[r.Model]
	if !ok {
		return r, m, invalid("unknown model alias")
	}
	if !slices.Contains(m.Capabilities, operation) {
		return r, m, invalid("model does not support %s", operation)
	}
	if strings.TrimSpace(r.Prompt) == "" || len(r.Prompt) > 100_000 {
		return r, m, invalid("prompt must contain 1..100000 bytes")
	}
	if len(r.RequestID) > 200 {
		return r, m, invalid("request_id too long")
	}
	if r.N == 0 {
		r.N = 1
	}
	if r.N < 1 || r.N > m.MaxImages {
		return r, m, invalid("n must be between 1 and %d", m.MaxImages)
	}
	if operation == "generate" && (len(r.Images) > 0 || r.Mask != nil) {
		return r, m, invalid("generate_image does not accept images or mask; use edit_image")
	}
	if operation == "edit" && (len(r.Images) == 0 || len(r.Images) > m.MaxInputImages) {
		return r, m, invalid("edit requires 1..%d images", m.MaxInputImages)
	}
	if r.Mask != nil && !slices.Contains(m.Capabilities, "mask") {
		return r, m, invalid("model does not support masks")
	}
	if m.Provider != "gemini" && r.Size != "" && r.AspectRatio != "" {
		return r, m, invalid("specify size or aspect_ratio, not both")
	}
	for key, value := range map[string]string{"size": r.Size, "aspect_ratio": r.AspectRatio, "quality": r.Quality, "output_format": r.OutputFormat} {
		if value == "" {
			continue
		}
		values, supported := m.Parameters[key]
		if !supported || (len(values) > 0 && !slices.Contains(values, value)) {
			return r, m, invalid("unsupported %s value for this model", key)
		}
	}
	for k, v := range r.Extra {
		if (k == "input_fidelity" && operation != "edit") || (k == "style" && operation != "generate") {
			return r, m, invalid("extension %s is not supported for %s", k, operation)
		}
		rule, ok := m.Extensions[k]
		if !ok {
			return r, m, invalid("extension %s is not allowed", k)
		}
		if !matchesExtension(v, rule) {
			return r, m, invalid("invalid extension %s", k)
		}
	}
	for _, in := range r.Images {
		if err := validateInput(in); err != nil {
			return r, m, err
		}
	}
	if r.Mask != nil {
		if err := validateInput(*r.Mask); err != nil {
			return r, m, err
		}
	}
	return r, m, nil
}
func matchesExtension(v any, r Extension) bool {
	b, _ := json.Marshal(v)
	var normalized any
	_ = json.Unmarshal(b, &normalized)
	switch x := normalized.(type) {
	case string:
		return r.Type == "string" && (len(r.Enum) == 0 || slices.Contains(r.Enum, x))
	case bool:
		return r.Type == "boolean"
	case float64:
		return r.Type == "number" || (r.Type == "integer" && x == float64(int64(x)))
	}
	return false
}
func validateInput(in ImageInput) error {
	n := 0
	for _, s := range []string{in.Path, in.URL, in.Data} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return invalid("each image must have exactly one of path, url, data")
	}
	return nil
}
