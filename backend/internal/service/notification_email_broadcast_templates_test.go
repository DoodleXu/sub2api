package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBroadcastContentTemplateLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	items, err := svc.ListBroadcastContentTemplates(ctx)
	require.NoError(t, err)
	require.Empty(t, items)
	input := BroadcastContentTemplate{Name: "  活动  ", BroadcastContent: BroadcastContent{
		Locale: "zh", MessageTitle: "充值活动", MessageHTML: "<p>满 30 元赠 2%</p>", ActionLabel: "充值", ActionURL: "https://ai.clol.site",
	}}
	saved, err := svc.SaveBroadcastContentTemplate(ctx, "", input)
	require.NoError(t, err)
	require.Equal(t, "活动", saved.Name)
	require.True(t, validBroadcastTemplateID(saved.ID))
	// A fresh service reads persisted templates; the draft remains independent.
	svc = NewNotificationEmailService(repo, nil)
	items, err = svc.ListBroadcastContentTemplates(ctx)
	require.NoError(t, err)
	require.Equal(t, []BroadcastContentTemplate{saved}, items)
	_, err = svc.GetBroadcastDraft(ctx)
	require.ErrorIs(t, err, ErrSettingNotFound)
	input.Name = "新活动"
	updated, err := svc.SaveBroadcastContentTemplate(ctx, saved.ID, input)
	require.NoError(t, err)
	require.Equal(t, saved.ID, updated.ID)
	require.Equal(t, "新活动", updated.Name)
	input.ActionURL = "javascript:alert(1)"
	_, err = svc.SaveBroadcastContentTemplate(ctx, "", input)
	require.Error(t, err)
	_, err = svc.SaveBroadcastContentTemplate(ctx, "draft", input)
	require.Error(t, err)
	require.Error(t, svc.DeleteBroadcastContentTemplate(ctx, "../draft"))
	require.NoError(t, svc.DeleteBroadcastContentTemplate(ctx, saved.ID))
	items, err = svc.ListBroadcastContentTemplates(ctx)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestBroadcastContentPreviewUsesDeliveryEnvelopeWithoutSending(t *testing.T) {
	ctx := context.Background()
	repo := newNotificationEmailMemorySettingRepo()
	svc := NewNotificationEmailService(repo, nil)
	_, err := svc.UpdateTemplate(ctx, NotificationEmailEventAdminBroadcast, "zh", "站点：{{message_title}}", "<html><body>{{message_html}}{{action_html}}<footer>自定义外框</footer></body></html>")
	require.NoError(t, err)
	preview, err := svc.PreviewBroadcastContent(ctx, BroadcastContent{Locale: "zh", MessageTitle: "活动", MessageHTML: "<p>活动内容</p>", ActionLabel: "充值", ActionURL: "https://ai.clol.site"})
	require.NoError(t, err)
	require.Equal(t, "站点：活动", preview.Subject)
	require.Contains(t, preview.HTML, "<p>活动内容</p>")
	require.Contains(t, preview.HTML, "自定义外框")
	require.Contains(t, preview.HTML, "https://ai.clol.site")
	_, err = svc.PreviewBroadcastContent(ctx, BroadcastContent{MessageTitle: "活动", MessageHTML: "<p>内容</p>", ActionURL: "javascript:alert(1)"})
	require.Error(t, err)
}
