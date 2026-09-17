package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type Picture struct {
	Data          []byte
	MIME          string
	Width, Height int
}
type ImageStore struct {
	cfg   Config
	fetch *http.Client
}

func NewImageStore(c Config) *ImageStore { return &ImageStore{cfg: c, fetch: publicImageClient()} }
func publicImageClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("image host has no addresses")
		}
		for _, a := range ips {
			if !a.IP.IsGlobalUnicast() || a.IP.IsPrivate() || a.IP.IsLoopback() || a.IP.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("image URLs must resolve to public addresses")
			}
		}
		d := net.Dialer{Timeout: 15 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return validateImageURL(req.URL)
	}}
}
func validateImageURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return invalid("image URL must be http(s) without credentials")
	}
	return nil
}
func (s *ImageStore) Fetch(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, invalid("invalid image URL")
	}
	if err = validateImageURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, invalid("invalid image URL")
	}
	resp, err := s.fetch.Do(req)
	if err != nil {
		return nil, fail("image_download_failed", "could not download image; private network URLs are not allowed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fail("image_download_failed", fmt.Sprintf("image download HTTP %d", resp.StatusCode))
	}
	return boundedRead(resp.Body, s.cfg.MaxImageBytes)
}
func boundedRead(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, invalid("image or response exceeds configured byte limit")
	}
	return b, nil
}
func (s *ImageStore) Decode(data []byte) (Picture, error) {
	if int64(len(data)) > s.cfg.MaxImageBytes {
		return Picture{}, invalid("image exceeds max_image_bytes")
	}
	info, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Picture{}, invalid("image must be valid PNG, JPEG, or WebP")
	}
	if info.Width < 1 || info.Height < 1 || int64(info.Width)*int64(info.Height) > s.cfg.MaxImagePixels {
		return Picture{}, invalid("image exceeds max_image_pixels")
	}
	_, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != decodedFormat {
		return Picture{}, invalid("image data is incomplete or invalid")
	}
	mime := "image/" + format
	if format == "jpeg" {
		mime = "image/jpeg"
	}
	return Picture{Data: data, MIME: mime, Width: info.Width, Height: info.Height}, nil
}
func (s *ImageStore) Resolve(ctx context.Context, in ImageInput) (Picture, error) {
	if err := validateInput(in); err != nil {
		return Picture{}, err
	}
	var b []byte
	var err error
	switch {
	case in.Data != "":
		if len(in.Data) > base64.StdEncoding.EncodedLen(int(s.cfg.MaxImageBytes))+4 {
			return Picture{}, invalid("base64 image too large")
		}
		b, err = base64.StdEncoding.DecodeString(in.Data)
	case in.URL != "":
		b, err = s.Fetch(ctx, in.URL)
	default:
		b, err = s.readAllowed(in.Path)
	}
	if err != nil {
		return Picture{}, invalid("cannot read input image: %v", err)
	}
	return s.Decode(b)
}
func (s *ImageStore) readAllowed(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, invalid("image path must be absolute")
	}
	for _, dir := range s.cfg.AllowedInputDirs {
		abs, e := filepath.Abs(dir)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(abs, path)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		root, e := os.OpenRoot(abs)
		if e != nil {
			continue
		}
		f, e := root.Open(rel)
		root.Close()
		if e != nil {
			continue
		}
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, invalid("input path must be a regular file")
		}
		b, e := boundedRead(f, s.cfg.MaxImageBytes)
		f.Close()
		return b, e
	}
	return nil, invalid("input path is outside allowed_input_dirs or cannot be read")
}
func validateMask(original, mask Picture) error {
	if mask.MIME != "image/png" || original.MIME != "image/png" || mask.Width != original.Width || mask.Height != original.Height {
		return invalid("mask and first input must be PNG images with identical dimensions")
	}
	im, _, err := image.Decode(bytes.NewReader(mask.Data))
	if err != nil {
		return err
	}
	for y := 0; y < mask.Height; y++ {
		for x := 0; x < mask.Width; x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a == 0 {
				return nil
			}
		}
	}
	return invalid("mask must contain transparent pixels indicating the edit area")
}
func (s *ImageStore) Save(taskID string, pics []Picture) ([]Asset, error) {
	dir := filepath.Join(s.cfg.OutputDir, "images", taskID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	assets := make([]Asset, 0, len(pics))
	for i, p := range pics {
		ext := strings.TrimPrefix(p.MIME, "image/")
		name := fmt.Sprintf("%d.%s", i+1, ext)
		if err := atomicWrite(filepath.Join(dir, name), p.Data); err != nil {
			return nil, err
		}
		preview, err := s.Preview(p)
		if err != nil {
			return nil, err
		}
		if err = atomicWrite(filepath.Join(dir, name+".preview.png"), preview); err != nil {
			return nil, err
		}
		assets = append(assets, Asset{Name: name, MIME: p.MIME, Width: p.Width, Height: p.Height, Bytes: len(p.Data)})
	}
	return assets, nil
}
func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".image-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func (s *ImageStore) Preview(p Picture) ([]byte, error) {
	im, _, err := image.Decode(bytes.NewReader(p.Data))
	if err != nil {
		return nil, err
	}
	w, h := p.Width, p.Height
	if w > s.cfg.PreviewSize || h > s.cfg.PreviewSize {
		if w >= h {
			h = max(1, h*s.cfg.PreviewSize/w)
			w = s.cfg.PreviewSize
		} else {
			w = max(1, w*s.cfg.PreviewSize/h)
			h = s.cfg.PreviewSize
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), im, im.Bounds(), draw.Over, nil)
		im = dst
	}
	var b bytes.Buffer
	err = png.Encode(&b, im)
	return b.Bytes(), err
}
