package repository

import (
	"fmt"
	"math"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	latencyHistogramMaxBucketCount      int64 = 6
	latencyHistogramLinearSpanThreshold int64 = 64
)

// buildDynamicLatencyHistogramBuckets partitions the inclusive [minMs, maxMs]
// range into at most six integer buckets. Narrow spans use equal widths; wider
// spans use logarithmic widths so long-tail latency does not collapse nearly all
// requests into the first bucket. The SQL query uses the same transformation.
func buildDynamicLatencyHistogramBuckets(minMs, maxMs int64, counts map[int64]int64) []*service.OpsLatencyHistogramBucket {
	if maxMs < minMs {
		return nil
	}

	span := maxMs - minMs + 1
	bucketCount := min(span, latencyHistogramMaxBucketCount)
	buckets := make([]*service.OpsLatencyHistogramBucket, 0, bucketCount)

	for i := int64(0); i < bucketCount; i++ {
		lowerOffset := latencyHistogramBoundary(span, i, bucketCount)
		upperOffset := latencyHistogramBoundary(span, i+1, bucketCount) - 1
		lowerMs := minMs + lowerOffset
		upperMs := minMs + upperOffset
		label := fmt.Sprintf("%d-%dms", lowerMs, upperMs)
		if lowerMs == upperMs {
			label = fmt.Sprintf("%dms", lowerMs)
		}
		buckets = append(buckets, &service.OpsLatencyHistogramBucket{
			Range: label,
			Count: counts[i],
		})
	}

	return buckets
}

func latencyHistogramBoundary(span, boundaryIndex, bucketCount int64) int64 {
	if boundaryIndex <= 0 {
		return 0
	}
	if boundaryIndex >= bucketCount {
		return span
	}
	if span <= latencyHistogramLinearSpanThreshold {
		return ceilDiv(boundaryIndex*span, bucketCount)
	}

	raw := math.Pow(float64(span), float64(boundaryIndex)/float64(bucketCount)) - 1
	// Move one representable float toward -Inf before Ceil so exact integer
	// boundaries are not rounded into the following millisecond by FP noise.
	boundary := int64(math.Ceil(math.Nextafter(raw, math.Inf(-1))))
	return max(0, min(boundary, span))
}

func ceilDiv(numerator, denominator int64) int64 {
	return (numerator + denominator - 1) / denominator
}
