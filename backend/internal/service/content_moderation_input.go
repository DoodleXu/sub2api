package service

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"

	"github.com/tidwall/gjson"
)

func ExtractContentModerationText(protocol string, body []byte) string {
	return ExtractContentModerationInput(protocol, body).Text
}

func ExtractContentModerationInput(protocol string, body []byte) ContentModerationInput {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ContentModerationInput{}
	}
	collector := contentModerationInputCollector{}
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		collectLatestAnthropicUserMessage(gjson.GetBytes(body, "messages"), &collector)
	case ContentModerationProtocolOpenAIChat:
		collectLatestRoleMessage(gjson.GetBytes(body, "messages"), "user", "chat_latest_user", &collector)
	case ContentModerationProtocolOpenAIResponses:
		collectLatestResponsesInput(gjson.GetBytes(body, "input"), &collector)
	case ContentModerationProtocolOpenAIAlphaSearch:
		collectLatestResponsesInput(gjson.GetBytes(body, "input"), &collector)
		collectOpenAIAlphaSearchQueries(gjson.GetBytes(body, "commands.search_query"), &collector)
		if !collector.isEmpty() {
			collector.source = "openai_alpha_search"
		}
	case ContentModerationProtocolGemini:
		collectLatestGeminiContent(gjson.GetBytes(body, "contents"), &collector)
	case ContentModerationProtocolOpenAIImages:
		collector.source = "openai_images"
		collector.AddText(gjson.GetBytes(body, "prompt").String())
		collectContentValue(gjson.GetBytes(body, "images"), &collector.parts, &collector.images)
	default:
		collectLatestResponsesInput(gjson.GetBytes(body, "input"), &collector)
		collectLatestRoleMessage(gjson.GetBytes(body, "messages"), "user", "chat_latest_user", &collector)
		collectLatestGeminiContent(gjson.GetBytes(body, "contents"), &collector)
	}
	rawText, strippedPolicy := stripKnownClassifierPolicyPreamble(strings.Join(collector.parts, "\n"))
	out := ContentModerationInput{
		Text:           normalizeContentModerationText(rawText),
		Images:         normalizeModerationImages(collector.images),
		Source:         collector.source,
		SegmentCount:   collector.segmentCount(),
		StrippedPolicy: strippedPolicy,
	}
	out.Normalize()
	return out
}

func collectOpenAIAlphaSearchQueries(queries gjson.Result, collector *contentModerationInputCollector) {
	if collector == nil || !queries.Exists() {
		return
	}
	collectQuery := func(query gjson.Result) {
		switch {
		case query.Type == gjson.String:
			collector.AddText(query.String())
		case query.IsObject():
			collector.AddText(query.Get("q").String())
		}
	}
	if queries.IsArray() {
		queries.ForEach(func(_, query gjson.Result) bool {
			collectQuery(query)
			return true
		})
		return
	}
	collectQuery(queries)
}

type contentModerationInputCollector struct {
	parts  []string
	images []string
	source string
}

func (c *contentModerationInputCollector) AddText(text string) {
	addModerationText(&c.parts, text)
}

func (c *contentModerationInputCollector) AddImage(image string) {
	addModerationImage(&c.images, image)
}

func (c *contentModerationInputCollector) isEmpty() bool {
	return normalizeContentModerationText(strings.Join(c.parts, "\n")) == "" && len(c.images) == 0
}

func (c *contentModerationInputCollector) segmentCount() int {
	count := 0
	for _, part := range c.parts {
		if normalizeContentModerationText(part) != "" {
			count++
		}
	}
	return count + len(c.images)
}

const (
	codexAmbientOpeningMarker = "You are an expert at upholding safety and compliance standards for Codex ambient suggestions."
	codexAmbientPolicyMarker  = "## 1. Policies to always exclude"
	codexAmbientContentMarker = "# Ambient suggestion candidates"
	codexAmbientOutputMarker  = "# Output Format"
)

var knownClassifierPolicyPreambleHashes = map[string]struct{}{
	// Codex ambient suggestions safety classifier policy preamble.
	"232bba41e5449567489bb74e36a5d34ffc7be1845a0fb7326cd10a5adf33d3a6": {},
}

func stripKnownClassifierPolicyPreamble(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, codexAmbientOpeningMarker) {
		return text, false
	}
	contentIdx := findMarkdownSectionHeading(trimmed, codexAmbientContentMarker, 0)
	if contentIdx < 0 {
		return text, false
	}
	preamble := strings.TrimSpace(trimmed[:contentIdx])
	if !knownClassifierPolicyPreambleHashAllowed(preamble) {
		return text, false
	}
	outputIdx := findCodexAmbientOutputMarker(trimmed, contentIdx)
	if outputIdx < 0 {
		return text, false
	}
	content := strings.TrimSpace(trimmed[contentIdx:outputIdx])
	if content == "" || !strings.Contains(content, "suggestion_id:") {
		return text, false
	}
	return content, true
}

func knownClassifierPolicyPreambleHashAllowed(preamble string) bool {
	if !strings.Contains(preamble, codexAmbientPolicyMarker) {
		return false
	}
	_, ok := knownClassifierPolicyPreambleHashes[knownClassifierPolicyPreambleHash(preamble)]
	return ok
}

func knownClassifierPolicyPreambleHash(preamble string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(preamble)), " ")
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", sum[:])
}

func findMarkdownSectionHeading(text string, heading string, start int) int {
	if start < 0 {
		start = 0
	}
	for offset := start; offset < len(text); {
		idx := strings.Index(text[offset:], heading)
		if idx < 0 {
			return -1
		}
		abs := offset + idx
		lineStart := strings.LastIndexByte(text[:abs], '\n') + 1
		nextLine := strings.IndexByte(text[abs:], '\n')
		lineEnd := len(text)
		if nextLine >= 0 {
			lineEnd = abs + nextLine
		}
		if strings.TrimSpace(text[lineStart:lineEnd]) == heading {
			return lineStart
		}
		offset = abs + len(heading)
	}
	return -1
}

func findCodexAmbientOutputMarker(text string, contentIdx int) int {
	if contentIdx < 0 {
		contentIdx = 0
	}
	inFence := false
	for lineStart := contentIdx; lineStart < len(text); {
		lineEnd := len(text)
		if nextLine := strings.IndexByte(text[lineStart:], '\n'); nextLine >= 0 {
			lineEnd = lineStart + nextLine
		}
		line := strings.TrimSpace(text[lineStart:lineEnd])
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		} else if !inFence && line == codexAmbientOutputMarker {
			return lineStart
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return -1
}

func collectLatestRoleMessage(messages gjson.Result, role string, source string, collector *contentModerationInputCollector) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	for i := len(array) - 1; i >= 0; i-- {
		item := array[i]
		if strings.ToLower(strings.TrimSpace(item.Get("role").String())) != role {
			continue
		}
		var candidate []string
		var candidateImages []string
		collectContentValue(item.Get("content"), &candidate, &candidateImages)
		if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
			continue
		}
		collector.parts = append(collector.parts, candidate...)
		collector.images = append(collector.images, candidateImages...)
		if collector.source == "" {
			collector.source = source
		}
		return
	}
}

func collectLatestAnthropicUserMessage(messages gjson.Result, collector *contentModerationInputCollector) {
	if !messages.IsArray() {
		return
	}
	array := messages.Array()
	for i := len(array) - 1; i >= 0; i-- {
		item := array[i]
		if strings.ToLower(strings.TrimSpace(item.Get("role").String())) != "user" {
			continue
		}
		var candidate []string
		var candidateImages []string
		collectAnthropicUserContentValue(item.Get("content"), &candidate, &candidateImages)
		if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
			continue
		}
		collector.parts = append(collector.parts, candidate...)
		collector.images = append(collector.images, candidateImages...)
		if collector.source == "" {
			collector.source = "anthropic_latest_user"
		}
		return
	}
}

func collectAnthropicUserContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		addModerationText(parts, stripSystemReminderBlocks(value.String()))
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectAnthropicUserContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() {
				addModerationText(parts, stripSystemReminderBlocks(value.Get("text").String()))
			}
			if value.Get("content").Exists() {
				collectAnthropicUserContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
			collectContentValue(value, parts, images)
		}
	}
}

func collectLatestResponsesInput(input gjson.Result, collector *contentModerationInputCollector) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		collectResponsesStringInput(input.String(), collector)
	case input.IsArray():
		array := input.Array()
		for i := len(array) - 1; i >= 0; i-- {
			item := array[i]
			if !isResponsesUserTextItem(item) {
				continue
			}
			collectResponsesItem(item, &collector.parts, &collector.images)
			if !collector.isEmpty() && collector.source == "" {
				collector.source = "responses_latest_user"
			}
			return
		}
	case input.IsObject():
		if isResponsesUserTextItem(input) {
			collectResponsesItem(input, &collector.parts, &collector.images)
			if !collector.isEmpty() && collector.source == "" {
				collector.source = "responses_user_object"
			}
		}
	}
}

func collectResponsesItem(item gjson.Result, parts *[]string, images *[]string) {
	collectContentValue(item.Get("content"), parts, images)
	if item.Get("type").String() == "input_text" || item.Get("text").Exists() {
		collectContentValue(item, parts, images)
	}
}

func collectResponsesStringInput(text string, collector *contentModerationInputCollector) {
	userText, ok := extractLabeledUserText(text)
	if ok {
		collector.AddText(userText)
		if collector.source == "" {
			collector.source = "responses_string_labeled_user"
		}
		return
	}
	collector.AddText(text)
	if collector.source == "" && !collector.isEmpty() {
		collector.source = "responses_string_raw"
	}
}

func isResponsesUserTextItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return responseItemHasModerationText(item)
	}
	if role != "" {
		return false
	}
	return responseItemHasModerationText(item)
}

func responseItemHasModerationText(item gjson.Result) bool {
	var parts []string
	var images []string
	collectResponsesItem(item, &parts, &images)
	return normalizeContentModerationText(strings.Join(parts, "\n")) != "" || len(images) > 0
}

func collectLatestGeminiContent(contents gjson.Result, collector *contentModerationInputCollector) {
	if !contents.IsArray() {
		return
	}
	array := contents.Array()
	for i := len(array) - 1; i >= 0; i-- {
		item := array[i]
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role != "" && role != "user" {
			continue
		}
		var candidate []string
		var candidateImages []string
		if arr := item.Get("parts"); arr.IsArray() {
			arr.ForEach(func(_, part gjson.Result) bool {
				addModerationText(&candidate, part.Get("text").String())
				addGeminiModerationImage(&candidateImages, part)
				return true
			})
		}
		if normalizeContentModerationText(strings.Join(candidate, "\n")) == "" && len(candidateImages) == 0 {
			continue
		}
		collector.parts = append(collector.parts, candidate...)
		collector.images = append(collector.images, candidateImages...)
		if collector.source == "" {
			collector.source = "gemini_latest_user"
		}
		return
	}
}

func collectContentValue(value gjson.Result, parts *[]string, images *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		addModerationText(parts, value.String())
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectContentValue(item, parts, images)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		addModerationImage(images, value.Get("image_url.url").String())
		addModerationImage(images, value.Get("image_url").String())
		addModerationImage(images, value.Get("url").String())
		addModerationImageData(images, value.Get("source.media_type").String(), value.Get("source.data").String())
		addModerationImageData(images, value.Get("source.mediaType").String(), value.Get("source.data").String())
		addModerationImageData(images, value.Get("media_type").String(), value.Get("data").String())
		addModerationImageData(images, value.Get("mime_type").String(), value.Get("data").String())
		addModerationImageData(images, value.Get("mimeType").String(), value.Get("data").String())
		addModerationImage(images, value.Get("source.data").String())
		addModerationImage(images, value.Get("data").String())
		addModerationImage(images, value.Get("base64").String())
		switch typ {
		case "", "text", "input_text", "message":
			if value.Get("text").Exists() {
				addModerationText(parts, value.Get("text").String())
			}
			if value.Get("content").Exists() {
				collectContentValue(value.Get("content"), parts, images)
			}
		case "image_url", "input_image", "image":
		}
	}
}

func extractLabeledUserText(text string) (string, bool) {
	type marker struct {
		start        int
		contentStart int
		role         string
	}
	markers := make([]marker, 0)
	for offset := 0; offset < len(text); {
		lineEnd := len(text)
		if nextLine := strings.IndexByte(text[offset:], '\n'); nextLine >= 0 {
			lineEnd = offset + nextLine
		}
		line := strings.TrimSpace(text[offset:lineEnd])
		role, ok := parseModerationRoleLabel(line)
		if ok {
			contentStart := lineEnd
			if lineEnd < len(text) {
				contentStart = lineEnd + 1
			}
			markers = append(markers, marker{start: offset, contentStart: contentStart, role: role})
		}
		if lineEnd == len(text) {
			break
		}
		offset = lineEnd + 1
	}
	if len(markers) == 0 {
		return "", false
	}
	for i := len(markers) - 1; i >= 0; i-- {
		if markers[i].role != "user" {
			continue
		}
		end := len(text)
		if i+1 < len(markers) {
			end = markers[i+1].start
		}
		userText := strings.TrimSpace(text[markers[i].contentStart:end])
		if normalizeContentModerationText(userText) == "" {
			continue
		}
		return userText, true
	}
	return "", true
}

func parseModerationRoleLabel(line string) (string, bool) {
	line = strings.TrimSpace(strings.Trim(line, "#*-_` "))
	line = strings.TrimSuffix(line, ":")
	line = strings.TrimSpace(line)
	switch strings.ToLower(line) {
	case "system", "system prompt", "instructions", "developer", "developer message":
		return "system", true
	case "user", "user message", "user input", "input":
		return "user", true
	case "assistant", "assistant message", "tool", "tool result", "function result":
		return "other", true
	default:
		return "", false
	}
}

func addGeminiModerationImage(images *[]string, part gjson.Result) {
	if inlineData := part.Get("inline_data"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mime_type").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	if inlineData := part.Get("inlineData"); inlineData.IsObject() {
		mimeType := strings.TrimSpace(inlineData.Get("mimeType").String())
		data := strings.TrimSpace(inlineData.Get("data").String())
		if mimeType != "" && data != "" {
			addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		}
	}
	addModerationImage(images, part.Get("file_data.file_uri").String())
	addModerationImage(images, part.Get("fileData.fileUri").String())
}

func addModerationImageData(images *[]string, mimeType string, data string) {
	mimeType = strings.TrimSpace(mimeType)
	data = strings.TrimSpace(data)
	if mimeType == "" || data == "" {
		return
	}
	addModerationImage(images, fmt.Sprintf("data:%s;base64,%s", mimeType, data))
}

func addModerationImage(images *[]string, image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		*images = append(*images, image)
	}
}

func normalizeModerationImages(images []string) []string {
	out := make([]string, 0, len(images))
	seen := make(map[string]struct{}, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if _, ok := seen[image]; ok {
			continue
		}
		seen[image] = struct{}{}
		out = append(out, image)
	}
	return out
}

func limitContentModerationImages(images []string) []string {
	if len(images) <= maxContentModerationInputImages {
		return images
	}
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(images))))
	if err != nil {
		return images[:maxContentModerationInputImages]
	}
	return []string{images[int(idx.Int64())]}
}

func addModerationText(parts *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	text = stripSystemReminderBlocks(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	*parts = append(*parts, text)
}

func normalizeContentModerationText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func stripSystemReminderBlocks(text string) string {
	for {
		start := strings.Index(text, "<system-reminder>")
		if start < 0 {
			return text
		}
		end := strings.Index(text[start:], "</system-reminder>")
		if end < 0 {
			return strings.TrimSpace(text[:start])
		}
		end = start + end + len("</system-reminder>")
		text = text[:start] + text[end:]
	}
}
