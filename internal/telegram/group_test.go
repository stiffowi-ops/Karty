package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGroupPanelAndPrivateMenuIsolation(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	b.adminIDs=map[int64]bool{1:true}
	ctx:=context.Background()
	groupChat:=chat{ID:-500,Type:"supergroup"}
	if err:=b.handle(ctx,update{Message:&message{Chat:groupChat,From:user{ID:77},Text:"/panel"}});err!=nil{t.Fatal(err)}
	if len(tr.calls)!=0 {t.Fatalf("non-admin posted group panel: %v",tr.calls)}
	if err:=b.handle(ctx,update{Message:&message{Chat:groupChat,From:user{ID:1},Text:"/panel"}});err!=nil{t.Fatal(err)}
	if len(tr.calls)!=1||tr.calls[0]!="/botTEST/sendMessage" {t.Fatalf("admin panel missing: %v",tr.calls)}
	panel:=tr.payload
	if _,ephemeral:=panel["ephemeral_message_parameters"];ephemeral{t.Fatal("admin panel must be public")}
	message,ok:=panel["text"].(string)
	if !ok || !strings.Contains(message,"Новые точки роста для Вашего бизнеса") || !strings.Contains(message,"Найдите перспективную локацию для развития Вашего бизнеса") {
		t.Fatalf("new group invitation missing: %q",message)
	}
	if err:=b.handle(ctx,update{Callback:&callbackQuery{
		ID:"private-menu",Data:"grp:menu",From:user{ID:77},
		Message:&message{MessageID:123,Chat:groupChat},
	}});err!=nil{t.Fatal(err)}
	if len(tr.calls)!=3||tr.calls[1]!="/botTEST/answerCallbackQuery"||tr.calls[2]!="/botTEST/sendMessage" {
		t.Fatalf("wrong callback flow: %v",tr.calls)
	}
	out:=tr.payload
	p:=out["ephemeral_message_parameters"].(map[string]any)
	if p["callback_query_id"]!="private-menu"||p["replace_callback_query_message"]!=true {
		t.Fatalf("not a private overlay: %+v",out)
	}
	if out["chat_id"]!=float64(-500){t.Fatalf("wrong group: %+v",out)}
	if _,shared:=out["editMessageText"];shared{t.Fatal("public message edited")}
}
func TestGroupFavoriteActionsRemainPerUser(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	groupChat:=chat{ID:-500,Type:"group"}
	ctx:=context.Background()
	for _,id:=range []int64{77,88}{
		cb:=&callbackQuery{ID:"save",Data:"grp:favorite:001:0",From:user{ID:id},Message:&message{Chat:groupChat,MessageID:111}}
		if err:=b.handle(ctx,update{Callback:cb});err!=nil{t.Fatal(err)}
		if tr.calls[len(tr.calls)-1]!="/botTEST/sendMessage" {
			t.Fatalf("public message was edited: %v",tr.calls)
		}
		if tr.payload["ephemeral_message_parameters"]==nil {t.Fatal("private params missing")}
	}
	if !b.favorites.Contains(77,"001")||!b.favorites.Contains(88,"001"){t.Fatal("personal favorites not saved")}
	cb:=&callbackQuery{ID:"remove",Data:"grp:favorite:001:0",From:user{ID:77},Message:&message{Chat:groupChat,MessageID:111}}
	if err:=b.handle(ctx,update{Callback:cb});err!=nil{t.Fatal(err)}
	if b.favorites.Contains(77,"001")||!b.favorites.Contains(88,"001"){t.Fatal("favorite leakage between users")}
}
func TestEphemeralGroupMessageEditingAndReceiverGate(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	cb:=&callbackQuery{ID:"ephemeral-action",Data:"grp:menu",From:user{ID:77},Message:&message{Chat:chat{ID:-500,Type:"supergroup"},EphemeralMessageID:765,ReceiverUser:&user{ID:77}}}
	if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
	if tr.calls[len(tr.calls)-1]!="/botTEST/editEphemeralMessageText"{t.Fatalf("should edit ephemeral screen: %v",tr.calls)}
	if tr.payload["receiver_user_id"]!=float64(77)||tr.payload["ephemeral_message_id"]!=float64(765) {
		t.Fatalf("bad ephemeral edit: %+v",tr.payload)
	}
	tr.calls=nil
	cb.From.ID=88
	if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
	if len(tr.calls)!=1||tr.calls[0]!="/botTEST/answerCallbackQuery" {t.Fatalf("another user modified private screen: %v",tr.calls)}
}
func TestGroupTextDoesNotTriggerPrivateLeadOrSearch(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	for _,txt:=range []string{"Иван","+79991112233","метро","/admin","/applications"} {
		if err:=b.handle(context.Background(),update{Message:&message{Chat:chat{ID:-500,Type:"supergroup"},From:user{ID:77},Text:txt}});err!=nil{t.Fatal(err)}
	}
	if len(tr.calls)!=0{t.Fatalf("group text processed unexpectedly: %v",tr.calls)}
	if err:=b.handle(context.Background(),update{Message:&message{Chat:chat{ID:-500,Type:"supergroup"},From:user{ID:77},Text:"/catalog",EphemeralMessageID:9}});err!=nil{t.Fatal(err)}
	if len(tr.calls)!=1||tr.calls[0]!="/botTEST/sendMessage"{t.Fatalf("ephemeral group command not handled: %v",tr.calls)}
	params:=tr.payload["ephemeral_message_parameters"].(map[string]any)
	if params["receiver_user_id"]!=float64(77){t.Fatalf("no client recipient: %+v",params)}
	reply:=tr.payload["reply_parameters"].(map[string]any)
	if reply["ephemeral_message_id"]!=float64(9){t.Fatalf("not a reply to ephemeral command: %+v",reply)}
}
func TestGroupLeadHandoffOnlyContainsPrivateLink(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	b.botUsername="user_bot"
	cb:=&callbackQuery{ID:"open",Data:"grp:lead:001",From:user{ID:77},Message:&message{Chat:chat{ID:-500,Type:"group"},MessageID:11}}
	if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
	raw,_:=json.Marshal(tr.payload)
	if !strings.Contains(string(raw),"https://t.me/user_bot?start=lead_001"){t.Fatalf("private deep link missing: %s",raw)}
	if !strings.Contains(string(raw),"ephemeral_message_parameters"){t.Fatal("deep link leaked to group")}
	if strings.Contains(string(raw),"Karty"){t.Fatalf("technical name exposed to customer: %s",raw)}
}

func TestGroupEphemeralTransportNeverFallsBackToPublic(t *testing.T){
	tr:=&inlineTransport{}
	b:=&Bot{token:"TEST",http:&http.Client{Transport:tr}}
	err:=b.Send(withGroupScreen(context.Background(),groupScreen{chatID:-500,userID:77}),0,"private",nil)
	if err!=nil{t.Fatal(err)}
	if len(tr.calls)!=1||tr.calls[0]!="/botTEST/sendMessage" {t.Fatal(tr.calls)}
	params:=tr.payload["ephemeral_message_parameters"].(map[string]any)
	if params["receiver_user_id"]!=float64(77){t.Fatal("no recipient")}
}

func TestGroupServiceSearchFiltersAndCompactCard(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	b.botUsername="service_bot"
	ctx:=context.Background()
	kind:=chat{ID:-500,Type:"supergroup"}
	callback:=func(data string) {
		t.Helper()
		cb:=&callbackQuery{ID:"choice",Data:data,From:user{ID:77},Message:&message{MessageID:345,Chat:kind}}
		if err:=b.handle(ctx,update{Callback:cb});err!=nil{t.Fatal(err)}
		if tr.calls[len(tr.calls)-1]!="/botTEST/sendMessage"{t.Fatalf("must answer privately: %v",tr.calls)}
		if tr.payload["ephemeral_message_parameters"]==nil{t.Fatal("missing private recipient")}
	}
	callback("grp:search")
	result:=tr.payload["reply_markup"].(map[string]any)
	kb,_:=json.Marshal(result)
	if !strings.Contains(string(kb),"grp:filter:metro:0") || !strings.Contains(string(kb),"grp:letters") {
		t.Fatalf("search filters missing: %s",kb)
	}
	callback("grp:filter:metro:0")
	txt:=tr.payload["text"].(string)
	if !strings.Contains(txt,"Станции метро") {t.Fatal(txt)}
	callback("grp:letters")
	callback("grp:search:а:0")
	txt=tr.payload["text"].(string)
	if !strings.Contains(txt,"Метро Академика") { // list contains matching button text not necessarily in body
		keyboard,_:=json.Marshal(tr.payload["reply_markup"])
		if !strings.Contains(string(keyboard),"grp:loc:001") {t.Fatalf("letter search failed: %s",keyboard)}
	}
	callback("grp:loc:001:0")
	txt=tr.payload["text"].(string)
	if !strings.Contains(txt,"Кратко о локации") || strings.Contains(txt,"Почему именно здесь") {
		t.Fatalf("location preview is not concise: %s",txt)
	}
	keyboard,_:=json.Marshal(tr.payload["reply_markup"])
	if !strings.Contains(string(keyboard),"grp:detail:001:0") || !strings.Contains(string(keyboard),"grp:lead:001") || !strings.Contains(string(keyboard),"Показать на карте") {
		t.Fatalf("card actions missing: %s",keyboard)
	}
	callback("grp:detail:001:0")
	txt=tr.payload["text"].(string)
	if !strings.Contains(txt,"Трафик и локация") {t.Fatalf("details not shown: %s",txt)}
	callback("grp:lead:001")
	txt=tr.payload["text"].(string)
	if !strings.Contains(txt,"Метро Академика") || !strings.Contains(txt,"повторно искать") {
		t.Fatalf("selected location not carried into handoff: %s",txt)
	}
	keyboard,_=json.Marshal(tr.payload["reply_markup"])
	if !strings.Contains(string(keyboard),"start=lead_001") {t.Fatalf("handoff lost chosen station: %s",keyboard)}
}
func TestSearchFirstLetterPrefixAndNoMatch(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	ctx:=withGroupScreen(context.Background(),groupScreen{chatID:-500,userID:77,callbackID:"x"})
	if err:=b.groupSearchResults(ctx,"а",0,true);err!=nil{t.Fatal(err)}
	raw,_:=json.Marshal(tr.payload["reply_markup"])
	if !strings.Contains(string(raw),"grp:prefix:ак:0") {t.Fatalf("missing second-letter refinement: %s",raw)}
	if err:=b.groupSearchResults(ctx,"аяая",0,true);err!=nil{t.Fatal(err)}
	if !strings.Contains(tr.payload["text"].(string),"Ничего не найдено"){t.Fatal(tr.payload)}
}

func TestGroupAssistantCopyKeepsPersonalScreensConcise(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 ctx:=withGroupScreen(context.Background(),groupScreen{chatID:-500,userID:77,callbackID:"go"})
 for _, screen := range []func(context.Context) error{
  b.groupMenu,
  func(ctx context.Context) error {return b.groupCatalog(ctx,0)},
  b.groupSearchMenu,
  b.groupFavorites,
  b.groupMap,
 } {
  if err:=screen(ctx);err!=nil{t.Fatal(err)}
  message,_:=tr.payload["text"].(string)
  for _,redundant:=range []string{"персональный экран","другие участники","только Вам","только для себя","не появятся в общей переписке"}{
   if strings.Contains(message,redundant){t.Fatalf("redundant privacy warning %q: %s",redundant,message)}
  }
 }
 if err:=b.groupMenu(ctx);err!=nil{t.Fatal(err)}
 if message,_:=tr.payload["text"].(string);!strings.Contains(message,"Помощник по локациям"){t.Fatalf("assistant title missing: %s",message)}
}
