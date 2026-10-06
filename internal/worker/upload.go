package worker

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

// uploadBackoffInitial is the first upload/download retry pause (2 s, then
// doubling to 60 s). Tests shrink it.
var uploadBackoffInitial = 2 * time.Second

// putWithRetry re-opens localPath and calls put until it succeeds, the error
// is not retryable (4xx, stale lease) or ctx is done. Network errors and 5xx
// are retried with a 2→4→…→60 s backoff.
func (j *jobExec) putWithRetry(ctx context.Context, localPath string, put func(r io.Reader) error) error {
	return j.putLoop(ctx, localPath, put, false)
}

// putWithRetryStop is putWithRetry that also aborts as soon as a client
// cancellation is known: no further upload and no more retries after that.
func (j *jobExec) putWithRetryStop(ctx context.Context, localPath string, put func(r io.Reader) error) error {
	return j.putLoop(ctx, localPath, put, true)
}

func (j *jobExec) putLoop(ctx context.Context, localPath string, put func(r io.Reader) error, stopOnCancel bool) error {
	backoff := uploadBackoffInitial
	for {
		if stopOnCancel && j.cancelRequested.Load() {
			return errCanceled
		}
		f, err := os.Open(localPath)
		if err != nil {
			return err
		}
		err = put(f)
		_ = f.Close()
		if err == nil || !client.IsRetryable(err) || ctx.Err() != nil {
			// A cancellation (or a lost lease) that killed this attempt must
			// not be reported as a failure of the stage.
			if stopOnCancel && j.cancelRequested.Load() {
				return errCanceled
			}
			return err
		}
		if stopOnCancel && j.cancelRequested.Load() {
			return errCanceled
		}
		j.log.Error("upload failed, retrying", "path", localPath, "err", err)
		if !sleepCtx(ctx, backoff) {
			if stopOnCancel && j.cancelRequested.Load() {
				return errCanceled
			}
			return err
		}
		if stopOnCancel && j.cancelRequested.Load() {
			return errCanceled
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
}
