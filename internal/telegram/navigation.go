package telegram

import (
	"context"
	"log"
	"strings"
)

// A callback belongs to a specific bot message, so menus can replace that
// message instead of adding another bubble to the private chat.
// We do not delete user messages, including messages sent via Inline Mode.
type navigationKey struct{}

type navigationTarget struct {
	chatID    int64
	messageID int
}

func navigationFromContext(ctx context.Context, chatID int64) (int, bool) {
	target, ok := ctx.Value(navigationKey{}).(navigationTarget)
	if !ok || target.messageID <= 0 || target.chatID != chatID {
		return 0, false
	}
	return target.messageID, true
}

func withNavigation(ctx context.Context, chatID int64, messageID int) context.Context {
	if messageID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, navigationKey{}, navigationTarget{
		chatID: chatID, messageID: messageID,
	})
}

func (b *Bot) editNavigationMessage(ctx context.Context, chatID int64, messageID int, text string, keyboard any) error {
	_, err := b.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID,
		"message_id": messageID,
		"text": text,
		"parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
		"reply_markup": keyboard,
	})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	return err
}

// Telegram cannot edit old, inaccessible or non-bot messages. In those cases
// a new message is preferable to a broken button.
func (b *Bot) sendOrEdit(ctx context.Context, chatID int64, text string, keyboard any) error {
	if messageID, ok := navigationFromContext(ctx, chatID); ok {
		if err := b.editNavigationMessage(ctx, chatID, messageID, text, keyboard); err == nil {
			return nil
		} else {
			log.Printf("Inline menu edit failed for message %d: %v; sending new screen", messageID, err)
		}
	}
	_, err := b.call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text": text,
		"parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
		"reply_markup": keyboard,
	})
	return err
}
