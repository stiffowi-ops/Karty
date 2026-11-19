package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"locationbot/internal/analytics"
	"locationbot/internal/catalog"
)

// All client screens triggered in a group use this separate context. Never
// reuse the ordinary send/edit code here: that would edit a shared message
// or publish a customer's personal state to the entire group.
type groupScreenKey struct{}
type groupScreen struct {
	chatID int64
	userID int64
	callbackID string
	ephemeralID int
	replyEphemeralID int
}

func withGroupScreen(ctx context.Context, target groupScreen) context.Context {
	return context.WithValue(ctx, groupScreenKey{}, target)
}
func groupScreenFromContext(ctx context.Context) (groupScreen, bool) {
	v, ok := ctx.Value(groupScreenKey{}).(groupScreen)
	return v, ok && v.userID > 0
}
func isGroupChat(kind string) bool {
	return kind == "group" || kind == "supergroup"
}

func (b *Bot) sendGroupScreen(ctx context.Context, text string, keyboard any) error {
	target, ok := groupScreenFromContext(ctx)
	if !ok { return errors.New("missing private group recipient") }
	if keyboard == nil { keyboard = markup([]Button{callbackButton("🏠 Меню","grp:menu")}) }
	if target.ephemeralID > 0 && target.chatID != 0 {
		_, err := b.call(ctx, "editEphemeralMessageText", map[string]any{
			"chat_id": target.chatID,
			"receiver_user_id": target.userID,
			"ephemeral_message_id": target.ephemeralID,
			"text": text, "parse_mode": "HTML",
			"link_preview_options": map[string]any{"is_disabled": true},
			"reply_markup": keyboard,
		})
		if err == nil || strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
			return nil
		}
		log.Printf("Private group screen expired or cannot be edited: %v", err)
	}
	params := map[string]any{}
	body := map[string]any{
		"text": text, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
		"reply_markup": keyboard,
	}
	if target.chatID != 0 { body["chat_id"] = target.chatID }
	switch {
	case target.callbackID != "":
		params["callback_query_id"] = target.callbackID
		// For a shared message, show a private overlay instead of replacing the
		// public card. This flag must be false for an ephemeral callback.
		if target.ephemeralID == 0 { params["replace_callback_query_message"] = true }
	case target.replyEphemeralID > 0 && target.chatID != 0:
		params["receiver_user_id"] = target.userID
		body["reply_parameters"] = map[string]any{"ephemeral_message_id": target.replyEphemeralID}
	case target.chatID != 0:
		// This is valid for normal group commands only if the bot is a group
		// administrator. Failure must NOT fall back to a public send.
		params["receiver_user_id"] = target.userID
	default:
		return errors.New("cannot address private group response")
	}
	body["ephemeral_message_parameters"] = params
	_, err := b.call(ctx, "sendMessage", body)
	return err
}

func (b *Bot) groupMenu(ctx context.Context) error {
	rows := [][]Button{
		{callbackButton("📍 Каталог локаций","grp:list:0"),callbackButton("🔎 Поиск","grp:search")},
		{callbackButton("⭐ Моё избранное","grp:favorites"),callbackButton("⚖️ Сравнение","grp:compare")},
		{callbackButton("🗺 Общая карта","grp:map"),callbackButton("📂 Мои заявки","grp:requests")},
		{callbackButton("📝 Оставить заявку","grp:lead:menu"),callbackButton("📍 Рядом со мной","grp:near")},
	}
	return b.Send(ctx, 0, "👋 <b>Ваше персональное меню локаций</b>\n\nВы видите этот экран только для себя. Список, избранное и сравнение не показываются остальным участникам группы.\n\nДля заявок и передачи телефона откроется личный чат с ботом.", markup(rows...))
}
func (b *Bot) groupCatalog(ctx context.Context, page int) error {
	items := sortedStations(b.store.List())
	last := 0
	if len(items) > 0 { last=(len(items)-1)/10 }
	if page < 0 { page=0 }
	if page > last { page=last }
	rows := [][]Button{}
	for i:=page*10;i<min(len(items),(page+1)*10);i+=2 {
		row:=[]Button{callbackButton(buttonLabel(items[i].Title),"grp:loc:"+items[i].ID+":"+strconv.Itoa(page))}
		if i+1<min(len(items),(page+1)*10) { row=append(row,callbackButton(buttonLabel(items[i+1].Title),"grp:loc:"+items[i+1].ID+":"+strconv.Itoa(page))) }
		rows=append(rows,row)
	}
	navigation:=[]Button{}
	if page>0 { navigation=append(navigation,callbackButton("◀️",fmt.Sprintf("grp:list:%d",page-1))) }
	if page<last { navigation=append(navigation,callbackButton("▶️",fmt.Sprintf("grp:list:%d",page+1))) }
	if len(navigation)>0 { rows=append(rows,navigation) }
	rows=append(rows,[]Button{callbackButton("🔎 Поиск","grp:search"),callbackButton("⭐ Избранное","grp:favorites")})
	rows=append(rows,[]Button{callbackButton("🏠 Меню","grp:menu")})
	return b.Send(ctx,0,fmt.Sprintf("📍 <b>Каталог локаций</b>\n\nВыберите точку. Это персональный экран, другие участники не увидят Ваш выбор.\n\nСтраница %d из %d",page+1,last+1),markup(rows...))
}
func (b *Bot) groupLocation(ctx context.Context, id string, page int) error {
	target, ok := groupScreenFromContext(ctx)
	if !ok { return errors.New("missing client context") }
	loc, found := b.store.Find(id)
	if !found { return b.groupCatalog(ctx,page) }
	saved := b.favorites!=nil && b.favorites.Contains(target.userID,id)
	label:="⭐ В избранное"
	if saved { label="✅ Убрать из избранного" }
	buttons:=[][]Button{
		{callbackButton(label,"grp:favorite:"+id+":"+strconv.Itoa(page))},
		{callbackButton("📝 Оставить заявку по точке","grp:lead:"+id)},
		{callbackButton("⬅️ К каталогу",fmt.Sprintf("grp:list:%d",page)),callbackButton("⭐ Избранное","grp:favorites")},
		{callbackButton("🏠 Меню","grp:menu")},
	}
	return b.Send(ctx,0,cardForLocation(loc,b.mapURL),markup(buttons...))
}
func (b *Bot) groupFavorites(ctx context.Context) error {
	target, ok := groupScreenFromContext(ctx)
	if !ok { return errors.New("missing client context") }
	if b.favorites==nil { return b.groupMenu(ctx) }
	items:=b.favoriteLocations(target.userID)
	if len(items)==0 {
		return b.Send(ctx,0,"⭐ <b>Моё избранное</b>\n\nПока нет сохранённых локаций. Выберите их в каталоге.",markup(
			[]Button{callbackButton("📍 Каталог","grp:list:0")},
			[]Button{callbackButton("🏠 Меню","grp:menu")},
		))
	}
	rows:=[][]Button{}
	for _,loc:=range items {
		rows=append(rows,[]Button{
			callbackButton("📍 "+buttonLabel(loc.Title),"grp:loc:"+loc.ID+":0"),
			callbackButton("✖️ Убрать","grp:unfavorite:"+loc.ID),
		})
	}
	rows=append(rows,[]Button{callbackButton("⚖️ Сравнить","grp:compare"),callbackButton("📍 Каталог","grp:list:0")})
	rows=append(rows,[]Button{callbackButton("🏠 Меню","grp:menu")})
	return b.Send(ctx,0,fmt.Sprintf("⭐ <b>Моё избранное</b> · %d\n\nЭти сохранения видны только Вам.",len(items)),markup(rows...))
}

func (b *Bot) groupSearchMenu(ctx context.Context) error {
	letters:=[]string{"А","Б","В","Г","Д","Е","Ж","З","И","К","Л","М","Н","О","П","Р","С","Т","У","Ф","Х","Ц","Ч","Ш","Щ","Э","Ю","Я"}
	rows:=[][]Button{}
	for i:=0;i<len(letters);i+=5 {
		row:=[]Button{}
		for _,letter:=range letters[i:min(i+5,len(letters))] {
			row=append(row,callbackButton(letter,"grp:search:"+strings.ToLower(letter)+":0"))
		}
		rows=append(rows,row)
	}
	rows=append(rows,[]Button{callbackButton("📋 Весь каталог","grp:list:0"),callbackButton("🏠 Меню","grp:menu")})
	return b.Send(ctx,0,"🔎 <b>Поиск локаций</b>\n\nВыберите первую букву названия (слово «Метро» не учитывается).\n\nДля точного поиска можно использовать приватную команду <code>/find название</code> из меню бота в группе. Не отправляйте личные данные обычными сообщениями в группу.",markup(rows...))
}
func groupSearchMatches(items []catalog.Location, prefix string) []catalog.Location {
	if prefix=="" { return nil }
	found:=make([]catalog.Location,0)
	for _,loc:=range items {
		name:=catalog.Normalize(loc.Title)
		if strings.HasPrefix(name,catalog.Normalize(prefix)) { found=append(found,loc) }
	}
	return sortedStations(found)
}
func (b *Bot) groupSearchResults(ctx context.Context, term string, page int, firstLetter bool) error {
	var matches []catalog.Location
	if firstLetter { matches=groupSearchMatches(b.store.List(),term) } else { matches=sortedStations(catalog.Search(b.store.List(),term)) }
	if len(matches)==0 {
		return b.Send(ctx,0,"🔎 <b>Ничего не найдено.</b>\n\nПопробуйте другую букву или уточните название.",markup(
			[]Button{callbackButton("🔎 К поиску","grp:search"),callbackButton("🏠 Меню","grp:menu")},
		))
	}
	last:=(len(matches)-1)/10
	if page<0 {page=0}
	if page>last {page=last}
	rows:=[][]Button{}
	for _,loc:=range matches[page*10:min(len(matches),(page+1)*10)] {
		rows=append(rows,[]Button{callbackButton("📍 "+buttonLabel(loc.Title),"grp:loc:"+loc.ID+":0")})
	}
	if firstLetter {
		nav:=[]Button{}
		if page>0 {nav=append(nav,callbackButton("◀️",fmt.Sprintf("grp:search:%s:%d",term,page-1)))}
		if page<last {nav=append(nav,callbackButton("▶️",fmt.Sprintf("grp:search:%s:%d",term,page+1)))}
		if len(nav)>0 {rows=append(rows,nav)}
	}
	rows=append(rows,[]Button{callbackButton("🔎 Новый поиск","grp:search"),callbackButton("🏠 Меню","grp:menu")})
	return b.Send(ctx,0,fmt.Sprintf("🔎 <b>Поиск:</b> %s\nНайдено: %d · Страница %d из %d",html.EscapeString(term),len(matches),page+1,last+1),markup(rows...))
}
func (b *Bot) groupCompare(ctx context.Context, notice string) error {
	target,ok:=groupScreenFromContext(ctx)
	if !ok{return errors.New("missing client context")}
	items:=b.favoriteLocations(target.userID)
	if len(items)<2 {
		return b.Send(ctx,0,"⚖️ <b>Сравнение</b>\n\nСохраните в избранное хотя бы две локации.",markup(
			[]Button{callbackButton("⭐ Избранное","grp:favorites"),callbackButton("📍 Каталог","grp:list:0")},
			[]Button{callbackButton("🏠 Меню","grp:menu")},
		))
	}
	selected:=b.comparisonSelection(target.userID)
	set:=map[string]bool{}
	for _,id:=range selected {set[id]=true}
	rows:=[][]Button{}
	for _,loc:=range items {
		indicator:="▫️"
		if set[loc.ID] {indicator="✅"}
		rows=append(rows,[]Button{callbackButton(indicator+" "+buttonLabel(loc.Title),"grp:compare:toggle:"+loc.ID)})
	}
	if len(selected)>=2 {rows=append(rows,[]Button{callbackButton("⚖️ Сравнить выбранные","grp:compare:done")})}
	rows=append(rows,[]Button{callbackButton("⭐ Избранное","grp:favorites"),callbackButton("🏠 Меню","grp:menu")})
	message:=fmt.Sprintf("⚖️ <b>Сравнение</b>\n\nВыберите 2–3 сохранённые локации. Отмечено: %d/3.",len(selected))
	if notice!="" {message+="\n"+html.EscapeString(notice)}
	return b.Send(ctx,0,message,markup(rows...))
}
func (b *Bot) groupCompareDone(ctx context.Context) error {
	target,ok:=groupScreenFromContext(ctx)
	if !ok{return errors.New("missing client context")}
	selected:=b.comparisonSelection(target.userID)
	if len(selected)<2 || len(selected)>3 {return b.groupCompare(ctx,"Выберите две или три локации.")}
	items:=[]catalog.Location{}
	for _,id:=range selected {
		if !b.favorites.Contains(target.userID,id) {continue}
		if loc,found:=b.store.Find(id);found {items=append(items,loc)}
	}
	if len(items)<2 {return b.groupFavorites(ctx)}
	b.clearComparisonSelection(target.userID)
	rows:=[][]Button{}
	for _,loc:=range items {rows=append(rows,[]Button{callbackButton("📍 "+buttonLabel(loc.Title),"grp:loc:"+loc.ID+":0")})}
	rows=append(rows,[]Button{callbackButton("⭐ Избранное","grp:favorites"),callbackButton("🏠 Меню","grp:menu")})
	return b.Send(ctx,0,comparisonText(items),markup(rows...))
}
func (b *Bot) groupPersonalLink(ctx context.Context, section,id string) error {
	start:="start"
	message:="🔒 <b>Продолжить в личном чате</b>\n\nЧтобы заполнить заявку и передать контактные данные, откройте личный диалог с ботом. Другие участники группы не увидят эти сведения."
	switch section {
	case "lead":
		start="new_application"
		if catalog.ValidID(id) && id!="menu" {start="lead_"+id}
	case "requests":
		start="applications"
		message="📂 <b>Мои заявки</b>\n\nСтатусы, дополнения и история обращений открываются в личном чате."
	case "near":
		start="nearest"
		message="📍 <b>Рядом со мной</b>\n\nГеопозицию безопаснее отправлять боту в личном чате, а не всем участникам группы."
	}
	rows:=[][]Button{{callbackButton("🏠 Меню","grp:menu")}}
	if link:=b.groupDeepLink(start);link!=""{
		rows=append([][]Button{{urlButton("🔒 Перейти в личный чат",link)}},rows...)
	} else {
		message+="\n\nОткройте профиль бота и нажмите «Начать»."
	}
	return b.Send(ctx,0,message,markup(rows...))
}
func (b *Bot) groupDeepLink(start string) string {
	username:=strings.TrimPrefix(b.botUsername,"@")
	if username=="" {return ""}
	for _,r:=range username {
		if !(r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='_') {return ""}
	}
	if start!="" {
		for _,r:=range start {
			if !(r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='_'||r=='-'){return ""}
		}
		if len(start)>64{return ""}
		return "https://t.me/"+username+"?start="+start
	}
	return "https://t.me/"+username
}

// A single published keyboard can be pinned in a group by its administrators.
// This command is restricted to configured administrators.
func (b *Bot) publishGroupPanel(ctx context.Context, chatID int64) error {
	_,err:=b.call(ctx,"sendMessage",map[string]any{
		"chat_id":chatID,
		"text":"📍 <b>Каталог локаций</b>\n\nВы можете изучать локации, сравнивать их и сохранять в избранное. Нажмите кнопку, чтобы открыть персональное меню: Ваши действия не появятся в общей переписке.",
		"parse_mode":"HTML",
		"reply_markup":markup([]Button{callbackButton("📍 Открыть персональное меню","grp:menu")}),
	})
	return err
}

func (b *Bot) groupCommand(ctx context.Context, m *message) error {
	if m==nil || !isGroupChat(m.Chat.Type) || m.From.ID<=0 {return nil}
	fields:=strings.Fields(m.Text)
	if len(fields)==0{return nil}
	cmd:=strings.ToLower(fields[0])
	if at:=strings.IndexByte(cmd,'@');at>=0 {cmd=cmd[:at]}
	if cmd=="/panel" {
		if !b.isAdmin(m.From.ID) {return nil}
		return b.publishGroupPanel(ctx,m.Chat.ID)
	}
	recognized:=cmd=="/catalog"||cmd=="/menu"||cmd=="/start"||cmd=="/list"||cmd=="/favorites"||cmd=="/find"||cmd=="/compare"||cmd=="/map"||cmd=="/myrequests"
	if !recognized{return nil}
	target:=groupScreen{chatID:m.Chat.ID,userID:m.From.ID,replyEphemeralID:m.EphemeralMessageID}
	// Never post a normal reply into a client group. An ordinary command
	// without an ephemeral ID requires the bot to be an administrator.
	privateCtx:=withGroupScreen(ctx,target)
	switch cmd {
	case "/catalog","/menu","/start":return b.groupMenu(privateCtx)
	case "/list":return b.groupCatalog(privateCtx,0)
	case "/favorites":return b.groupFavorites(privateCtx)
	case "/compare":return b.groupCompare(privateCtx,"")
	case "/map":return b.groupMap(privateCtx)
	case "/myrequests":return b.groupPersonalLink(privateCtx,"requests","")
	case "/find":
		if len(fields)<2{return b.groupSearchMenu(privateCtx)}
		return b.groupSearchResults(privateCtx,strings.Join(fields[1:]," "),0,false)
	}
	return nil
}
func (b *Bot) groupMap(ctx context.Context) error {
	return b.Send(ctx,0,"🗺 <b>Общая карта локаций</b>\n\nОткройте карту по кнопке. Другие участники группы не увидят это действие.",markup(
		[]Button{urlButton("🗺 Открыть карту",b.mapURL)},
		[]Button{callbackButton("🏠 Меню","grp:menu")},
	))
}

// Returns handled=true for every group or inline-only interaction so the
// legacy private navigators cannot edit shared cards or leak personal state.
func (b *Bot) groupCallback(ctx context.Context, cb *callbackQuery) (bool,error) {
	if cb==nil || cb.From.ID<=0 {return false,nil}
	group:=cb.Message!=nil && isGroupChat(cb.Message.Chat.Type)
	inlineOnly:=cb.Message==nil && cb.InlineMessageID!=""
	if !group && !inlineOnly {return false,nil}
	if !strings.HasPrefix(cb.Data,"grp:") &&
		!strings.HasPrefix(cb.Data,"fav:inline:") &&
		!strings.HasPrefix(cb.Data,"inline:nav:") &&
		!strings.HasPrefix(cb.Data,"fav:toggle:") &&
		!strings.HasPrefix(cb.Data,"fav:remove:") &&
		!strings.HasPrefix(cb.Data,"loc:") &&
		!strings.HasPrefix(cb.Data,"page:") &&
		!strings.HasPrefix(cb.Data,"lead:") &&
		!strings.HasPrefix(cb.Data,"search:") &&
		!strings.HasPrefix(cb.Data,"cab:") &&
		cb.Data!="menu" && cb.Data!="list" && cb.Data!="favorites" &&
		cb.Data!="search" && cb.Data!="near" {return false,nil}
	if cb.Message!=nil && cb.Message.ReceiverUser!=nil && cb.Message.ReceiverUser.ID!=cb.From.ID {
		b.answerCallback(ctx,cb.ID)
		return true,nil
	}
	target:=groupScreen{userID:cb.From.ID,callbackID:cb.ID}
	if group {
		target.chatID=cb.Message.Chat.ID
		target.ephemeralID=cb.Message.EphemeralMessageID
	}
	b.answerCallback(ctx,cb.ID)
	privateCtx:=withGroupScreen(ctx,target)
	data:=cb.Data
	if strings.HasPrefix(data,"fav:inline:") {
		id:=strings.TrimPrefix(data,"fav:inline:")
		if b.favorites!=nil {
			if _,ok:=b.store.Find(id);ok {
				added,err:=b.favorites.Toggle(cb.From.ID,id)
				if err!=nil{return true,err}
				kind:=analytics.FavoriteRemoved
				if added {kind=analytics.FavoriteAdded}
				b.recordLocationAnalytics(cb.From.ID,id,kind)
				return true,b.groupLocation(privateCtx,id,0)
			}
		}
		return true,b.groupMenu(privateCtx)
	}
	if strings.HasPrefix(data,"inline:nav:") {
		switch {
		case data=="inline:nav:menu":return true,b.groupMenu(privateCtx)
		case data=="inline:nav:favorites":return true,b.groupFavorites(privateCtx)
		case strings.HasPrefix(data,"inline:nav:lead:"):
			return true,b.groupPersonalLink(privateCtx,"lead",strings.TrimPrefix(data,"inline:nav:lead:"))
		}
		return true,nil
	}
	switch {
	case data=="grp:menu" || data=="menu":return true,b.groupMenu(privateCtx)
	case data=="grp:favorites" || data=="favorites":return true,b.groupFavorites(privateCtx)
	case data=="grp:search" || data=="search" || strings.HasPrefix(data,"search:"):
		return true,b.groupSearchMenu(privateCtx)
	case data=="grp:map":return true,b.groupMap(privateCtx)
	case data=="grp:near" || data=="near":return true,b.groupPersonalLink(privateCtx,"near","")
	case data=="grp:requests" || strings.HasPrefix(data,"cab:"):return true,b.groupPersonalLink(privateCtx,"requests","")
	case data=="grp:compare" || data=="compare:start":return true,b.groupCompare(privateCtx,"")
	case data=="grp:compare:done":return true,b.groupCompareDone(privateCtx)
	case strings.HasPrefix(data,"grp:compare:toggle:"):
		id:=strings.TrimPrefix(data,"grp:compare:toggle:")
		if !b.favorites.Contains(cb.From.ID,id){return true,b.groupFavorites(privateCtx)}
		_,full:=b.toggleComparisonSelection(cb.From.ID,id)
		notice:=""
		if full{notice="Можно выбрать не более трёх локаций."}
		return true,b.groupCompare(privateCtx,notice)
	case strings.HasPrefix(data,"grp:search:"):
		parts:=strings.Split(strings.TrimPrefix(data,"grp:search:"),":")
		if len(parts)!=2{return true,b.groupSearchMenu(privateCtx)}
		page,err:=strconv.Atoi(parts[1])
		if err!=nil || utf8.RuneCountInString(parts[0])!=1{return true,b.groupSearchMenu(privateCtx)}
		return true,b.groupSearchResults(privateCtx,parts[0],page,true)
	case strings.HasPrefix(data,"grp:list:") || strings.HasPrefix(data,"page:"):
		raw:=strings.TrimPrefix(data,"grp:list:")
		if strings.HasPrefix(data,"page:"){raw=strings.TrimPrefix(data,"page:")}
		page,err:=strconv.Atoi(raw)
		if err!=nil {page=0}
		return true,b.groupCatalog(privateCtx,page)
	case data=="list":return true,b.groupCatalog(privateCtx,0)
	case strings.HasPrefix(data,"grp:loc:") || strings.HasPrefix(data,"loc:"):
		raw:=strings.TrimPrefix(data,"grp:loc:")
		if strings.HasPrefix(data,"loc:"){raw=strings.TrimPrefix(data,"loc:")}
		parts:=strings.SplitN(raw,":",2)
		page:=0
		if len(parts)==2{page,_=strconv.Atoi(parts[1])}
		if !catalog.ValidID(parts[0]){return true,b.groupMenu(privateCtx)}
		return true,b.groupLocation(privateCtx,parts[0],page)
	case strings.HasPrefix(data,"grp:favorite:") || strings.HasPrefix(data,"fav:toggle:"):
		raw:=strings.TrimPrefix(data,"grp:favorite:")
		if strings.HasPrefix(data,"fav:toggle:"){raw=strings.TrimPrefix(data,"fav:toggle:")}
		parts:=strings.SplitN(raw,":",2)
		if !catalog.ValidID(parts[0]){return true,b.groupMenu(privateCtx)}
		page:=0
		if len(parts)>1{page,_=strconv.Atoi(parts[1])}
		if _,exists:=b.store.Find(parts[0]);!exists{return true,b.groupMenu(privateCtx)}
		added,err:=b.favorites.Toggle(cb.From.ID,parts[0])
		if err!=nil{return true,err}
		kind:=analytics.FavoriteRemoved
		if added {kind=analytics.FavoriteAdded}
		b.recordLocationAnalytics(cb.From.ID,parts[0],kind)
		return true,b.groupLocation(privateCtx,parts[0],page)
	case strings.HasPrefix(data,"grp:unfavorite:") || strings.HasPrefix(data,"fav:remove:"):
		id:=strings.TrimPrefix(data,"grp:unfavorite:")
		if strings.HasPrefix(data,"fav:remove:"){id=strings.TrimPrefix(data,"fav:remove:")}
		if !catalog.ValidID(id){return true,b.groupFavorites(privateCtx)}
		removed,err:=b.favorites.Remove(cb.From.ID,id)
		if err!=nil{return true,err}
		if removed{b.recordLocationAnalytics(cb.From.ID,id,analytics.FavoriteRemoved)}
		b.removeComparisonSelection(cb.From.ID,id)
		return true,b.groupFavorites(privateCtx)
	case strings.HasPrefix(data,"grp:lead:"):
		return true,b.groupPersonalLink(privateCtx,"lead",strings.TrimPrefix(data,"grp:lead:"))
	case strings.HasPrefix(data,"lead:"):
		return true,b.groupPersonalLink(privateCtx,"lead","menu")
	default:
		return true,b.groupMenu(privateCtx)
	}
}
