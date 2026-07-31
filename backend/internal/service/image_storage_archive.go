package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	imageArchiveQueueSize       = 32
	imageArchiveWorkerCount     = 2
	imageArchiveMaxPendingBytes = 128 << 20
	imageArchiveTimeout         = 45 * time.Second
)

type imageArchiveJob struct {
	id         string
	payload    []byte
	imageCount int
}

// EnqueueImageArchive schedules a best-effort synchronous image archive. The
// queue is bounded both by jobs and bytes so large base64 responses cannot
// create unbounded heap pressure after the client response has been written.
func (s *ImageStorageSettingService) EnqueueImageArchive(id string, payload []byte, imageCount int) bool {
	if s == nil || len(payload) == 0 || imageCount <= 0 || int64(len(payload)) > imageArchiveMaxPendingBytes {
		return false
	}
	s.archiveQueueOnce.Do(func() {
		s.archiveQueue = make(chan imageArchiveJob, imageArchiveQueueSize)
		for index := 0; index < imageArchiveWorkerCount; index++ {
			s.archiveQueueWG.Add(1)
			go s.imageArchiveWorker()
		}
	})

	job := imageArchiveJob{id: id, payload: payload, imageCount: imageCount}
	s.archiveQueueMu.Lock()
	defer s.archiveQueueMu.Unlock()
	if s.archiveQueueClosed {
		return false
	}
	if s.archivePendingBytes+int64(len(payload)) > imageArchiveMaxPendingBytes {
		return false
	}
	select {
	case s.archiveQueue <- job:
		s.archivePendingBytes += int64(len(payload))
		return true
	default:
		return false
	}
}

func (s *ImageStorageSettingService) imageArchiveWorker() {
	defer s.archiveQueueWG.Done()
	for job := range s.archiveQueue {
		func() {
			defer s.archivePendingBytesDone(len(job.payload))
			uploader, enabled := s.Resolver()()
			if !enabled || uploader == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), imageArchiveTimeout)
			err := uploader.Archive(ctx, job.id, job.payload)
			cancel()
			if err != nil {
				logger.L().Warn("image_archive.standard_upload_failed",
					zap.String("archive_id", job.id),
					zap.Int("image_count", job.imageCount),
					zap.Error(err),
				)
			}
		}()
	}
}

func (s *ImageStorageSettingService) archivePendingBytesDone(size int) {
	s.archiveQueueMu.Lock()
	s.archivePendingBytes -= int64(size)
	if s.archivePendingBytes < 0 {
		s.archivePendingBytes = 0
	}
	s.archiveQueueMu.Unlock()
}

// CloseImageArchiveQueue is intended for tests and controlled shutdowns. A
// running application may leave the queue alive for its process lifetime.
func (s *ImageStorageSettingService) CloseImageArchiveQueue() {
	if s == nil {
		return
	}
	s.archiveQueueOnce.Do(func() {
		s.archiveQueue = make(chan imageArchiveJob, imageArchiveQueueSize)
	})
	s.archiveQueueMu.Lock()
	if !s.archiveQueueClosed {
		s.archiveQueueClosed = true
		close(s.archiveQueue)
	}
	s.archiveQueueMu.Unlock()
	s.archiveQueueWG.Wait()
}
