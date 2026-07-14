package repository

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestImageGenerationTTFTMigrationBackfillsExistingAggregates(t *testing.T) {
	sqlBytes, err := os.ReadFile("../../migrations/177_add_usage_log_image_first_output_ms.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := string(sqlBytes)

	for _, expected := range []string{
		"MIN(bucket_start) AS start_time",
		"UPDATE ops_metrics_hourly hourly",
		"UPDATE ops_metrics_daily daily",
		"LEFT JOIN image_usage_agg",
		"IS DISTINCT FROM backfill.sample_count",
		"ul.image_count > 0",
		"COALESCE(ul.video_count, 0) = 0",
		"ul.image_first_output_ms IS NOT NULL",
	} {
		if !strings.Contains(sqlText, expected) {
			t.Fatalf("migration missing historical backfill clause %q", expected)
		}
	}
}

func TestAggregateHourlyRowsWeightsImageGenerationTTFTByItsOwnSamples(t *testing.T) {
	got := aggregateHourlyRows([]opsHourlyMetricsRow{
		{
			imageGenerationTTFTSampleCount: 1,
			imageGenerationTTFTAvg:         sql.NullFloat64{Float64: 10_000, Valid: true},
		},
		{
			imageGenerationTTFTSampleCount: 3,
			imageGenerationTTFTAvg:         sql.NullFloat64{Float64: 30_000, Valid: true},
		},
		{
			imageGenerationTTFTSampleCount: 0,
			imageGenerationTTFTAvg:         sql.NullFloat64{Float64: 999_999, Valid: true},
		},
	})

	if got.imageGenerationTTFTSampleCount != 4 {
		t.Fatalf("image generation TTFT sample count = %d, want 4", got.imageGenerationTTFTSampleCount)
	}
	if got.imageGenerationTTFTAvgMs == nil {
		t.Fatal("image generation TTFT average is nil")
	}
	if *got.imageGenerationTTFTAvgMs != 25_000 {
		t.Fatalf("image generation TTFT average = %d, want 25000", *got.imageGenerationTTFTAvgMs)
	}
}

func TestWeightedAverageIntSkipsMissingSegments(t *testing.T) {
	value := 42_000
	got := weightedAverageInt([]opsWeightedAverageSegment{
		{weight: 0, value: &value},
		{weight: 2, value: nil},
		{weight: 3, value: &value},
	})

	if got == nil || *got != value {
		t.Fatalf("weighted average = %v, want %d", got, value)
	}
}
