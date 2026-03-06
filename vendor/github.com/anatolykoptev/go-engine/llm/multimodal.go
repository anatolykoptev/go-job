package llm

import (
	"context"

	kitllm "github.com/anatolykoptev/go-kit/llm"
)

// ImagePart describes an image for multimodal prompts.
type ImagePart struct {
	URL      string // data:image/... or https://...
	MIMEType string // optional, e.g. "image/jpeg"
}

// CompleteMultimodal sends a vision prompt with images using OpenAI format.
func (c *Client) CompleteMultimodal(ctx context.Context, prompt string, images []ImagePart) (string, error) {
	if c.metrics != nil {
		c.metrics.Incr("llm_calls")
	}

	kitImages := make([]kitllm.ImagePart, len(images))
	for i, img := range images {
		kitImages[i] = kitllm.ImagePart{URL: img.URL, MIMEType: img.MIMEType}
	}

	raw, err := c.kit.CompleteMultimodal(ctx, prompt, kitImages)
	if err != nil {
		if c.metrics != nil {
			c.metrics.Incr("llm_errors")
		}
		return "", err
	}

	return stripFences(raw), nil
}
