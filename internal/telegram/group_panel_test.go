package telegram

import (
 "context"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "strings"
 "testing"
)

type groupPinTransport struct {
 methods []string
 bodies []map[string]any
 pinnedID int
 pinFailure bool
 staleEdit bool
}
func (tr *groupPinTransport) RoundTrip(r *http.Request)(*http.Response,error){
 method:=filepath.Base(r.URL.Path)
 tr.methods=append(tr.methods,method)
 var data map[string]any
 if err:=json.NewDecoder(r.Body).Decode(&data);err!=nil{return nil,err}
 tr.bodies=append(tr.bodies,data)
 body:=`{"ok":true,"result":true}`
 switch method {
 case "getChat":
  if tr.pinnedID>0 {
   body=`{"ok":true,"result":{"pinned_message":{"message_id":`+strconvItoa(tr.pinnedID)+`,"reply_markup":{"inline_keyboard":[[{"text":"📍 Позвать помощника","callback_data":"grp:menu"}]]}}}}`
  } else {body=`{"ok":true,"result":{}}`}
 case "sendMessage":
  body=`{"ok":true,"result":{"message_id":223}}`
 case "editMessageText":
  if tr.staleEdit { body=`{"ok":false,"description":"Bad Request: message to edit not found"}` }
 case "pinChatMessage":
  if tr.pinFailure {body=`{"ok":false,"description":"Bad Request: not enough rights to manage pinned messages"}`}
 }
 return &http.Response{StatusCode:200,Header:http.Header{"Content-Type":[]string{"application/json"}},Body:io.NopCloser(strings.NewReader(body))},nil
}
func strconvItoa(n int)string {return fmt.Sprintf("%d",n)}

func TestAutoPinPanelReusesStoredMessageAndSurvivesRestart(t *testing.T){
 b,_:=makeInlineTestBot(t)
 file:=filepath.Join(t.TempDir(),"group_panels.json")
 tr:=&groupPinTransport{}
 b.http=&http.Client{Transport:tr}
 b.groupPanelPath=file
 if err:=b.publishGroupPanel(context.Background(),-500);err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="getChat,sendMessage,pinChatMessage" {
  t.Fatalf("first publish should send and pin: %v",tr.methods)
 }
 raw,err:=os.ReadFile(file);if err!=nil{t.Fatal(err)}
 if !strings.Contains(string(raw),`"-500":223`){t.Fatalf("missing durable message ID: %s",raw)}
 tr.methods=nil
 if err:=b.publishGroupPanel(context.Background(),-500);err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="editMessageText,pinChatMessage"{
  t.Fatalf("repeated panel must edit same message: %v",tr.methods)
 }
 if tr.bodies[len(tr.bodies)-2]["message_id"]!=float64(223){t.Fatalf("wrong edit target: %+v",tr.bodies)}
 b2,_:=makeInlineTestBot(t)
 b2.groupPanelPath=file
 b2.http=&http.Client{Transport:tr}
 tr.methods=nil
 if err:=b2.publishGroupPanel(context.Background(),-500);err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="editMessageText,pinChatMessage" {
  t.Fatalf("restart duplicated group panel: %v",tr.methods)
 }
}

func TestAutoPinAdoptsOldPinnedHelper(t *testing.T){
 b,_:=makeInlineTestBot(t)
 tr:=&groupPinTransport{pinnedID:321}
 b.http=&http.Client{Transport:tr}
 if err:=b.publishGroupPanel(context.Background(),-200);err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="getChat,editMessageText,pinChatMessage"{
  t.Fatalf("old pinned helper not adopted: %v",tr.methods)
 }
 if tr.bodies[1]["message_id"]!=float64(321){t.Fatal("edited incorrect message")}
}

func TestAutoPinFailedPermissionCanRetryWithoutDuplicate(t *testing.T){
 b,_:=makeInlineTestBot(t)
 tr:=&groupPinTransport{pinFailure:true}
 b.http=&http.Client{Transport:tr}
 b.adminIDs=map[int64]bool{1:true}
 if err:=b.handle(context.Background(),update{Message:&message{Chat:chat{ID:-500,Type:"supergroup"},From:user{ID:1},Text:"/panel"}});err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="getChat,sendMessage,pinChatMessage,sendMessage"{
  t.Fatalf("permission failure must notify admin privately: %v",tr.methods)
 }
 last:=tr.bodies[len(tr.bodies)-1]
 if _,ok:=last["ephemeral_message_parameters"];!ok{t.Fatalf("admin warning leaked publicly: %+v",last)}
 if strings.Contains(last["text"].(string),"Karty"){t.Fatal("technical name in message")}
 tr.pinFailure=false
 tr.methods=nil
 if err:=b.publishGroupPanel(context.Background(),-500);err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="editMessageText,pinChatMessage"{
  t.Fatalf("retry must not duplicate post: %v",tr.methods)
 }
}

func TestAutoPinReplacesDeletedPanel(t *testing.T){
 b,_:=makeInlineTestBot(t)
 tr:=&groupPinTransport{staleEdit:true}
 b.http=&http.Client{Transport:tr}
 b.groupPanelIDs=map[int64]int{-500:111}
 if err:=b.publishGroupPanel(context.Background(),-500);err!=nil{t.Fatal(err)}
 if strings.Join(tr.methods,",")!="editMessageText,sendMessage,pinChatMessage"{
  t.Fatalf("deleted panel fallback broken: %v",tr.methods)
 }
 if b.groupPanelIDs[-500]!=223{t.Fatal("new message id not recorded")}
}
