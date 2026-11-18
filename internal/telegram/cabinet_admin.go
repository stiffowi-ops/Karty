package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

func (b *Bot) clearResolutionSession(adminID int64) {
	if !b.isAdmin(adminID) {
		return
	}
	s := b.adminSession(adminID)
	s.ResolveLead = 0
	s.ResolveSummary = ""
	s.ResolveVersion = 0
	s.ResolveToken = ""
}
func adminCaseButton(n int64) Button {
	return callbackButton("📂 Открыть заявку", fmt.Sprintf("adm:case:view:%d", n))
}
func (b *Bot) showAdminCase(ctx context.Context, chatID, number int64, page int) error {
	l, ok := b.leads.byNumber(number)
	if !ok || l.Hidden {
		return b.Send(ctx, chatID, "Обращение недоступно.", markup([]Button{callbackButton("⬅️ К заявкам", "adm:leads")}))
	}
	text := adminLeadText(l)
	rows := adminLeadMarkup(l)["inline_keyboard"].([][]Button)
	// The detail view is the destination, so its Open button is redundant.
	if len(rows) > 0 {
		rows = rows[1:]
	}
	pending := []leadUpdate{}
	for _, u := range l.Updates {
		if u.AcceptedAt == nil {
			pending = append(pending, u)
		}
	}
	pages := max(1, (len(pending)+4)/5)
	page = max(0, min(page, pages-1))
	for _, u := range pending[page*5 : min((page+1)*5, len(pending))] {
		rows = append(rows, []Button{callbackButton(fmt.Sprintf("✅ Принять дополнение №%d", u.ID), fmt.Sprintf("adm:case:ack:%d:%d", number, u.ID))})
	}
	nav := []Button{}
	if page > 0 {
		nav = append(nav, callbackButton("◀️ Дополнения", fmt.Sprintf("adm:case:view:%d:%d", number, page-1)))
	}
	if page+1 < pages {
		nav = append(nav, callbackButton("Дополнения ▶️", fmt.Sprintf("adm:case:view:%d:%d", number, page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []Button{callbackButton("🕓 Полная история", fmt.Sprintf("adm:case:timeline:%d:0", number))}, []Button{callbackButton("⬅️ К заявкам", "adm:leads")})
	return b.Send(ctx, chatID, text, markup(rows...))
}
func (b *Bot) handleCaseAdminCallback(ctx context.Context, from user, data string) error {
	if !b.isAdmin(from.ID) || b.leads == nil {
		return nil
	}
	p := strings.Split(data, ":")
	if len(p) < 4 {
		return nil
	}
	if p[2] == "list" {
		page, err := strconv.Atoi(p[3])
		if err != nil {
			return nil
		}
		b.clearResolutionSession(from.ID)
		return b.showAdminLeadsPage(ctx, from.ID, page)
	}
	number, err := strconv.ParseInt(p[3], 10, 64)
	if err != nil || number <= 0 {
		return nil
	}
	l, ok := b.leads.byNumber(number)
	if !ok || l.Hidden {
		return b.showAdminLeads(ctx, from.ID)
	}
	switch p[2] {
	case "view":
		b.clearResolutionSession(from.ID)
		page := 0
		if len(p) > 4 {
			page, _ = strconv.Atoi(p[4])
		}
		return b.showAdminCase(ctx, from.ID, number, page)
	case "timeline":
		b.clearResolutionSession(from.ID)
		page := 0
		if len(p) > 4 {
			page, _ = strconv.Atoi(p[4])
		}
		return b.showCaseTimeline(ctx, from.ID, l, page, true)
	case "ack":
		b.clearResolutionSession(from.ID)
		if len(p) != 5 {
			return nil
		}
		id, err := strconv.Atoi(p[4])
		if err != nil {
			return nil
		}
		updated, _, err := b.leads.acceptUpdate(number, id, from.ID)
		if err != nil {
			return b.Send(ctx, from.ID, html.EscapeString(err.Error()), markup([]Button{adminCaseButton(number)}))
		}
		b.refreshLeadAdminMessages(ctx, updated)
		b.refreshSupplementMessages(ctx, updated)
		b.notifyCaseEvents(ctx, time.Now().UTC())
		return b.showAdminCase(ctx, from.ID, number, 0)
	case "resolve":
		b.clearResolutionSession(from.ID)
		if l.isResolved() {
			return b.showAdminCase(ctx, from.ID, number, 0)
		}
		if !l.TakenInWork {
			return b.Send(ctx, from.ID, "Сначала возьмите обращение в работу.", adminLeadMarkup(l))
		}
		for _, u := range l.Updates {
			if u.AcceptedAt == nil {
				return b.Send(ctx, from.ID, "Перед завершением изучите и примите все дополнения клиента.", markup([]Button{adminCaseButton(number)}))
			}
		}
		token, err := newSupplement(number)
		if err != nil {
			return err
		}
		s := b.adminSession(from.ID)
		s.ResolveLead = number
		s.ResolveVersion = len(l.Updates)
		s.ResolveToken = token.Token
		return b.Send(ctx, from.ID, fmt.Sprintf("✅ <b>Завершение обращения №%04d</b>\n\nНапишите итог для клиента одним сообщением: от 3 до 1000 символов. Перед отправкой Вы сможете его проверить.", number), markup([]Button{adminCaseButton(number)}))
	case "finish":
		if len(p) != 5 {
			return nil
		}
		s := b.adminSession(from.ID)
		if s.ResolveLead != number || s.ResolveToken != p[4] || s.ResolveSummary == "" {
			return b.showAdminCase(ctx, from.ID, number, 0)
		}
		updated, _, err := b.leads.resolveCase(number, from.ID, s.ResolveVersion, s.ResolveSummary)
		b.clearResolutionSession(from.ID)
		if err != nil {
			return b.Send(ctx, from.ID, html.EscapeString(err.Error()), markup([]Button{adminCaseButton(number)}))
		}
		b.refreshLeadAdminMessages(ctx, updated)
		b.notifyCaseEvents(ctx, time.Now().UTC())
		return b.showAdminCase(ctx, from.ID, number, 0)
	}
	return nil
}
func (b *Bot) handleResolutionText(ctx context.Context, chatID int64, from user, input string) (bool, error) {
	if !b.isAdmin(from.ID) || chatID != from.ID {
		return false, nil
	}
	s := b.adminSession(from.ID)
	if s.ResolveLead == 0 || strings.HasPrefix(input, "/") {
		return false, nil
	}
	input = strings.TrimSpace(input)
	if len([]rune(input)) < 3 || len([]rune(input)) > 1000 {
		return true, b.Send(ctx, chatID, "Укажите итог от 3 до 1000 символов.", markup([]Button{adminCaseButton(s.ResolveLead)}))
	}
	l, ok := b.leads.byNumber(s.ResolveLead)
	if !ok || l.Hidden || l.isResolved() {
		b.clearResolutionSession(from.ID)
		return true, b.showAdminLeads(ctx, chatID)
	}
	s.ResolveSummary = input
	return true, b.Send(ctx, chatID, fmt.Sprintf("✅ <b>Завершить обращение №%04d?</b>\n\nКлиент получит итог:\n\n%s", s.ResolveLead, html.EscapeString(input)), markup(
		[]Button{callbackButton("✅ Решить и отправить итог", fmt.Sprintf("adm:case:finish:%d:%s", s.ResolveLead, s.ResolveToken))},
		[]Button{adminCaseButton(s.ResolveLead)},
	))
}
