package telegram

import (
 "context"
 "encoding/json"
 "fmt"
 "html"
 "strconv"
 "strings"
)

// /channelpanel @public_channel (or -100... for a private channel).
// Only configured admins can publish the public launcher. The whole
// personal catalog is then delivered as callback-only ephemeral messages.
func (b *Bot) channelPanelCommand(ctx context.Context, m *message) error {
 if m == nil || m.Chat.Type != "private" || !b.isAdmin(m.From.ID) {
  return nil
 }
 fields := strings.Fields(strings.TrimSpace(m.Text))
 if len(fields) != 2 {
  return b.Send(ctx,m.Chat.ID,"Для публикации помощника в канале отправьте <code>/channelpanel @имя_канала</code>. Для приватного канала укажите его числовой ID вида <code>-100...</code>. Добавьте бота администратором канала с правами публикации и закрепления сообщений.",nil)
 }
 target := fields[1]
 if !strings.HasPrefix(target,"@") {
  if id,err:=strconv.ParseInt(target,10,64);err!=nil||id>=0 {
   return b.Send(ctx,m.Chat.ID,"Укажите @имя публичного канала или числовой ID приватного канала.",nil)
  }
 } else if len(target)<6 || len(target)>33 {
  return b.Send(ctx,m.Chat.ID,"Некорректное имя канала.",nil)
 } else {
  for _,r:=range target[1:] {
   if !(r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='_') {
    return b.Send(ctx,m.Chat.ID,"Некорректное имя канала.",nil)
   }
  }
 }
 raw,err:=b.call(ctx,"getChat",map[string]any{"chat_id":target})
 if err!=nil {return b.Send(ctx,m.Chat.ID,"Канал не найден или у бота нет доступа. Проверьте ID и права администратора.",nil)}
 var info struct {ID int64 `json:"id"`; Type string `json:"type"`; Title string `json:"title"`}
 if json.Unmarshal(raw,&info)!=nil || info.Type!="channel" || info.ID>=0 {
  return b.Send(ctx,m.Chat.ID,"Указанный чат не является Telegram-каналом. Для группы используйте /panel в самой группе.",nil)
 }
 // Check permissions before touching existing messages.
 memberRaw,err:=b.call(ctx,"getChatMember",map[string]any{"chat_id":info.ID,"user_id":b.botID})
 if err!=nil || b.botID<=0 {return b.Send(ctx,m.Chat.ID,"Не удалось проверить права бота. Убедитесь, что он добавлен в канал администратором.",nil)}
 var member struct {Status string `json:"status"`; CanPostMessages bool `json:"can_post_messages"`; CanEditMessages bool `json:"can_edit_messages"`}
 if json.Unmarshal(memberRaw,&member)!=nil || !(member.Status=="creator" || member.Status=="administrator"&&member.CanPostMessages&&member.CanEditMessages) {
  return b.Send(ctx,m.Chat.ID,"Для панели боту нужны права администратора: публиковать и редактировать/закреплять сообщения канала.",nil)
 }
 if err:=b.publishAndPinGroupPanel(ctx,info.ID);err!=nil {
  return b.Send(ctx,m.Chat.ID,"Не получилось опубликовать панель: "+html.EscapeString(err.Error()),nil)
 }
 return b.Send(ctx,m.Chat.ID,fmt.Sprintf("✅ Помощник опубликован и закреплён в канале <b>%s</b>. Подписчик открывает личное меню кнопкой «📍 Позвать помощника» без перехода в другой чат.",html.EscapeString(info.Title)),nil)
}
