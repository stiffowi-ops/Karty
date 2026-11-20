package telegram

import (
 "context"
 "encoding/json"
 "net/http"
 "strings"
 "testing"
)

func TestChannelCallbackPrivateMenuAndNavigation(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 channel:=chat{ID:-100987654321,Type:"channel"}
 for _,action:=range []string{"grp:menu","grp:list:0","grp:favorites","grp:search","grp:loc:001:0"} {
  tr.calls=nil
  cb:=&callbackQuery{ID:"channel-callback",Data:action,From:user{ID:77},Message:&message{Chat:channel,MessageID:500}}
  if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatalf("%s: %v",action,err)}
  if len(tr.calls)!=2 || tr.calls[0]!="/botTEST/answerCallbackQuery" || tr.calls[1]!="/botTEST/sendMessage" {t.Fatalf("%s: wrong calls %v",action,tr.calls)}
  if _,hasChat:=tr.payload["chat_id"];hasChat {t.Fatalf("%s: leaked into public channel: %+v",action,tr.payload)}
  params,ok:=tr.payload["ephemeral_message_parameters"].(map[string]any)
  if !ok || params["callback_query_id"]!="channel-callback" || params["receiver_user_id"]!=nil || params["replace_callback_query_message"]!=true {t.Fatalf("%s: unsafe params %+v",action,tr.payload)}
 }
}
func TestChannelPersonalFavoriteSeparated(t *testing.T){
 b,tr:=makeInlineTestBot(t)
 ch:=chat{ID:-100987654321,Type:"channel"}
 for _,id:=range []int64{77,88}{
  cb:=&callbackQuery{ID:"toggle",Data:"grp:favorite:001:0",From:user{ID:id},Message:&message{Chat:ch,MessageID:23}}
  if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
  if _,ok:=tr.payload["chat_id"];ok{t.Fatal("favorite leaked into channel")}
 }
 if !b.favorites.Contains(77,"001")||!b.favorites.Contains(88,"001"){t.Fatal("missing personal favorites")}
}
func TestChannelEphemeralCallbackDoesNotEditSupergroupMessage(t *testing.T){
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"ephemeral",Data:"grp:menu",From:user{ID:77},Message:&message{Chat:chat{ID:-10090,Type:"channel"},EphemeralMessageID:456,ReceiverUser:&user{ID:77}}}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if tr.calls[len(tr.calls)-1]!="/botTEST/sendMessage"{t.Fatalf("channel edit attempted: %v",tr.calls)}
 if _,ok:=tr.payload["chat_id"];ok{t.Fatal("channel chat ID exposed")}
 params:=tr.payload["ephemeral_message_parameters"].(map[string]any)
 if params["replace_callback_query_message"]!=nil{t.Fatal("must not replace ephemeral callback")}
}
type channelPanelTransport struct {
 calls []string
 payload map[string]any
}
func (tr *channelPanelTransport) RoundTrip(r *http.Request)(*http.Response,error) {
 method:=r.URL.Path[strings.LastIndex(r.URL.Path,"/")+1:]
 tr.calls=append(tr.calls,method)
 _=json.NewDecoder(r.Body).Decode(&tr.payload)
 body:=`{"ok":true,"result":true}`
 switch method {
 case "getChat":
  body=`{"ok":true,"result":{"id":-100987654321,"type":"channel","title":"Karty"}}`
 case "getChatMember":
  body=`{"ok":true,"result":{"status":"administrator","can_post_messages":true,"can_edit_messages":true}}`
 case "sendMessage":
  if tr.payload["ephemeral_message_parameters"]==nil && tr.payload["chat_id"]==float64(-100987654321) {
   body=`{"ok":true,"result":{"message_id":123}}`
  }else {body=`{"ok":true,"result":{"message_id":124}}`}
 }
 return &http.Response{StatusCode:200,Header:http.Header{"Content-Type":[]string{"application/json"}},Body:strings.NewReader(body)},nil
}
func TestAdminChannelPanelPublishesAndPins(t *testing.T){
 b,_:=makeInlineTestBot(t)
 tr:=&channelPanelTransport{}
 b.http=&http.Client{Transport:tr}
 b.botID=101
 b.adminIDs=map[int64]bool{1:true}
 m:=&message{Chat:chat{ID:1,Type:"private"},From:user{ID:1},Text:"/channelpanel @test_channel"}
 if err:=b.handle(context.Background(),update{Message:m});err!=nil{t.Fatal(err)}
 steps:=strings.Join(tr.calls,",")
 if !strings.Contains(steps,"getChatMember")||!strings.Contains(steps,"pinChatMessage"){t.Fatalf("channel panel not pinned: %s",steps)}
 if !strings.Contains(steps,"sendMessage"){t.Fatal("no panel")}
}
func TestUnauthorizedChannelPanelDoesNothing(t *testing.T){
 b,tr:=makeInlineTestBot(t)
 b.adminIDs=map[int64]bool{1:true}
 m:=&message{Chat:chat{ID:77,Type:"private"},From:user{ID:77},Text:"/channelpanel @test_channel"}
 if err:=b.handle(context.Background(),update{Message:m});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=0{t.Fatalf("unauthorized action: %v",tr.calls)}
}
