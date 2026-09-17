package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func cliConfig(t *testing.T, listen, public string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "state")
	path := filepath.Join(dir, "config.yaml")
	contents := fmt.Sprintf(`default_model: demo
output_dir: %q
http:
  listen: %q
  public_url: %q
models:
  demo:
    provider: openai
    base_url: https://example.com/v1
    token_env: TEST_UPSTREAM_KEY
    model: test-model
    capabilities: [generate]
    max_images: 1
    max_input_images: 1
`, output, listen, public)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path, output
}
func TestHTTPBindFailureDoesNotStartWorkers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path, output := cliConfig(t, listener.Addr().String(), "http://"+listener.Addr().String())
	if err = run([]string{"-config", path, "-transport", "http"}); err == nil {
		t.Fatal("accepted already occupied port")
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("workers/database initialized before HTTP bind succeeded")
	}
}
func TestCLIConfigCheckHasNoSideEffects(t *testing.T) {
	for _, transport := range []string{"stdio", "http"} {
		t.Run(transport, func(t *testing.T) {
			path, output := cliConfig(t, "127.0.0.1:8080", "http://127.0.0.1:8080")
			if err := run([]string{"-config", path, "-transport", transport, "-check"}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("configuration check created persistent state")
			}
		})
	}
}
func TestCLIRejectsInvalidConfigurationBeforeStartup(t *testing.T) {
	path, output := cliConfig(t, "127.0.0.1:8080", "http://127.0.0.1:8080/wrong-prefix")
	for _, extra := range [][]string{{"-check"}, nil} {
		args := append([]string{"-config", path, "-transport", "http"}, extra...)
		if err := run(args); err == nil {
			t.Fatal("accepted unsupported public URL path")
		}
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("invalid config caused persistent side effects")
	}
	if err := run([]string{"-version", "unexpected"}); err == nil {
		t.Fatal("unknown positional argument ignored")
	}
}
