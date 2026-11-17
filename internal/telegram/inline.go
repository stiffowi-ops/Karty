package telegram

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"locationbot/internal/catalog"
)

// Inline search uses Telegram's native suggestions, updated as the user types.
type inlineQuery struct {
	ID     string `json:"id"`
	From   user   `json:"from"`
	Query  string `json:"query"`
	Offset string `json:"offset"`
}

const inlineLocationLimit = 15

func inlineSearchButton() Button {
	return Button{"text": "🔎 Поиск локации", "switch_inline_query_current_chat": ""}
}

type inlineMatch struct {
	location catalog.Location
	rank int
}

// Search only approved location titles, not free-text descriptions. Prefer
// prefix matches, then word starts, then fragments. Ignore case, ё/е and Метро.
func inlineLocationMatches(all []catalog.Location, query string, limit int) []catalog.Location {
	if limit <= 0 { return nil }
	query = catalog.Normalize(query)
	words := strings.Fields(query)
	matches := make([]inlineMatch, 0, len(all))
	for _, l := range all {
		if l.ID == "" || l.Title == "" { continue }
		name := catalog.Normalize(l.Title)
		rank := 0
		if query != "" {
			rank = 3
			switch {
			case strings.HasPrefix(name, query):
				rank = 0
			case inlineAnyWordStarts(name, query):
				rank = 1
			case strings.Contains(name, query):
				rank = 2
			default:
				allWords := true
				for _, word := range words {
					if !strings.Contains(name, word) { allWords = false; break }
				}
				if !allWords { continue }
			}
		}
		matches = append(matches, inlineMatch{location: l, rank: rank})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.rank != b.rank { return a.rank < b.rank }
		ka, kb := stationSortKey(a.location.Title), stationSortKey(b.location.Title)
		if ka != kb { return ka < kb }
		return a.location.ID < b.location.ID
	})
	if len(matches) > limit { matches = matches[:limit] }
	results := make([]catalog.Location, 0, len(matches))
	for _, m := range matches { results = append(results, m.location) }
	return results
}

func inlineAnyWordStarts(name, prefix string) bool {
	for _, word := range strings.Fields(name) {
		if strings.HasPrefix(word, prefix) { return true }
	}
	return false
}

func (b *Bot) answerLocationInlineQuery(ctx context.Context, query *inlineQuery) error {
	if query == nil || query.ID == "" { return nil }
	results := make([]map[string]any, 0, inlineLocationLimit)
	if b.store != nil {
		for _, loc := range inlineLocationMatches(b.store.List(), query.Query, inlineLocationLimit) {
			if !catalog.ValidID(loc.ID) || len("fav:inline:"+loc.ID) > 64 { continue }
			rows := make([][]Button, 0, 3)
			if link := stationMapLink(loc, b.mapURL); link != "" {
				rows = append(rows, []Button{urlButton("🗺 Открыть на карте", link)})
			}
			// Inline results can be shared in groups; favorite actions belong
			// to the user clicking, so the label remains neutral.
			rows = append(rows,
				[]Button{callbackButton("⭐ Сохранить / убрать из избранного", "fav:inline:"+loc.ID)},
				[]Button{inlineSearchButton()},
			)
			description := strings.TrimSpace(loc.Landmarks)
			if description == "" { description = strings.TrimSpace(loc.LineStatus) }
			if description == "" { description = "Открыть описание и точку на карте" }
			results = append(results, map[string]any{
				"type": "article",
				"id": "karty:" + loc.ID,
				"title": loc.Title,
				"description": limitRunes(description, 125),
				"input_message_content": map[string]any{
					"message_text": cardForLocation(loc, b.mapURL),
					"parse_mode": "HTML",
					"link_preview_options": map[string]any{"is_disabled": true},
				},
				"reply_markup": markup(rows...),
			})
		}
	}
	_, err := b.call(ctx, "answerInlineQuery", map[string]any{
		"inline_query_id": query.ID,
		"results": results,
		"cache_time": 0,
		"is_personal": true,
		"next_offset": "",
	})
	return err
}

// Inline-mode callbacks do not necessarily have a Message or chat ID.
// A toast confirms the action; no chat lookup is needed.
func (b *Bot) toggleInlineFavorite(ctx context.Context, callback *callbackQuery) error {
	if callback == nil { return nil }
	id := strings.TrimPrefix(callback.Data, "fav:inline:")
	text := "Не удалось обновить избранное."
	if b.store != nil && b.favorites != nil && catalog.ValidID(id) && callback.From.ID > 0 {
		if _, exists := b.store.Find(id); exists {
			added, err := b.favorites.Toggle(callback.From.ID, id)
			if err == nil {
				if added { text = "⭐ Локация добавлена в избранное" } else { text = "✅ Локация убрана из избранного" }
			}
		} else {
			text = fmt.Sprintf("Локация %s больше не найдена", id)
		}
	}
	_, err := b.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callback.ID,
		"text": text,
		"show_alert": false,
	})
	return err
}
