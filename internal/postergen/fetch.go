package postergen

// fetch.go — fetching a user-pasted background image URL for the
// generator. The discipline mirrors the collection-art fetcher
// (internal/collectionart) exactly, minus its host allowlist — a
// background URL is the user's own art, which can live anywhere:
//   - http/https only, no credentials in the URL;
//   - a browser User-Agent (some image hosts 403 the Go default);
//   - redirects are NOT followed — a redirect is an error, so what
//     gets validated is what the user actually pointed at;
//   - 12 MB cap, enforced on Content-Length and while streaming;
//   - PNG/JPEG magic bytes, then a real decode — anything else is
//     rejected before it can reach the renderer.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG for image.Decode
	_ "image/png"  // register PNG for image.Decode
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxImageBytes matches the collection-art cap (12 MB).
const maxImageBytes = 12 * 1024 * 1024

// browserUA mirrors internal/collectionart's verified UA string.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

var pngMagic = []byte{137, 80, 78, 71, 13, 10, 26, 10}

// FetchImageURL downloads and decodes the image at rawURL under the
// discipline above. Errors name the URL and the failure plainly.
func FetchImageURL(ctx context.Context, rawURL string) (image.Image, error) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("paste an http(s) image address without credentials")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/png,image/jpeg")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s (%v)", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("the image host returned HTTP %d for %s", resp.StatusCode, rawURL)
	}
	if resp.ContentLength > maxImageBytes {
		return nil, fmt.Errorf("the image at %s exceeds 12 MB", rawURL)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("the image at %s exceeds 12 MB", rawURL)
	}
	return DecodeImage(data)
}

// DecodeImage validates (magic bytes) and decodes PNG/JPEG bytes.
func DecodeImage(data []byte) (image.Image, error) {
	isPNG := len(data) >= 8 && bytes.Equal(data[:8], pngMagic)
	isJPG := len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
	if !isPNG && !isJPG {
		return nil, fmt.Errorf("the response is not a PNG or JPEG image")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the image could not be decoded")
	}
	return img, nil
}
