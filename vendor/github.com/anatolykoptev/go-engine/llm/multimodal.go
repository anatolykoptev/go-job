package llm

import (
	"context"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	"github.com/anatolykoptev/go-kit/metrics"
)

// ImagePart describes an image for multimodal prompts.
type ImagePart struct {
	URL      string // data:image/... or https://...
	MIMEType string // optional, e.g. "image/jpeg"
}

// CompleteMultimodal sends a vision prompt with images using OpenAI format.
func (c *Client) CompleteMultimodal(ctx context.Context, prompt string, images []ImagePart) (string, error) {
	kitImages := make([]kitllm.ImagePart, len(images))
	for i, img := range images {
		kitImages[i] = kitllm.ImagePart{URL: img.URL, MIMEType: img.MIMEType}
	}

	var raw string
	err := metrics.TrackCall(c.metrics, "llm_calls_total", "llm_errors_total", func() error {
		var e error
		raw, e = c.kit.CompleteMultimodal(ctx, prompt, kitImages)
		return e
	})
	if err != nil {
		return "", err
	}
	return stripFences(raw), nil
}
