package nicorender

import (
	"context"
	"errors"
	"io"

	"imagepadserver/internal/niconico"
)

// CaptureCommentTimelineStream captures comments and writes the NCT2 stream
// incrementally. Writers may optionally implement
// WriteContext(context.Context, []byte) (int, error) to make downstream writes
// cancellable. A plain io.Writer blocked in Write must be unblocked by its
// caller; this function never closes the caller-owned writer.
func CaptureCommentTimelineStream(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, out io.Writer) (TimelineCaptureReport, error) {
	return captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		ctx, snapshot, options, out, nil,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, nil,
	)
}

func captureCommentTimelineStreamWithCaptureLimitsAndStarter(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, out io.Writer, randomSeed *uint32, batchLimit, batchTargetBytes int, freshStarter freshBrowserStarter) (TimelineCaptureReport, error) {
	if out == nil {
		return TimelineCaptureReport{}, errors.New("niconico timeline: stream writer is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	budget := newTimelineStreamBudget(TimelineStreamGoBudgetBytes)
	queue := newTimelineStreamOutputQueue(ctx, out, budget)
	_, report, err := captureCommentTimelineWithCaptureLimitsAndStarterAndStream(
		ctx, snapshot, options, randomSeed, batchLimit, batchTargetBytes, freshStarter, queue,
	)
	if err != nil {
		if queueErr := queue.abortAndWait(); queueErr != nil && !errors.Is(queueErr, err) {
			err = errors.Join(err, queueErr)
		}
		return report, err
	}
	if err := queue.finishAndWait(ctx); err != nil {
		return report, err
	}
	return report, err
}
