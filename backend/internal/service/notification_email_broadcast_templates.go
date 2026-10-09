package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const broadcastContentTemplatePrefix = "notification_email_broadcast_content_template:"

// BroadcastContent stores reusable content only, never recipients or delivery settings.
type BroadcastContent struct {
	Locale       string `json:"locale"`
	MessageTitle string `json:"message_title"`
	MessageHTML  string `json:"message_html"`
	ActionLabel  string `json:"action_label"`
	ActionURL    string `json:"action_url"`
}

type BroadcastContentTemplate struct {
	BroadcastContent
	ID        string `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
}

func normalizeBroadcastContent(content BroadcastContent) (BroadcastContent, error) {
	input, err := normalizeNotificationEmailBroadcastDraftInput(NotificationEmailBroadcastInput{
		Locale: content.Locale, MessageTitle: content.MessageTitle, MessageHTML: content.MessageHTML,
		ActionLabel: content.ActionLabel, ActionURL: content.ActionURL,
	}, false)
	if err != nil {
		return BroadcastContent{}, err
	}
	return BroadcastContent{Locale: input.Locale, MessageTitle: input.MessageTitle, MessageHTML: input.MessageHTML,
		ActionLabel: input.ActionLabel, ActionURL: input.ActionURL}, nil
}

func validBroadcastTemplateID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && id == strings.ToLower(id)
}

func (s *NotificationEmailService) ListBroadcastContentTemplates(ctx context.Context) ([]BroadcastContentTemplate, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("notification email service is not configured")
	}
	values, err := s.settingRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]BroadcastContentTemplate, 0)
	for key, value := range values {
		if !strings.HasPrefix(key, broadcastContentTemplatePrefix) {
			continue
		}
		var item BroadcastContentTemplate
		if err := json.Unmarshal([]byte(value), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	return items, nil
}

func (s *NotificationEmailService) SaveBroadcastContentTemplate(ctx context.Context, id string, item BroadcastContentTemplate) (BroadcastContentTemplate, error) {
	if s == nil || s.settingRepo == nil {
		return BroadcastContentTemplate{}, errors.New("notification email service is not configured")
	}
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len([]rune(item.Name)) > 100 {
		return BroadcastContentTemplate{}, errors.New("template name must contain 1 to 100 characters")
	}
	content, err := normalizeBroadcastContent(item.BroadcastContent)
	if err != nil {
		return BroadcastContentTemplate{}, err
	}
	if id == "" {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return BroadcastContentTemplate{}, err
		}
		id = hex.EncodeToString(token[:])
	} else {
		if !validBroadcastTemplateID(id) {
			return BroadcastContentTemplate{}, errors.New("invalid template id")
		}
		if _, err := s.settingRepo.GetValue(ctx, broadcastContentTemplatePrefix+id); err != nil {
			return BroadcastContentTemplate{}, err
		}
	}
	item.BroadcastContent = content
	item.ID = id
	item.UpdatedAt = s.nowUTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(item)
	if err != nil {
		return BroadcastContentTemplate{}, err
	}
	if err := s.settingRepo.Set(ctx, broadcastContentTemplatePrefix+id, string(raw)); err != nil {
		return BroadcastContentTemplate{}, err
	}
	return item, nil
}

func (s *NotificationEmailService) DeleteBroadcastContentTemplate(ctx context.Context, id string) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("notification email service is not configured")
	}
	if !validBroadcastTemplateID(id) {
		return errors.New("invalid template id")
	}
	err := s.settingRepo.Delete(ctx, broadcastContentTemplatePrefix+id)
	if errors.Is(err, ErrSettingNotFound) {
		return nil
	}
	return err
}

func (s *NotificationEmailService) PreviewBroadcastContent(ctx context.Context, content BroadcastContent) (NotificationEmailPreview, error) {
	if s == nil || s.settingRepo == nil {
		return NotificationEmailPreview{}, errors.New("notification email service is not configured")
	}
	content, err := normalizeBroadcastContent(content)
	if err != nil {
		return NotificationEmailPreview{}, err
	}
	return s.PreviewTemplate(ctx, NotificationEmailPreviewInput{
		Event: NotificationEmailEventAdminBroadcast, Locale: content.Locale,
		Variables: map[string]string{
			"message_title": content.MessageTitle, "message_html": content.MessageHTML,
			"action_label": content.ActionLabel, "action_url": content.ActionURL,
		},
	})
}
