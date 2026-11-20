package telegram

import (
 "bytes"
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "html"
 "io"
 "net/http"
 "net/url"
 "sort"
 "strconv"
 "strings"
 "time"

 "locationbot/internal/catalog"
 "locationbot/internal/xlsx"
)

const (
 importLocationsSheet = "Локации"
 importSectionsSheet = "Разделы"
 importInstructionsSheet = "Инструкция"
)
var locationHeaders=[]string{"Код","Название","Категория","Статус","Ориентир","Описание","Ссылка на карту","Широта","Долгота"}
var sectionHeaders=[]string{"Код локации","Порядок","Название раздела","Текст раздела","Показывать"}

func importGuide() [][]string {
 return [][]string{
  {"Karty · импорт локаций"},
  {"1. Лист «Локации»: одна строка = одна локация. Код — произвольное уникальное слово (например, point-1)."},
  {"2. Обязательные поля: Код и Название. Широта и долгота НЕОБЯЗАТЕЛЬНЫ; при заполнении нужны обе."},
  {"3. Лист «Разделы»: несколько строк с тем же кодом = свои разделы этой локации."},
  {"4. Названия, тексты, количество и порядок разделов задаются вами; от 0 до 15 на локацию."},
  {"5. «Показывать»: Да/Нет (пустое значение = Да)."},
  {"6. Максимум 50 новых локаций за файл. Экспорт существующих карточек предназначен для просмотра: импортировать их повторно как новые нельзя."},
  {"7. Загрузите XLSX в личный чат администратора, проверьте результат, подтвердите публикацию."},
 }
}
func templateWorkbook()([]byte,error){
 return xlsx.Write([]xlsx.Sheet{
  {Name:importInstructionsSheet, Rows:importGuide()},
  {Name:importLocationsSheet,Rows:[][]string{
    locationHeaders,
    {"point-1","Метро Маяковская","Метро","Действующая станция","Выход № 2","Перспективная локация","https://yandex.ru/maps/","",""},
    {"point-2","Новая торговая точка","Торговля","","","Описание второго объекта","","",""},
  }},
  {Name:importSectionsSheet,Rows:[][]string{
    sectionHeaders,
    {"point-1","1","🚶 Трафик","Высокий пешеходный поток","Да"},
    {"point-1","2","🏪 Для бизнеса","Кофейня и услуги","Да"},
    {"point-2","1","👥 Аудитория","Жители соседних домов","Да"},
  }},
 })
}
func exportWorkbook(locations []catalog.Location)([]byte,error){
 sort.Slice(locations,func(i,j int)bool{return locations[i].Title<locations[j].Title})
 rows:=[][]string{locationHeaders}
 sections:=[][]string{sectionHeaders}
 for _,loc:=range locations{
  row:=[]string{loc.ID,loc.Title,loc.Category,loc.LineStatus,loc.Landmarks,loc.Description,loc.MapURL,"",""}
  if loc.Latitude!=nil{row[7]=strconv.FormatFloat(*loc.Latitude,'f',-1,64)}
  if loc.Longitude!=nil{row[8]=strconv.FormatFloat(*loc.Longitude,'f',-1,64)}
  rows=append(rows,row)
  for i,sec:=range catalog.Revision(loc).Sections{
   yes:="Да";if !sec.Enabled{yes="Нет"}
   sections=append(sections,[]string{loc.ID,strconv.Itoa(i+1),sec.Title,sec.Body,yes})
  }
 }
 return xlsx.Write([]xlsx.Sheet{
  {Name:importInstructionsSheet,Rows:importGuide()},
  {Name:importLocationsSheet,Rows:rows},
  {Name:importSectionsSheet,Rows:sections},
 })
}
func readValue(row []string,index int)string{
 if index>=0&&index<len(row){return strings.TrimSpace(row[index])}
 return ""
}
func blankRow(row []string)bool{
 for _,v:=range row{if strings.TrimSpace(v)!=""{return false}}
 return true
}
func validateHeaders(rows [][]string, want []string, sheet string)error{
 if len(rows)==0{return fmt.Errorf("лист «%s» пуст",sheet)}
 for i,value:=range want{
  if readValue(rows[0],i)!=value{return fmt.Errorf("лист «%s»: заголовок %d должен быть «%s»",sheet,i+1,value)}
 }
 return nil
}
func parseCoordinate(s string, low,high float64)(*float64,error){
 if s==""{return nil,nil}
 if strings.Contains(s,","){s=strings.ReplaceAll(s,",",".")}
 val,err:=strconv.ParseFloat(s,64)
 if err!=nil||val<low||val>high{return nil,errors.New("некорректные координаты")}
 return &val,nil
}
func parseImport(data []byte,store *catalog.Store)([]catalog.Location,error){
 sheets,err:=xlsx.Read(data,importLocationsSheet,importSectionsSheet)
 if err!=nil{return nil,err}
 locations:=sheets[importLocationsSheet];sections:=sheets[importSectionsSheet]
 if err=validateHeaders(locations,locationHeaders,importLocationsSheet);err!=nil{return nil,err}
 if err=validateHeaders(sections,sectionHeaders,importSectionsSheet);err!=nil{return nil,err}
 if len(locations)>501{return nil,errors.New("слишком много локаций (максимум 50)")}
 byKey:=map[string]*catalog.Location{}
 order:=[]string{}
 titles:=map[string]bool{}
 for i,row:=range locations[1:]{
  if blankRow(row){continue}
  if len(order)>=50{return nil,errors.New("максимум 50 локаций за один импорт")}
  key:=readValue(row,0); title:=readValue(row,1)
  if key==""||len(key)>60||title==""{return nil,fmt.Errorf("Локации, строка %d: заполните код и название",i+2)}
  for _,char:=range key{
   if !((char>='a'&&char<='z')||(char>='A'&&char<='Z')||(char>='0'&&char<='9')||char=='-'||char=='_'){
    return nil,fmt.Errorf("Локации, строка %d: код содержит недопустимые символы",i+2)
   }
  }
  if _,found:=byKey[key];found{return nil,fmt.Errorf("Локации, строка %d: повторяется код %s",i+2,key)}
  normal:=catalog.Normalize(title);if titles[normal]{return nil,fmt.Errorf("Локации, строка %d: повторяется название",i+2)}
  titles[normal]=true
  lat,err:=parseCoordinate(readValue(row,7),-90,90);if err!=nil{return nil,fmt.Errorf("Локации, строка %d: неверная широта",i+2)}
  lon,err:=parseCoordinate(readValue(row,8),-180,180);if err!=nil{return nil,fmt.Errorf("Локации, строка %d: неверная долгота",i+2)}
  if (lat==nil)!=(lon==nil){return nil,fmt.Errorf("Локации, строка %d: заполните обе координаты или оставьте обе пустыми",i+2)}
  mapURL:=readValue(row,6);if mapURL!=""&&!strings.HasPrefix(mapURL,"https://"){return nil,fmt.Errorf("Локации, строка %d: ссылка должна начинаться с https://",i+2)}
  obj:=&catalog.Location{Title:title,Category:readValue(row,2),LineStatus:readValue(row,3),Landmarks:readValue(row,4),Description:readValue(row,5),MapURL:mapURL,Latitude:lat,Longitude:lon,Sections:[]catalog.CardSection{}}
  byKey[key]=obj
  order=append(order,key)
 }
 if len(order)==0{return nil,errors.New("лист «Локации» не содержит карточек")}
 secPositions:=map[string]map[int]bool{}
 for i,row:=range sections[1:]{
  if blankRow(row){continue}
  key:=readValue(row,0)
  obj:=byKey[key]
  if obj==nil{return nil,fmt.Errorf("Разделы, строка %d: неизвестный код локации %q",i+2,key)}
  pos,err:=strconv.Atoi(readValue(row,1))
  if err!=nil||pos<1||pos>15{return nil,fmt.Errorf("Разделы, строка %d: порядок должен быть от 1 до 15",i+2)}
  if secPositions[key]==nil{secPositions[key]=map[int]bool{}}
  if secPositions[key][pos]{return nil,fmt.Errorf("Разделы, строка %d: повторяется порядок раздела",i+2)}
  secPositions[key][pos]=true
  title:=readValue(row,2)
  if title==""{return nil,fmt.Errorf("Разделы, строка %d: отсутствует название",i+2)}
  visibility:=strings.ToLower(readValue(row,4))
  enabled:=visibility==""||visibility=="да"||visibility=="yes"||visibility=="true"||visibility=="1"
  if !enabled&&visibility!="нет"&&visibility!="no"&&visibility!="false"&&visibility!="0"{
   return nil,fmt.Errorf("Разделы, строка %d: укажите Да или Нет",i+2)
  }
  if len(obj.Sections)>=15{return nil,fmt.Errorf("Разделы, строка %d: максимум 15 разделов",i+2)}
  obj.Sections=append(obj.Sections,catalog.CardSection{ID:fmt.Sprintf("section-%02d",pos),Title:title,Body:readValue(row,3),Enabled:enabled})
 }
 results:=make([]catalog.Location,0,len(order))
 known:=map[string]bool{}
 for _,loc:=range store.List(){known[catalog.Normalize(loc.Title)]=true}
 for _,key:=range order{
  obj:=byKey[key]
  if known[catalog.Normalize(obj.Title)]{return nil,fmt.Errorf("локация «%s» уже существует в каталоге",obj.Title)}
  sort.Slice(obj.Sections,func(i,j int)bool{return obj.Sections[i].ID<obj.Sections[j].ID})
  id,e:=catalog.NewAdminLocationID();if e!=nil{return nil,e}
  obj.ID=id
  if e=catalog.Revision(*obj).Validate();e!=nil{return nil,fmt.Errorf("локация «%s»: %w",obj.Title,e)}
  results=append(results,*obj)
 }
 return results,nil
}
func (b *Bot) getXLSX(ctx context.Context,fileID string,size int64)([]byte,error){
 if size<0||size>xlsx.MaxFileSize{return nil,errors.New("XLSX слишком большой, максимум 5 МБ")}
 raw,err:=b.call(ctx,"getFile",map[string]any{"file_id":fileID});if err!=nil{return nil,err}
 var metadata struct{FilePath string `json:"file_path"`;FileSize int64 `json:"file_size"`}
 if err=json.Unmarshal(raw,&metadata);err!=nil{return nil,err}
 if metadata.FileSize>xlsx.MaxFileSize{return nil,errors.New("XLSX слишком большой")}
 filePath:=metadata.FilePath
 if filePath==""||strings.Contains(filePath,"..")||strings.Contains(filePath,`\`)||strings.HasPrefix(filePath,"/"){return nil,errors.New("некорректный путь Excel")}
 for _,part:=range strings.Split(filePath,"/"){if part==""||part=="."||part==".."{return nil,errors.New("некорректный путь Excel")}}
 address:="https://api.telegram.org/file/bot"+url.PathEscape(b.token)+"/"+filePath
 req,err:=http.NewRequestWithContext(ctx,http.MethodGet,address,nil);if err!=nil{return nil,err}
 resp,err:=b.http.Do(req);if err!=nil{return nil,err};defer resp.Body.Close()
 if resp.StatusCode!=http.StatusOK{return nil,fmt.Errorf("Telegram не отдал Excel: HTTP %d",resp.StatusCode)}
 data,err:=io.ReadAll(io.LimitReader(resp.Body,xlsx.MaxFileSize+1));if err!=nil{return nil,err}
 if len(data)>xlsx.MaxFileSize{return nil,errors.New("XLSX слишком большой")}
 return data,nil
}
func (b *Bot) showExcelMenu(ctx context.Context,chatID int64)error{
 return b.Send(ctx,chatID,"📊 <b>Excel и локации</b>

Скачайте шаблон, заполните локации и произвольные разделы, затем отправьте файл в личный чат бота. Координаты необязательны. Перед публикацией будет проверка и подтверждение.",markup(
  []Button{callbackButton("📄 Скачать шаблон","adm:xlsx:template")},
  []Button{callbackButton("📥 Загрузить Excel","adm:xlsx:upload")},
  []Button{callbackButton("📤 Экспорт каталога","adm:xlsx:export")},
  []Button{callbackButton("⬅️ К локациям","adm:locations:0")},
 ))
}
func (b *Bot) handleExcelCallback(ctx context.Context,chatID int64,userID int64,data string)(bool,error){
 if !strings.HasPrefix(data,"adm:xlsx:"){return false,nil}
 if !b.isAdmin(userID)||chatID!=userID{return true,nil}
 session:=b.adminSession(userID)
 switch data{
 case "adm:xlsx:menu":
  session.AwaitingExcel=false;session.PendingExcel=nil
  return true,b.showExcelMenu(ctx,chatID)
 case "adm:xlsx:template":
  raw,err:=templateWorkbook();if err!=nil{return true,err}
  return true,b.sendAdminDocument(ctx,chatID,raw,"Karty_Шаблон_локаций.xlsx")
 case "adm:xlsx:export":
  raw,err:=exportWorkbook(b.store.List());if err!=nil{return true,err}
  return true,b.sendAdminDocument(ctx,chatID,raw,"Karty_Каталог_"+time.Now().UTC().Format("20060102")+".xlsx")
 case "adm:xlsx:upload":
  session.AwaitingExcel=true;session.PendingExcel=nil
  return true,b.Send(ctx,chatID,"📥 <b>Загрузить Excel</b>

Пришлите XLSX-файл документом. Локации будут проверены и останутся неопубликованными до подтверждения. Координаты можно оставить пустыми.",markup([]Button{callbackButton("❌ Отмена","adm:xlsx:menu")}))
 case "adm:xlsx:publish":
  if len(session.PendingExcel)==0{return true,b.showExcelMenu(ctx,chatID)}
  b.backupMu.Lock()
  err:=b.store.CreateAdminLocations(session.PendingExcel)
  b.backupMu.Unlock()
  if err!=nil{return true,b.Send(ctx,chatID,"❌ Импорт не выполнен: "+html.EscapeString(err.Error()),markup([]Button{callbackButton("❌ Отмена","adm:xlsx:menu")}))}
  count:=len(session.PendingExcel)
  session.PendingExcel=nil
  return true,b.Send(ctx,chatID,fmt.Sprintf("✅ <b>Опубликовано локаций: %d</b>

Карточки доступны в каталоге. Разделы и координаты сохранены.",count),markup([]Button{callbackButton("📍 Все локации","adm:locations:0")}))
 }
 return true,b.showExcelMenu(ctx,chatID)
}
func (b *Bot) receiveLocationExcel(ctx context.Context,m *message)error{
 if m.Document==nil{return nil}
 session:=b.adminSession(m.From.ID)
 if !strings.HasSuffix(strings.ToLower(m.Document.FileName),".xlsx"){
  return b.Send(ctx,m.Chat.ID,"Нужен файл с расширением .xlsx.",nil)
 }
 data,err:=b.getXLSX(ctx,m.Document.FileID,m.Document.FileSize)
 if err!=nil{return b.Send(ctx,m.Chat.ID,"❌ Не удалось скачать Excel: "+html.EscapeString(err.Error()),nil)}
 items,err:=parseImport(data,b.store)
 if err!=nil{return b.Send(ctx,m.Chat.ID,"❌ <b>Ошибка проверки Excel</b>

"+html.EscapeString(err.Error())+"

Исправьте файл и отправьте повторно. Ничего не опубликовано.",markup([]Button{callbackButton("❌ Отмена","adm:xlsx:menu")}))
 }
 session.AwaitingExcel=false
 session.PendingExcel=items
 names:=make([]string,0,min(len(items),5))
 for _,item:=range items[:min(len(items),5)]{names=append(names,"• "+html.EscapeString(item.Title))}
 more:=""
 if len(items)>5{more=fmt.Sprintf("
... и ещё %d",len(items)-5)}
 return b.Send(ctx,m.Chat.ID,fmt.Sprintf("✅ <b>Excel проверен</b>

Локаций: %d

%s%s

Подтвердить публикацию? Существующие карточки не меняются.",len(items),strings.Join(names,"
"),more),markup(
  []Button{callbackButton("✅ Опубликовать всё","adm:xlsx:publish")},
  []Button{callbackButton("❌ Отмена","adm:xlsx:menu")},
 ))
}
// Convenience helper for unit tests: ensures workbook round-trips.
func validateWorkbookRoundtrip(data []byte)error{
 _,err:=xlsx.Read(data,importLocationsSheet,importSectionsSheet)
 return err
}
var _ = bytes.MinRead
