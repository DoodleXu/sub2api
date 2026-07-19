package repository

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

func TestImageLifecycleRuleCoversPrefix(t *testing.T) {
	days := int32(2)
	shortDays := int32(1)
	rootPrefix := "images/"
	exactPrefix := "images/generated/"
	narrowPrefix := "images/generated/narrow/"
	objectSize := int64(1)
	enabledRule := func() types.LifecycleRule {
		return types.LifecycleRule{
			Status:     types.ExpirationStatusEnabled,
			Expiration: &types.LifecycleExpiration{Days: &days},
		}
	}

	tests := []struct {
		name string
		rule types.LifecycleRule
		want bool
	}{
		{name: "unfiltered rule", rule: enabledRule(), want: true},
		{name: "empty filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{}
			return rule
		}(), want: true},
		{name: "broader prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Prefix: &rootPrefix}
			return rule
		}(), want: true},
		{name: "exact prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Prefix: &exactPrefix}
			return rule
		}(), want: true},
		{name: "narrower prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Prefix: &narrowPrefix}
			return rule
		}()},
		{name: "tag filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{Tag: &types.Tag{}}
			return rule
		}()},
		{name: "size filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{ObjectSizeGreaterThan: &objectSize}
			return rule
		}()},
		{name: "and filter", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Filter = &types.LifecycleRuleFilter{And: &types.LifecycleRuleAndOperator{Prefix: &rootPrefix, Tags: []types.Tag{{}}}}
			return rule
		}()},
		{name: "legacy broader prefix", rule: func() types.LifecycleRule {
			rule := enabledRule()
			//nolint:staticcheck // Exercise compatibility with deprecated Prefix responses from S3-compatible providers.
			rule.Prefix = &rootPrefix
			return rule
		}(), want: true},
		{name: "disabled", rule: func() types.LifecycleRule {
			rule := enabledRule()
			rule.Status = types.ExpirationStatusDisabled
			return rule
		}()},
		{name: "retention too short", rule: types.LifecycleRule{
			Status:     types.ExpirationStatusEnabled,
			Expiration: &types.LifecycleExpiration{Days: &shortDays},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, imageLifecycleRuleCoversPrefix(tt.rule, exactPrefix, 2))
		})
	}
}
