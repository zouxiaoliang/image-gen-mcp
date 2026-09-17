package imagegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPCheckRejectsUnusableAddress(t *testing.T) {
	t.Setenv("HTTP_CONFIG_TEST_TOKEN", "test-token")
	for _, tc := range []struct{ name, listen, public string }{
		{"public path", "127.0.0.1:8080", "http://localhost:8080/prefix"},
		{"named listen port", "127.0.0.1:invalid", "http://localhost:8080"},
		{"oversized listen port", "127.0.0.1:65536", "http://localhost:8080"},
		{"oversized public port", "127.0.0.1:8080", "http://localhost:65536"},
		{"public query", "127.0.0.1:8080", "http://localhost:8080?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(t, "http://localhost/v1")
			c.HTTP = HTTPConfig{Listen: tc.listen, PublicURL: tc.public, TokenEnv: "HTTP_CONFIG_TEST_TOKEN"}
			if err := c.ValidateHTTP(); err == nil {
				t.Fatal("-check accepted unusable HTTP configuration")
			}
		})
	}
}

func TestEmptyConfiguredPathsAreRejected(t *testing.T) {
	for _, fragment := range []string{"output_dir: \"\"", "allowed_input_dirs: [\"\"]"} {
		// A minimal independent document ensures an empty path is not silently
		// interpreted as the configuration directory.
		path := filepath.Join(t.TempDir(), "config.yaml")
		data := "default_model: demo\n" + fragment + "\nmodels:\n  demo:\n    provider: openai\n    base_url: https://example.com/v1\n    token_env: TEST_TOKEN\n    model: demo\n    capabilities: [generate]\n    max_images: 1\n    max_input_images: 1\n"
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("accepted %s", fragment)
		}
	}
}
