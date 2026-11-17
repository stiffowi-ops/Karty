package telegram

import (
 "context"
 "net/http"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "locationbot/internal/catalog"
)

func TestReviewMarkerHiddenFromCardsAndComparisons(t *testing.T) {
 original:=catalog.Location{ID:"001",Title:"Ашан Марфино (данные проверить)",Landmarks:"д. Марфино, вл110 (данные проверить)",Traffic:"Поток клиентов (данные проверить)",Conclusion:"Резервная площадка (данные проверить)"}
 card:=cardForLocation(original,defaultProjectMapURL)
 for _,phrase:=range []string{"Ашан Марфино","д. Марфино, вл110","Поток клиентов"} {
  if !strings.Contains(card,phrase){t.Fatalf("card missing %q: %s",phrase,card)}
 }
 if strings.Contains(card,"(данные проверить)"){t.Fatalf("review note leaked into card: %s",card)}
 if !strings.Contains(original.Landmarks,"(данные проверить)"){t.Fatal("source data changed")}
 if compared:=comparisonText([]catalog.Location{original});strings.Contains(compared,"(данные проверить)"){t.Fatalf("review note leaked into comparison: %s",compared)}
 if shown:=buttonLabel(original.Title);strings.Contains(shown,"(данные проверить)"){t.Fatalf("review note leaked into list: %s",shown)}
}
func TestInlineResultIsNotSearchedAgain(t *testing.T){
 tr:=&inlineTransport{}
 b:=&Bot{token:"TEST",http:&http.Client{Transport:tr}}
 msg:=&message{MessageID:101,Text:"🏪 Ашан Марфино\n🚶 ТРАФИК И ЛОКАЦИЯ\nТочка-резерв",Chat:chat{ID:123,Type:"private"},From:user{ID:12},ViaBot:&user{ID:999,FirstName:"Karty"}}
 if err:=b.handle(context.Background(),update{Message:msg});err!=nil{t.Fatal(err)}
 if len(tr.calls)!=0{t.Fatalf("inline card triggered duplicate response: %v",tr.calls)}
}
func TestNavigationEditsSingleBotMessageAcrossTabs(t *testing.T){
 root:=t.TempDir()
 seed:=filepath.Join(root,"seed.json")
 if err:=os.WriteFile(seed,[]byte("[{\"id\":\"001\",\"title\":\"Ашан Марфино\",\"traffic\":\"Трафик\"},{\"id\":\"002\",\"title\":\"Метро Троицк\"}]"),0600);err!=nil{t.Fatal(err)}
 store,err:=catalog.New(filepath.Join(root,"runtime"),seed,filepath.Join(root,"overrides.json"));if err!=nil{t.Fatal(err)}
 favorites,err:=openFavoriteStore(filepath.Join(root,"favorites.json"));if err!=nil{t.Fatal(err)}
 tr:=&inlineTransport{}
 b:=&Bot{token:"TEST",store:store,favorites:favorites,mapURL:defaultProjectMapURL,http:&http.Client{Transport:tr}}
 for _,tc:=range []struct{cb,want string}{
 {"list","Каталог локаций"},
 {"loc:001:0","Ашан Марфино"},
 {"menu","Точки роста твоего бизнеса"},
 {"favorites","Избранное"},
 {"near","Найди локации рядом"},
 } {
  cb:=&callbackQuery{ID:"callback-id",Data:tc.cb,From:user{ID:55,FirstName:"Иван"},Message:&message{MessageID:500,Chat:chat{ID:123,Type:"private"}}}
  if err:=b.handle(context.Background(),update{Callback:cb});err!=nil{t.Fatalf("callback %s: %v",tc.cb,err)}
  if len(tr.calls)<2||tr.calls[len(tr.calls)-1]!="/botTEST/editMessageText"{t.Fatalf("callback %s failed edit: %v",tc.cb,tr.calls)}
  value,_:=tr.payload["text"].(string)
  if rich,ok:=tr.payload["rich_message"].(map[string]any);ok{
   value,_=rich["html"].(string)
  }
  if !strings.Contains(value,tc.want){t.Fatalf("callback %s missing %q: %q",tc.cb,tc.want,value)}
  if tr.payload["message_id"]!=float64(500){t.Fatalf("edited wrong message: %+v",tr.payload)}
 }
 for _,request:=range tr.calls{
  if request=="/botTEST/sendMessage"||request=="/botTEST/deleteMessage"{t.Fatalf("navigation must not create/delete messages: %v",tr.calls)}
 }
}
