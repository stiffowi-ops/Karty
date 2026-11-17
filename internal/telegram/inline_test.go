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
 card:=content["message_text"].(string)
 if !strings.Contains(card,"<b>Метро Академика Черенкова</b>")||!strings.Contains(card,"Жители района")||!strings.Contains(card,"Открыть карту"){t.Fatalf("incomplete card: %s",card)}
 kb,_:=json.Marshal(res["reply_markup"])
 if !strings.Contains(string(kb),"fav:inline:001")||!strings.Contains(string(kb),"switch_inline_query_current_chat"){t.Fatalf("inline buttons missing: %s",kb)}
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
