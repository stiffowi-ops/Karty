package telegram

import (
 "context"
 "encoding/json"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "locationbot/internal/catalog"
)

type inlineTransport struct {calls []string; payload map[string]any}
func (tr *inlineTransport) RoundTrip(req *http.Request)(*http.Response,error){
 tr.calls=append(tr.calls,req.URL.Path)
 raw,err:=io.ReadAll(req.Body);if err!=nil{return nil,err}
 tr.payload = make(map[string]any)
 if err=json.Unmarshal(raw,&tr.payload);err!=nil{return nil,err}
 return &http.Response{StatusCode:200,Body:io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)),Header:http.Header{"Content-Type":[]string{"application/json"}}},nil
}
func makeInlineTestBot(t *testing.T)(*Bot,*inlineTransport){
 t.Helper()
 root:=t.TempDir();seed:=filepath.Join(root,"seed.json")
 data:=`[{"id":"001","title":"Метро Академика Черенкова","landmarks":"Ориентир у парка","traffic":"Жители района"},{"id":"002","title":"Ашан Марфино"},{"id":"003","title":"МГСУ"},{"id":"004","title":"Метро Кленовый бульвар"}]`
 if err:=os.WriteFile(seed,[]byte(data),0600);err!=nil{t.Fatal(err)}
 store,err:=catalog.New(filepath.Join(root,"runtime"),seed,filepath.Join(root,"overrides.json"));if err!=nil{t.Fatal(err)}
 fav,err:=openFavoriteStore(filepath.Join(root,"favorites.json"));if err!=nil{t.Fatal(err)}
 tr:=&inlineTransport{}
 return &Bot{token:"TEST",http:&http.Client{Transport:tr},store:store,favorites:fav,mapURL:defaultProjectMapURL},tr
}
func TestInlineMatchesOnlyTitlesAndPrefersPrefixes(t *testing.T){
 b,_:=makeInlineTestBot(t);items:=b.store.List()
 got:=inlineLocationMatches(items,"а",15)
 if len(got)!=3||got[0].ID!="001"||got[1].ID!="002"||got[2].ID!="004"{t.Fatalf("incorrect search order: %+v",got)}
 got=inlineLocationMatches(items,"академика",15)
 if len(got)!=1||got[0].ID!="001"{t.Fatalf("wrong match: %+v",got)}
 got=inlineLocationMatches(items,"кленовый",15)
 if len(got)!=1||got[0].ID!="004"{t.Fatalf("wrong middle-word match: %+v",got)}
 if got=inlineLocationMatches(items,"несуществующая",15);len(got)!=0{t.Fatalf("unexpected matches %+v",got)}
 if got=inlineLocationMatches(items,"",2);len(got)!=2{t.Fatalf("empty search limit broken: %d",len(got))}
}
func TestInlineQuerySendsLocationCard(t *testing.T){
 b,tr:=makeInlineTestBot(t)
 if err:=b.handle(context.Background(),update{InlineQuery:&inlineQuery{ID:"inline-1",From:user{ID:77},Query:"академика"}});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=1||tr.calls[0]!="/botTEST/answerInlineQuery"{t.Fatalf("bad API call: %v",tr.calls)}
 if tr.payload["inline_query_id"]!="inline-1"||tr.payload["cache_time"]!=float64(0){t.Fatalf("incorrect payload: %+v",tr.payload)}
 results,ok:=tr.payload["results"].([]any);if !ok||len(results)!=1{t.Fatalf("bad results: %+v",tr.payload)}
 res:=results[0].(map[string]any)
 if res["id"]!="karty:001"||res["type"]!="article"{t.Fatalf("bad inline article: %+v",res)}
 content:=res["input_message_content"].(map[string]any)
 rich:=content["rich_message"].(map[string]any)
 card:=rich["html"].(string)
 if !strings.Contains(card,"Метро Академика Черенкова")||!strings.Contains(card,"Жители района")||!strings.Contains(card,"Открыть карту")||strings.Count(card,"<details>")!=4{t.Fatalf("incomplete rich card: %s",card)}
 kb,_:=json.Marshal(res["reply_markup"])
 for _,want:=range []string{"fav:inline:001","Добавить в избранное","Перейти в избранное","inline:nav:favorites","inline:nav:menu","search:catalog"} {
  if !strings.Contains(string(kb),want){t.Fatalf("missing %q in inline keyboard: %s",want,kb)}
 }
 if strings.Contains(string(kb),"🗺 Открыть на карте"){t.Fatalf("duplicate map button: %s",kb)}
}
func TestInlineFavoriteWithoutChatMessage(t *testing.T){
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"callback-1",Data:"fav:inline:001",From:user{ID:77}}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if !b.favorites.Contains(77,"001"){t.Fatal("inline favorite not saved")}
 if tr.calls[0]!="/botTEST/answerCallbackQuery"{t.Fatalf("wrong callback path: %v",tr.calls)}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if b.favorites.Contains(77,"001"){t.Fatal("inline favorite not removed")}
}


func TestInlineFavoriteChangesButtonLabel(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"clicked",Data:"fav:inline:001",From:user{ID:77},InlineMessageID:"inline-message-123"}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if !b.favorites.Contains(77,"001"){t.Fatal("favorite not added")}
 if len(tr.calls)!=2||tr.calls[0]!="/botTEST/answerCallbackQuery"||tr.calls[1]!="/botTEST/sendMessage"{
  t.Fatalf("shared inline card must not be edited: %v",tr.calls)
 }
 if tr.payload["ephemeral_message_parameters"]==nil{t.Fatal("private result missing")}
 if err:=b.handle(context.Background(),update{InlineQuery:&inlineQuery{ID:"lookup2",From:user{ID:77},Query:"академика"}});err!=nil{t.Fatal(err)}
 results:=tr.payload["results"].([]any)
 kb,_:=json.Marshal(results[0].(map[string]any)["reply_markup"])
 if !strings.Contains(string(kb),"Убрать из избранного"){t.Fatalf("saved favorite not reflected in fresh results: %s",kb)}
}


func TestInlineNavigationOpensPersonalChat(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"nav",Data:"inline:nav:favorites",From:user{ID:77},InlineMessageID:"inline-123"}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=2||tr.calls[0]!="/botTEST/answerCallbackQuery"||tr.calls[1]!="/botTEST/sendMessage"{
  t.Fatalf("private ephemeral favorites missing: %v",tr.calls)
 }
 if tr.payload["chat_id"]!=nil||tr.payload["ephemeral_message_parameters"]==nil{
  t.Fatalf("favorites must only answer clicking user, not shared chat: %+v",tr.payload)
 }
}


func TestInlineMainMenuReplacesChosenResult(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"nav-menu",Data:"inline:nav:menu",From:user{ID:77,FirstName:"Иван"},InlineMessageID:"inline-123"}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=2||tr.calls[0]!="/botTEST/answerCallbackQuery"||tr.calls[1]!="/botTEST/sendMessage"{
  t.Fatalf("inline menu must stay private: %v",tr.calls)
 }
 if tr.payload["inline_message_id"]!=nil||tr.payload["ephemeral_message_parameters"]==nil{
  t.Fatalf("shared inline result edited: %+v",tr.payload)
 }
 value,_:=tr.payload["text"].(string)
 if !strings.Contains(value,"Помощник по локациям"){t.Fatalf("missing private menu: %q",value)}
}

func TestInlineSearchTransitionUpdatesChosenResult(t *testing.T) {
 b,tr:=makeInlineTestBot(t)
 cb:=&callbackQuery{ID:"nav-search",Data:"search:catalog",From:user{ID:77},InlineMessageID:"inline-123"}
 if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=2||tr.calls[1]!="/botTEST/sendMessage" {
  t.Fatalf("search must be private: %v",tr.calls)
 }
 if tr.payload["ephemeral_message_parameters"]==nil||tr.payload["inline_message_id"]!=nil{
  t.Fatalf("inline card was modified: %+v",tr.payload)
 }
}
