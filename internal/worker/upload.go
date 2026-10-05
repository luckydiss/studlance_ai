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
	backoff := uploadBackoffInitial
	for {
		f, err := os.Open(localPath)
		if err != nil {
			return err
		}
		err = put(f)
		_ = f.Close()
		if err == nil || !client.IsRetryable(err) || ctx.Err() != nil {
			return err
		}
		j.log.Error("upload failed, retrying", "path", localPath, "err", err)
		if !sleepCtx(ctx, backoff) {
			return err
		}
		if backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
	}
}
