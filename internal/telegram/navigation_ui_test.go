package telegram

import (
 "context"
 "encoding/json"
 "net/http"
 "strings"
 "testing"
)

func TestCompactSearchMenusRespectOrigin(t *testing.T) {
 cases := []struct{origin,back,lead string}{
  {"favorites","favorites","lead:open:favorites"},
  {"catalog","page:0","lead:open:menu"},
  {"menu","list","lead:open:menu"},
 }
 for _,tc:=range cases {
  tr:=&captureTransport{}
  b:=&Bot{token:"TEST",http:&http.Client{Transport:tr}}
  if err:=b.requestStationSearch(context.Background(),123,tc.origin);err!=nil {t.Fatal(err)}
  if len(tr.requests)!=1 || tr.requests[0]!="/botTEST/sendMessage" {t.Fatalf("unexpected search requests: %v",tr.requests)}
  markup,_:=tr.card["reply_markup"].(map[string]any)
  rows,ok:=markup["inline_keyboard"].([]any)
  if !ok||len(rows)!=2{t.Fatalf("search menu must contain two compact rows: %+v",markup)}
  top,_:=rows[0].([]any);bottom,_:=rows[1].([]any)
  if len(top)!=2||len(bottom)!=2{t.Fatalf("wrong compact keyboard: %+v",markup)}
  start:=top[0].(map[string]any);back:=top[1].(map[string]any)
  lead:=bottom[0].(map[string]any);menu:=bottom[1].(map[string]any)
  if start["switch_inline_query_current_chat"]!=""||start["text"]!="🔎 Начать поиск" {t.Fatalf("live search button missing: %+v",start)}
  if back["callback_data"]!=tc.back || lead["callback_data"]!=tc.lead || menu["callback_data"]!="menu" {
   t.Fatalf("wrong return navigation for %s: %+v",tc.origin,rows)
  }
 }
}

func TestSearchMenuCallbackUpdatesExistingBotScreen(t *testing.T) {
 tr:=&captureTransport{}
 b:=&Bot{token:"TEST",http:&http.Client{Transport:tr}}
 cb:=&callbackQuery{ID:"cb",Data:"search:favorites",From:user{ID:100},Message:&message{MessageID:777,Chat:chat{ID:100,Type:"private"}}}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if len(tr.requests)!=2||tr.requests[0]!="/botTEST/answerCallbackQuery"||tr.requests[1]!="/botTEST/editMessageText" {t.Fatalf("search callback must edit screen, got %v",tr.requests)}
 if tr.card!=nil {t.Fatalf("search callback sent new message: %+v",tr.card)}
}

func TestInlineSelectionRemovesOnlyObsoleteOwnSearchPrompt(t *testing.T) {
 tr:=&inlineTransport{}
 b:=&Bot{token:"TEST",botID:999,http:&http.Client{Transport:tr},lastScreens:map[int64]int{77:123},searchPrompts:map[int64]int{77:123}}
 m:=&message{MessageID:333,Chat:chat{ID:77,Type:"private"},From:user{ID:77},ViaBot:&user{ID:999},Text:"карточка"}
 if err:=b.handle(context.Background(),update{Message:m});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=1||tr.calls[0]!="/botTEST/deleteMessage" {t.Fatalf("old search screen not cleaned: %v",tr.calls)}
 if _,exists:=b.searchPrompts[77];exists {t.Fatal("obsolete prompt not cleared")}
 if _,exists:=b.lastScreens[77];exists {t.Fatal("deleted search message kept as live screen")}
 tr.calls=nil
 m.ViaBot=&user{ID:1234}
 b.searchPrompts[77]=456
 b.lastScreens[77]=456
 if err:=b.handle(context.Background(),update{Message:m});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=0 {t.Fatalf("other bot's inline result must not delete our screen: %v",tr.calls)}
}

func TestSearchPromptTrackingAfterBotMessage(t *testing.T) {
 tr:=&captureTransport{}
 b:=&Bot{token:"TEST",http:&http.Client{Transport:tr}}
 if err:=b.requestStationSearch(context.Background(),91,"menu");err!=nil{t.Fatal(err)}
 if b.searchPrompts[91]!=100 || b.lastScreens[91]!=100 {t.Fatalf("new search prompt not tracked: %+v %+v",b.searchPrompts,b.lastScreens)}
 if err:=b.welcome(withNavigation(context.Background(),91,100),91,user{});err!=nil{t.Fatal(err)}
 if _,exists:=b.searchPrompts[91];exists {t.Fatal("navigating away from search must clear its cleanup marker")}
 if b.lastScreens[91]!=100 {t.Fatal("current bot screen should still be tracked")}
}

func TestInlineMenuMarkupHasNoVerboseNavConfirmation(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"x",Data:"inline:nav:menu",From:user{ID:77},InlineMessageID:"inline-123"}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=2||tr.calls[0]!="/botTEST/answerCallbackQuery"||tr.calls[1]!="/botTEST/editMessageText" {t.Fatalf("navigation must reuse inline message: %v",tr.calls)}
 raw,err:=json.Marshal(tr.payload);if err!=nil{t.Fatal(err)}
 if strings.Contains(string(raw),"Открыл в личном чате"){t.Fatal("obsolete notification must not be shown")}
}
