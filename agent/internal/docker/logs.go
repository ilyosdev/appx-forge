// Package docker -- LogReader wraps Docker's container log retrieval
// with tail and follow mode support.
package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// LogClient is the subset of the Docker Client interface needed
// for log retrieval operations.
type LogClient interface {
	GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error)
}

// LogReader reads container logs via the Docker client.
type LogReader struct {
	client LogClient
	logger *slog.Logger
}

// NewLogReader creates a LogReader that delegates to the given client.
func NewLogReader(client LogClient, logger *slog.Logger) *LogReader {
	return &LogReader{
		client: client,
		logger: logger,
	}
}

// ReadLogs retrieves container logs with the specified tail count and
// follow mode. The caller is responsible for closing the returned reader.
func (lr *LogReader) ReadLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	rc, err := lr.client.GetLogs(ctx, containerID, tail, follow)
	if err != nil {
		return nil, fmt.Errorf("read logs for container %s: %w", containerID, err)
	}

	lr.logger.Debug("reading container logs",
		"container_id", containerID,
		"tail", tail,
		"follow", follow,
	)

	return rc, nil
}
