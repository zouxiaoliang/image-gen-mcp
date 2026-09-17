package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

type Provider struct {
	client *http.Client
	images *ImageStore
}

func NewProvider(images *ImageStore) *Provider {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &Provider{images: images, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (p *Provider) Generate(ctx context.Context, m Model, r Request, inputs []Picture, mask *Picture) ([]Picture, error) {
	token := m.credential()
	if token == "" {
		return nil, fail("configuration_error", "upstream token is not configured")
	}
	var req *http.Request
	var err error
	if m.Provider == "gemini" {
		req, err = geminiRequest(ctx, m, r, inputs)
	} else {
		req, err = openAIRequest(ctx, m, r, inputs, mask)
	}
	if err != nil {
		return nil, fail("invalid_request", "could not construct upstream request")
	}
	if m.Provider == "gemini" {
		req.Header.Set("x-goog-api-key", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, unknown("upstream connection interrupted; generation may have been billed; do not retry automatically")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 || resp.StatusCode == 408 {
			return nil, unknown(fmt.Sprintf("upstream HTTP %d; generation outcome is unknown", resp.StatusCode))
		}
		return nil, fail("upstream_rejected", fmt.Sprintf("upstream HTTP %d; check model, parameters and credentials", resp.StatusCode))
	}
	limit := int64(r.N)*(p.images.cfg.MaxImageBytes*4/3+4096) + (1 << 20)
	b, err := boundedRead(resp.Body, limit)
	if err != nil {
		return nil, unknown("could not read upstream result; do not regenerate automatically")
	}
	var blobs []imageBlob
	if m.Provider == "gemini" {
		blobs, err = parseGemini(b)
	} else {
		err = json.Unmarshal(b, &struct {
			Data *[]imageBlob `json:"data"`
		}{&blobs})
	}
	if err != nil || len(blobs) != r.N {
		return nil, unknown("upstream returned an invalid or unexpected image result")
	}
	pics := make([]Picture, 0, len(blobs))
	for _, blob := range blobs {
		var data []byte
		if blob.Base64 != "" {
			data, err = base64.StdEncoding.DecodeString(blob.Base64)
		} else if blob.URL != "" {
			data, err = p.images.Fetch(ctx, blob.URL)
		} else {
			err = fmt.Errorf("empty image")
		}
		if err != nil {
			return nil, unknown("generated image could not be retrieved; do not regenerate automatically")
		}
		pic, e := p.images.Decode(data)
		if e != nil {
			return nil, unknown("generated image is invalid or exceeds configured limits")
		}
		pics = append(pics, pic)
	}
	return pics, nil
}

type imageBlob struct {
	Base64 string `json:"b64_json"`
	URL    string `json:"url"`
}

func openAIRequest(ctx context.Context, m Model, r Request, inputs []Picture, mask *Picture) (*http.Request, error) {
	values := map[string]any{"model": m.Model, "prompt": r.Prompt, "n": r.N}
	for k, v := range map[string]string{"size": r.Size, "quality": r.Quality, "output_format": r.OutputFormat} {
		if v != "" {
			values[k] = v
		}
	}
	for k, v := range r.Extra {
		values[k] = v
	}
	endpoint := strings.TrimRight(m.BaseURL, "/") + "/images/generations"
	var body bytes.Buffer
	contentType := "application/json"
	if len(inputs) == 0 {
		if err := json.NewEncoder(&body).Encode(values); err != nil {
			return nil, err
		}
	} else {
		endpoint = strings.TrimRight(m.BaseURL, "/") + "/images/edits"
		w := multipart.NewWriter(&body)
		for k, v := range values {
			var value string
			if s, ok := v.(string); ok {
				value = s
			} else {
				b, e := json.Marshal(v)
				if e != nil {
					return nil, e
				}
				value = string(b)
			}
			if e := w.WriteField(k, value); e != nil {
				return nil, e
			}
		}
		for i, pic := range inputs {
			field := "image[]"
			if len(inputs) == 1 {
				field = "image"
			}
			if err := writePicture(w, field, strconv.Itoa(i)+"."+strings.TrimPrefix(pic.MIME, "image/"), pic); err != nil {
				return nil, err
			}
		}
		if mask != nil {
			if err := writePicture(w, "mask", "mask.png", *mask); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		contentType = w.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err == nil {
		req.Header.Set("Content-Type", contentType)
	}
	return req, err
}
func writePicture(w *multipart.Writer, field, name string, p Picture) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, name))
	h.Set("Content-Type", p.MIME)
	part, e := w.CreatePart(h)
	if e != nil {
		return e
	}
	_, e = part.Write(p.Data)
	return e
}
func geminiRequest(ctx context.Context, m Model, r Request, inputs []Picture) (*http.Request, error) {
	parts := []any{map[string]any{"text": r.Prompt}}
	for _, pic := range inputs {
		parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": pic.MIME, "data": base64.StdEncoding.EncodeToString(pic.Data)}})
	}
	generation := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	imageConfig := map[string]any{}
	if r.AspectRatio != "" {
		imageConfig["aspectRatio"] = r.AspectRatio
	}
	if r.Size != "" {
		imageConfig["imageSize"] = r.Size
	}
	if len(imageConfig) > 0 {
		generation["imageConfig"] = imageConfig
	}
	for k, v := range r.Extra {
		generation[k] = v
	}
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": parts}}, "generationConfig": generation}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(m.BaseURL, "/") + "/models/" + url.PathEscape(strings.TrimPrefix(m.Model, "models/")) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}
func parseGemini(b []byte) ([]imageBlob, error) {
	type inline struct {
		Data string `json:"data"`
	}
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Inline      *inline `json:"inlineData"`
					SnakeInline *inline `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	var out []imageBlob
	for _, c := range resp.Candidates {
		for _, part := range c.Content.Parts {
			data := part.Inline
			if data == nil {
				data = part.SnakeInline
			}
			if data != nil && data.Data != "" {
				out = append(out, imageBlob{Base64: data.Data})
			}
		}
	}
	return out, nil
}
