package handlers

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

var errUnsupportedGreetingFormatting = errors.New("unsupported greeting formatting")

var panelGreetingPlaceholders = []string{
	panelGreetingPlaceholderUser,
	panelGreetingPlaceholderChatTitle,
	panelGreetingPlaceholderChatLinkTitled,
	panelGreetingPlaceholderTimeout,
}

type greetingEntitySpan struct {
	entity api.MessageEntity
	start  int
	end    int
}

type greetingPlaceholderRange struct {
	start int
	end   int
}

func normalizeGreetingTemplateInput(msg *api.Message) (string, error) {
	if msg == nil {
		return "", errUnsupportedGreetingFormatting
	}

	text := msg.Text
	if strings.TrimSpace(text) == "" {
		return "", nil
	}

	if len(msg.Entities) == 0 {
		return db.WrapGatekeeperGreetingMarkdownV2Template(escapeMarkdownV2GreetingTemplateText(strings.TrimSpace(text))), nil
	}

	normalized, err := markdownV2GreetingTemplateFromEntities(text, msg.Entities)
	if err != nil {
		return "", err
	}

	return db.WrapGatekeeperGreetingMarkdownV2Template(normalized), nil
}

func markdownV2GreetingTemplateFromEntities(text string, entities []api.MessageEntity) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}

	runes := []rune(text)
	boundaries := utf16RuneBoundaries(runes)
	spans, err := buildGreetingEntitySpans(boundaries, entities)
	if err != nil {
		return "", err
	}
	if err := validateGreetingPlaceholderUsage(runes, spans); err != nil {
		return "", err
	}

	starts := make(map[int][]greetingEntitySpan)
	ends := make(map[int][]greetingEntitySpan)
	boundarySet := map[int]struct{}{0: {}, len(runes): {}}
	for _, span := range spans {
		starts[span.start] = append(starts[span.start], span)
		ends[span.end] = append(ends[span.end], span)
		boundarySet[span.start] = struct{}{}
		boundarySet[span.end] = struct{}{}
	}

	boundaryPositions := make([]int, 0, len(boundarySet))
	for pos := range boundarySet {
		boundaryPositions = append(boundaryPositions, pos)
	}
	sort.Ints(boundaryPositions)

	for pos, items := range starts {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].end == items[j].end {
				return items[i].entity.Type < items[j].entity.Type
			}
			return items[i].end > items[j].end
		})
		starts[pos] = items
	}
	for pos, items := range ends {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].start == items[j].start {
				return items[i].entity.Type < items[j].entity.Type
			}
			return items[i].start > items[j].start
		})
		ends[pos] = items
	}

	var builder strings.Builder
	active := make([]greetingEntitySpan, 0, len(spans))
	cursor := 0

	for _, boundary := range boundaryPositions {
		if boundary > cursor {
			segment, err := escapeGreetingTemplateSegment(string(runes[cursor:boundary]), active)
			if err != nil {
				return "", err
			}
			builder.WriteString(segment)
			cursor = boundary
		}

		for _, span := range ends[boundary] {
			if len(active) == 0 {
				return "", errUnsupportedGreetingFormatting
			}
			top := active[len(active)-1]
			if top.start != span.start || top.end != span.end || top.entity.Type != span.entity.Type {
				return "", errUnsupportedGreetingFormatting
			}
			closeMarker, err := greetingMarkdownV2CloseMarker(span, string(runes[span.start:span.end]))
			if err != nil {
				return "", err
			}
			builder.WriteString(closeMarker)
			active = active[:len(active)-1]
		}

		for _, span := range starts[boundary] {
			openMarker, err := greetingMarkdownV2OpenMarker(span)
			if err != nil {
				return "", err
			}
			builder.WriteString(openMarker)
			active = append(active, span)
		}
	}

	if len(active) != 0 {
		return "", errUnsupportedGreetingFormatting
	}

	return builder.String(), nil
}

func buildGreetingEntitySpans(boundaries []int, entities []api.MessageEntity) ([]greetingEntitySpan, error) {
	if len(entities) == 0 {
		return nil, nil
	}

	spans := make([]greetingEntitySpan, 0, len(entities))
	for _, entity := range entities {
		start, ok := utf16PositionToRuneIndex(boundaries, entity.Offset)
		if !ok {
			return nil, errUnsupportedGreetingFormatting
		}
		end, ok := utf16PositionToRuneIndex(boundaries, entity.Offset+entity.Length)
		if !ok || end < start {
			return nil, errUnsupportedGreetingFormatting
		}
		if !isGreetingEntityTypeSupported(entity.Type) {
			return nil, errUnsupportedGreetingFormatting
		}
		spans = append(spans, greetingEntitySpan{
			entity: entity,
			start:  start,
			end:    end,
		})
	}

	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end > spans[j].end
		}
		return spans[i].start < spans[j].start
	})

	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[j].start >= spans[i].end {
				break
			}
			if spans[j].end > spans[i].end {
				return nil, errUnsupportedGreetingFormatting
			}
		}
	}

	return spans, nil
}

func validateGreetingPlaceholderUsage(runes []rune, spans []greetingEntitySpan) error {
	placeholders := findGreetingPlaceholderRanges(runes)
	if len(placeholders) == 0 || len(spans) == 0 {
		return nil
	}

	for _, placeholder := range placeholders {
		for _, span := range spans {
			if span.end <= placeholder.start || span.start >= placeholder.end {
				continue
			}
			if span.start > placeholder.start || span.end < placeholder.end {
				return errUnsupportedGreetingFormatting
			}
			if !isGreetingPlaceholderEntitySafe(span.entity.Type) {
				return errUnsupportedGreetingFormatting
			}
		}
	}

	return nil
}

func findGreetingPlaceholderRanges(runes []rune) []greetingPlaceholderRange {
	var ranges []greetingPlaceholderRange
	for _, placeholder := range panelGreetingPlaceholders {
		placeholderRunes := []rune(placeholder)
		if len(placeholderRunes) == 0 || len(runes) < len(placeholderRunes) {
			continue
		}
		for start := 0; start <= len(runes)-len(placeholderRunes); start++ {
			if !slices.Equal(runes[start:start+len(placeholderRunes)], placeholderRunes) {
				continue
			}
			ranges = append(ranges, greetingPlaceholderRange{
				start: start,
				end:   start + len(placeholderRunes),
			})
		}
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	return ranges
}

func escapeGreetingTemplateSegment(segment string, active []greetingEntitySpan) (string, error) {
	if segment == "" {
		return "", nil
	}

	if hasGreetingNonTextPlaceholderContext(active) && containsGreetingPlaceholder(segment) {
		return "", errUnsupportedGreetingFormatting
	}

	if hasGreetingCodeContext(active) {
		return escapeMarkdownV2CodeText(segment), nil
	}

	return escapeMarkdownV2GreetingTemplateText(segment), nil
}

func greetingMarkdownV2OpenMarker(span greetingEntitySpan) (string, error) {
	switch span.entity.Type {
	case "bold":
		return "*", nil
	case "italic":
		return "_", nil
	case "underline":
		return "__", nil
	case "strikethrough":
		return "~", nil
	case "spoiler":
		return "||", nil
	case "code":
		return "`", nil
	case "pre":
		if span.entity.Language != "" {
			return "```" + escapeMarkdownV2CodeLanguage(span.entity.Language) + "\n", nil
		}
		return "```\n", nil
	case "text_link", "text_mention", "url", "mention", "email", "phone_number":
		return "[", nil
	default:
		return "", errUnsupportedGreetingFormatting
	}
}

func greetingMarkdownV2CloseMarker(span greetingEntitySpan, entityText string) (string, error) {
	switch span.entity.Type {
	case "bold":
		return "*", nil
	case "italic":
		return "_", nil
	case "underline":
		return "__", nil
	case "strikethrough":
		return "~", nil
	case "spoiler":
		return "||", nil
	case "code":
		return "`", nil
	case "pre":
		return "\n```", nil
	case "text_link":
		if span.entity.URL == "" {
			return "", errUnsupportedGreetingFormatting
		}
		return "](" + escapeMarkdownV2LinkTarget(span.entity.URL) + ")", nil
	case "text_mention":
		if span.entity.User == nil {
			return "", errUnsupportedGreetingFormatting
		}
		return "](" + escapeMarkdownV2LinkTarget(fmt.Sprintf("tg://user?id=%d", span.entity.User.ID)) + ")", nil
	case "url":
		return "](" + escapeMarkdownV2LinkTarget(entityText) + ")", nil
	case "mention":
		username := strings.TrimPrefix(entityText, "@")
		if username == "" {
			return "", errUnsupportedGreetingFormatting
		}
		return "](" + escapeMarkdownV2LinkTarget("https://t.me/"+username) + ")", nil
	case "email":
		return "](" + escapeMarkdownV2LinkTarget("mailto:"+entityText) + ")", nil
	case "phone_number":
		return "](" + escapeMarkdownV2LinkTarget("tel:"+entityText) + ")", nil
	default:
		return "", errUnsupportedGreetingFormatting
	}
}

func utf16RuneBoundaries(runes []rune) []int {
	boundaries := make([]int, len(runes)+1)
	offset := 0
	for i, r := range runes {
		boundaries[i] = offset
		offset += len(utf16.Encode([]rune{r}))
	}
	boundaries[len(runes)] = offset
	return boundaries
}

func utf16PositionToRuneIndex(boundaries []int, position int) (int, bool) {
	index, found := sort.Find(len(boundaries), func(i int) int {
		return position - boundaries[i]
	})
	return index, found
}

func isGreetingEntityTypeSupported(entityType string) bool {
	switch entityType {
	case "bold", "italic", "underline", "strikethrough", "spoiler", "code", "pre",
		"text_link", "text_mention", "url", "mention", "email", "phone_number":
		return true
	default:
		return false
	}
}

func isGreetingPlaceholderEntitySafe(entityType string) bool {
	switch entityType {
	case "bold", "italic", "underline", "strikethrough", "spoiler":
		return true
	default:
		return false
	}
}

func hasGreetingCodeContext(active []greetingEntitySpan) bool {
	for _, span := range active {
		if span.entity.Type == "code" || span.entity.Type == "pre" {
			return true
		}
	}
	return false
}

func hasGreetingNonTextPlaceholderContext(active []greetingEntitySpan) bool {
	for _, span := range active {
		switch span.entity.Type {
		case "code", "pre", "text_link", "text_mention", "url", "mention", "email", "phone_number":
			return true
		}
	}
	return false
}

func containsGreetingPlaceholder(text string) bool {
	for _, placeholder := range panelGreetingPlaceholders {
		if strings.Contains(text, placeholder) {
			return true
		}
	}
	return false
}

func escapeMarkdownV2GreetingTemplateText(text string) string {
	if text == "" {
		return ""
	}

	var builder strings.Builder
	rest := text
	for len(rest) > 0 {
		nextIndex := len(rest)
		nextPlaceholder := ""
		for _, placeholder := range panelGreetingPlaceholders {
			index := strings.Index(rest, placeholder)
			if index >= 0 && index < nextIndex {
				nextIndex = index
				nextPlaceholder = placeholder
			}
		}
		if nextIndex > 0 {
			builder.WriteString(api.EscapeText(api.ModeMarkdownV2, rest[:nextIndex]))
			rest = rest[nextIndex:]
			continue
		}
		if nextPlaceholder == "" {
			break
		}
		builder.WriteString(nextPlaceholder)
		rest = rest[len(nextPlaceholder):]
	}
	if len(rest) > 0 {
		builder.WriteString(api.EscapeText(api.ModeMarkdownV2, rest))
	}
	return builder.String()
}

func escapeMarkdownV2CodeText(text string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "`", "\\`")
	return replacer.Replace(text)
}

func escapeMarkdownV2CodeLanguage(text string) string {
	replacer := strings.NewReplacer("\\", "", "`", "")
	return replacer.Replace(text)
}

func escapeMarkdownV2LinkTarget(text string) string {
	return api.EscapeText(api.ModeMarkdownV2, text)
}
