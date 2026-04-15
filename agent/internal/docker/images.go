// Package docker -- ImagePuller pre-pulls sandbox images from the registry
// and supports periodic re-pulls to pick up new tags.
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ImagePullClient is the subset of the Docker Client interface needed
// for image pull operations.
type ImagePullClient interface {
	PullImage(ctx context.Context, imageRef string) error
}

// ImagePuller manages pulling a specific Docker image with retry support.
// It can pull once (with retries) or start a periodic pull loop.
type ImagePuller struct {
	client      ImagePullClient
	image       string
	logger      *slog.Logger
	baseBackoff time.Duration
	maxRetries  int
}

// NewImagePuller creates an ImagePuller for the given image reference.
func NewImagePuller(client ImagePullClient, image string, logger *slog.Logger) *ImagePuller {
	return &ImagePuller{
		client:      client,
		image:       image,
		logger:      logger,
		baseBackoff: 1 * time.Second,
		maxRetries:  3,
	}
}

// PullOnce pulls the image, retrying up to maxRetries times with
// exponential backoff on failure.
func (p *ImagePuller) PullOnce(ctx context.Context) error {
	var lastErr error

	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := p.client.PullImage(ctx, p.image)
		if err == nil {
			if attempt > 0 {
				p.logger.Info("image pull succeeded after retry",
					"image", p.image,
					"attempt", attempt+1,
				)
			}
			return nil
		}

		lastErr = err
		p.logger.Warn("image pull failed",
			"image", p.image,
			"attempt", attempt+1,
			"error", err,
		)

		// Don't sleep after the last attempt
		if attempt < p.maxRetries {
			backoff := p.baseBackoff * time.Duration(1<<uint(attempt))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return fmt.Errorf("image pull failed after %d attempts: %w", p.maxRetries+1, lastErr)
}

// StartPeriodicPull launches a goroutine that calls PullOnce at the given
// interval. It logs errors but continues pulling. The goroutine exits
// when ctx is cancelled.
func (p *ImagePuller) StartPeriodicPull(ctx context.Context, interval time.Duration) {
	go func() {
		// Pull immediately on start
		if err := p.PullOnce(ctx); err != nil {
			p.logger.Warn("periodic image pull failed", "image", p.image, "error", err)
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := p.PullOnce(ctx); err != nil {
					p.logger.Warn("periodic image pull failed", "image", p.image, "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
