package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These are elapsed-time reminders for administrators, not client promises.
const supplementReminderDelay = 2 * time.Hour
const supplementEscalationDelay = 4 * time.Hour

func (b *Bot) caseAdminRecipients(l leadApplication) []int64 {
	if l.TakenInWork && b.adminIDs[l.TakenBy] {
		return []int64{l.TakenBy}
	}
	ids := []int64{}
	for id := range b.adminIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
func supplementAdminMarkup(l leadApplication, u leadUpdate) any {
	rows := [][]Button{{adminCaseButton(l.Number)}}
	if u.AcceptedAt == nil && !l.isResolved() {
		rows = append(rows, []Button{callbackButton("✅ Принять дополнение", fmt.Sprintf("adm:case:ack:%d:%d", l.Number, u.ID))})
	} else {
		rows = append(rows, []Button{{"text": "✅ Дополнение принято", "callback_data": fmt.Sprintf("adm:case:view:%d", l.Number), "style": "success"}})
	}
	return markup(rows...)
}
func supplementAdminText(l leadApplication, u leadUpdate, stage string) string {
	title := "🔔 Дополнение к заявке"
	if stage == "reminder" {
		title = "⏰ Дополнение ожидает принятия"
	}
	if stage == "escalation" {
		title = "‼️ Дополнение не принято более 4 часов"
	}
	text := fmt.Sprintf("<b>%s №%04d</b>\nДополнение №%d · %s\n\n<b>Клиент:</b> %s\n<b>Телефон:</b> %s\n", title, l.Number, u.ID, caseTime(u.CreatedAt), html.EscapeString(l.Name), html.EscapeString(l.Phone))
	if l.Username != "" {
		text += "<b>Telegram:</b> @" + html.EscapeString(l.Username) + "\n"
	}
	if len(u.Locations) > 0 {
		text += "\n<b>Добавлены локации</b>\n" + locationLines(u.Locations)
	}
	if u.Comment != "" {
		text += "\n<b>Комментарий</b>\n" + html.EscapeString(u.Comment) + "\n"
	}
	text += fmt.Sprintf("\n<b>Всего локаций в заявке:</b> %d\n%s", len(l.locations()), leadStatus(l))
	return text
}

// Successful deliveries are durably acknowledged per event, stage and recipient.
// The mutex serializes the polling retry and interactive request handlers.
func (b *Bot) deliverCaseNotice(ctx context.Context, l leadApplication, key string, chatID int64, text string, keyboard any) {
	latest, exists := b.leads.byNumber(l.Number)
	if !exists || latest.Hidden || latest.NoticeReceipts[key] {
		return
	}
	if strings.HasPrefix(key, "update:") {
		parts := strings.Split(key, ":")
		if len(parts) != 4 {
			return
		}
		id, e := strconv.Atoi(parts[1])
		if e != nil || id <= 0 || id > len(latest.Updates) || latest.Updates[id-1].AcceptedAt != nil || latest.isResolved() {
			return
		}
	}
	if key == "client:taken" && latest.isResolved() {
		return
	}
	raw, err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}, "reply_markup": keyboard})
	if err != nil {
		log.Printf("Case %d delivery %s: %v", l.Number, key, err)
		return
	}
	var receipt struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(raw, &receipt)
	if err := b.leads.markCaseNotice(l.Number, key, receipt.MessageID); err != nil {
		log.Printf("Case %d delivery receipt %s: %v", l.Number, key, err)
	}
}
func (b *Bot) notifyCaseEvents(ctx context.Context, now time.Time) {
	if b.leads == nil {
		return
	}
	b.leadNotifyMu.Lock()
	defer b.leadNotifyMu.Unlock()
	for _, l := range b.leads.visibleSubmitted() {
		if l.ServiceVersion == 0 {
			continue
		}
		if l.TakenInWork && !l.isResolved() {
			b.deliverCaseNotice(ctx, l, "client:taken", l.UserID, fmt.Sprintf("🛠 <b>Ваше обращение №%04d взято в работу.</b>\n\nСтатус и возможность дополнить обращение доступны в личном кабинете.", l.Number), markup([]Button{caseButton("📂 Открыть обращение", l.Number)}))
		}
		for _, u := range l.Updates {
			if u.AcceptedAt != nil {
				b.deliverCaseNotice(ctx, l, fmt.Sprintf("client:ack:%d", u.ID), l.UserID, fmt.Sprintf("✅ <b>Специалист принял дополнение №%d к обращению №%04d.</b>", u.ID, l.Number), markup([]Button{caseButton("📂 Открыть обращение", l.Number)}))
				continue
			}
			if l.isResolved() {
				continue
			}
			recipients := b.caseAdminRecipients(l)
			for _, id := range recipients {
				b.deliverCaseNotice(ctx, l, updateKey(u.ID, "initial", id), id, supplementAdminText(l, u, "initial"), supplementAdminMarkup(l, u))
			}
			if now.Sub(u.CreatedAt) >= supplementReminderDelay {
				for _, id := range recipients {
					b.deliverCaseNotice(ctx, l, updateKey(u.ID, "reminder", id), id, supplementAdminText(l, u, "reminder"), supplementAdminMarkup(l, u))
				}
			}
			if now.Sub(u.CreatedAt) >= supplementEscalationDelay {
				ids := []int64{}
				for id := range b.adminIDs {
					if id != l.TakenBy {
						ids = append(ids, id)
					}
				}
				sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
				for _, id := range ids {
					b.deliverCaseNotice(ctx, l, updateKey(u.ID, "escalation", id), id, supplementAdminText(l, u, "escalation"), supplementAdminMarkup(l, u))
				}
			}
		}
		if l.isResolved() {
			b.deliverCaseNotice(ctx, l, "client:resolved", l.UserID, fmt.Sprintf("✅ <b>Обращение №%04d решено.</b>\n\n<b>Итог</b>\n%s\n\nИстория сохранена в личном кабинете.", l.Number, html.EscapeString(l.Resolution)), markup([]Button{caseButton("📂 Посмотреть обращение", l.Number), cabinetButton()}))
		}
	}
}
func (b *Bot) refreshSupplementMessages(ctx context.Context, l leadApplication) {
	for key, mid := range l.NoticeMessages {
		if !strings.HasPrefix(key, "update:") || mid <= 0 {
			continue
		}
		p := strings.Split(key, ":")
		if len(p) != 4 {
			continue
		}
		id, e := strconv.Atoi(p[1])
		admin, e2 := strconv.ParseInt(p[3], 10, 64)
		if e != nil || e2 != nil || !b.adminIDs[admin] || id <= 0 || id > len(l.Updates) {
			continue
		}
		u := l.Updates[id-1]
		if u.AcceptedAt == nil {
			continue
		}
		if _, err := b.call(ctx, "editMessageReplyMarkup", map[string]any{"chat_id": admin, "message_id": mid, "reply_markup": supplementAdminMarkup(l, u)}); err != nil && !strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			log.Printf("Case supplement message refresh: %v", err)
		}
	}
}
