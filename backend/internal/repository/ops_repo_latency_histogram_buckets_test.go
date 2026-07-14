package repository

import (
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildDynamicLatencyHistogramBuckets_UsesLogarithmicRangesForWideSpan(t *testing.T) {
	buckets := buildDynamicLatencyHistogramBuckets(1000, 2000, map[int64]int64{
		0: 3,
		5: 7,
	})

	require.Len(t, buckets, 6)
	require.Equal(t, "1000-1002ms", buckets[0].Range)
	require.EqualValues(t, 3, buckets[0].Count)
	require.Equal(t, "1316-2000ms", buckets[5].Range)
	require.EqualValues(t, 7, buckets[5].Count)
}

func TestLatencyHistogramBoundary_CoversLongTailWithoutGaps(t *testing.T) {
	const span int64 = 1_666_960
	previous := int64(-1)
	for i := int64(0); i <= latencyHistogramMaxBucketCount; i++ {
		boundary := latencyHistogramBoundary(span, i, latencyHistogramMaxBucketCount)
		require.Greater(t, boundary, previous)
		previous = boundary
	}
	require.EqualValues(t, 0, latencyHistogramBoundary(span, 0, latencyHistogramMaxBucketCount))
	require.EqualValues(t, span, latencyHistogramBoundary(span, latencyHistogramMaxBucketCount, latencyHistogramMaxBucketCount))
}

func TestLatencyHistogramBoundary_MatchesSQLLogarithmicBucketFormula(t *testing.T) {
	for _, span := range []int64{65, 1001, 1_666_960} {
		step := max(int64(1), span/10_000)
		for offset := int64(0); offset < span; offset += step {
			bucketIndex := int64(math.Floor(
				math.Log(float64(offset+1)) * float64(latencyHistogramMaxBucketCount) / math.Log(float64(span)),
			))
			bucketIndex = min(bucketIndex, latencyHistogramMaxBucketCount-1)
			lower := latencyHistogramBoundary(span, bucketIndex, latencyHistogramMaxBucketCount)
			upper := latencyHistogramBoundary(span, bucketIndex+1, latencyHistogramMaxBucketCount) - 1
			require.GreaterOrEqual(t, offset, lower, "span=%d offset=%d bucket=%d", span, offset, bucketIndex)
			require.LessOrEqual(t, offset, upper, "span=%d offset=%d bucket=%d", span, offset, bucketIndex)
		}
	}
}

func TestBuildDynamicLatencyHistogramBuckets_UsesOneBucketForSingleValue(t *testing.T) {
	buckets := buildDynamicLatencyHistogramBuckets(1294414, 1294414, map[int64]int64{0: 348})

	require.Equal(t, []*service.OpsLatencyHistogramBucket{
		{Range: "1294414ms", Count: 348},
	}, buckets)
}

func TestBuildDynamicLatencyHistogramBuckets_AvoidsEmptyRangesForNarrowSpan(t *testing.T) {
	buckets := buildDynamicLatencyHistogramBuckets(8, 10, map[int64]int64{0: 1, 1: 2, 2: 3})

	require.Equal(t, []*service.OpsLatencyHistogramBucket{
		{Range: "8ms", Count: 1},
		{Range: "9ms", Count: 2},
		{Range: "10ms", Count: 3},
	}, buckets)
}

func TestBuildDynamicLatencyHistogramBuckets_RejectsInvalidRange(t *testing.T) {
	require.Nil(t, buildDynamicLatencyHistogramBuckets(11, 10, nil))
}
