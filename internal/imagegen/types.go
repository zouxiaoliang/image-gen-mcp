package imagegen

import (
	"encoding/json"
	"fmt"
	"time"
)

// ImageInput has exactly one source. Paths are on the MCP server, not the client.
type ImageInput struct {
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Data string `json:"data,omitempty"` // raw base64, not a data URL
}

type Request struct {
	Model        string         `json:"model,omitempty" jsonschema:"Configured model alias; omit for the default"`
	Prompt       string         `json:"prompt" jsonschema:"Describe the image or the requested edit"`
	Images       []ImageInput   `json:"images,omitempty" jsonschema:"Reference images; each has exactly one server path, URL, or base64 data source"`
	Mask         *ImageInput    `json:"mask,omitempty" jsonschema:"Optional PNG mask; transparent pixels mark regions to edit"`
	Size         string         `json:"size,omitempty"`
	AspectRatio  string         `json:"aspect_ratio,omitempty"`
	Quality      string         `json:"quality,omitempty"`
	OutputFormat string         `json:"output_format,omitempty"`
	N            int            `json:"n,omitempty"`
	Extra        map[string]any `json:"extra,omitempty" jsonschema:"Only extensions explicitly allowed by the selected model profile"`
	RequestID    string         `json:"request_id,omitempty" jsonschema:"Stable caller-generated key to prevent duplicate billing across retries and restarts"`
}

type Asset struct {
	Name   string `json:"name"`
	MIME   string `json:"mime_type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
}

type Task struct {
	ID                   string     `json:"task_id"`
	Model                string     `json:"model"`
	Operation            string     `json:"operation"`
	Status               string     `json:"status"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	Assets               []Asset    `json:"images,omitempty"`
	Error                *TaskError `json:"error,omitempty"`
	DownloadRequiresAuth bool       `json:"download_requires_auth,omitempty"`
}

type TaskError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	ResultUnknown bool   `json:"result_unknown,omitempty"`
}

func (e *TaskError) Error() string         { return e.Code + ": " + e.Message }
func fail(code, message string) *TaskError { return &TaskError{Code: code, Message: message} }
func unknown(message string) *TaskError {
	return &TaskError{Code: "result_unknown", Message: message, ResultUnknown: true}
}
func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "unknown" || status == "expired"
}
func cloneJSON[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func invalid(format string, args ...any) error {
	return fail("invalid_argument", fmt.Sprintf(format, args...))
}
