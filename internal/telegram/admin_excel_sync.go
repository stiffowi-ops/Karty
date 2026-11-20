package telegram

import (
 "context"
 "fmt"
 "html"
 "path/filepath"
 "sort"
 "strconv"
 "strings"
 "time"

 "locationbot/internal/adminbackup"
 "locationbot/internal/catalog"
 "locationbot/internal/xlsx"
)

const fullSyncMarker="KARTY_FULL_SYNC_V1"
const fullSyncRegistry="Реестр"
const maxSyncLocations=650

type catalogSyncPreview struct {
 Desired []catalog.Location
 Removals []string
 Fingerprint string
 Added int
 Updated int
 Unchanged int
 RemovedNames []string
 UpdatedNames []string
 AddedNames []string
}

func worksheetName(index int,title string)string{
 title=strings.TrimSpace(title)
 title=strings.Map(func(r rune)rune{
  switch r {case ':','\\','/','?','*','[',']':return ' '}
  if r<32{return ' '}
  return r
 },title)
 prefix:=fmt.Sprintf("%03d ",index)
 room:=31-len([]rune(prefix))
 chars:=[]rune(title)
 if len(chars)>room {chars=chars[:room]}
 return prefix+string(chars)
}
func locationSheet(loc catalog.Location)[][]string{
 rows:=[][]string{
  {"Karty · карточка локации","Изменяй значения в столбце B"},
  {"ID (пустой для новой локации)",loc.ID},
  {"Название",loc.Title},
  {"Категория",loc.Category},
  {"Статус",loc.LineStatus},
  {"Ориентир",loc.Landmarks},
  {"Описание",loc.Description},
  {"Ссылка на карту",loc.MapURL},
  {"Широта (необязательно)",""},
  {"Долгота (необязательно)",""},
  {"Рейтинг",loc.Rating},
  {"",""},
  {"ВАЖНО","Удаление листа означает окончательное удаление локации после подтверждения"},
  {"",""},
  {"НАСТРАИВАЕМЫЕ РАЗДЕЛЫ","От 0 до 15. Название, порядок и текст произвольные."},
  {"Порядок","Название раздела","Текст раздела","Показывать (Да/Нет)","ID раздела (не менять)"},
 }
 if loc.Latitude!=nil{rows[8][1]=strconv.FormatFloat(*loc.Latitude,'f',-1,64)}
 if loc.Longitude!=nil{rows[9][1]=strconv.FormatFloat(*loc.Longitude,'f',-1,64)}
 for i,sec:=range catalog.Revision(loc).Sections{
  enabled:="Да";if !sec.Enabled{enabled="Нет"}
  rows=append(rows,[]string{strconv.Itoa(i+1),sec.Title,sec.Body,enabled,sec.ID})
 }
 return rows
}
func exportFullCatalog(store *catalog.Store)([]byte,error){
 locs:=store.List()
 if len(locs)>maxSyncLocations{return nil,fmt.Errorf("каталог слишком большой: %d локаций",len(locs))}
 sort.Slice(locs,func(i,j int)bool{return locs[i].ID<locs[j].ID})
 index:=[][]string{
  {fullSyncMarker,store.CatalogFingerprint()},
  {"Дата экспорта (UTC)",time.Now().UTC().Format(time.RFC3339)},
  {"ИНСТРУКЦИЯ","Один лист = одна локация. Удаление листа = удаление локации после подтверждения."},
  {"НОВАЯ ЛОКАЦИЯ","Скопируйте лист любой карточки, очистите B2 (ID) и заполните новое название."},
  {"КООРДИНАТЫ","Широта и долгота необязательны; если заполнены, нужны обе."},
  {"ID","Название","Вкладка"},
 }
 sheets:=[]xlsx.Sheet{{Name:fullSyncRegistry,Rows:index}}
 for i,loc:=range locs {
  sheet:=worksheetName(i+1,loc.Title)
  index=append(index,[]string{loc.ID,loc.Title,sheet})
  sheets=append(sheets,xlsx.Sheet{Name:sheet,Rows:locationSheet(loc)})
 }
 sheets[0].Rows=index
 return xlsx.Write(sheets)
}
func exactCell(rows [][]string,row,col int)string{
 if row<0||row>=len(rows){return ""}
 return readValue(rows[row],col)
}
func parseSyncLocation(rows [][]string,sheet string)(catalog.Location,error){
 required:=[]struct{row int;label string}{
  {1,"ID (пустой для новой локации)"},{2,"Название"},
  {3,"Категория"},{4,"Статус"},{5,"Ориентир"},
  {6,"Описание"},{7,"Ссылка на карту"},
  {8,"Широта (необязательно)"},{9,"Долгота (необязательно)"},{10,"Рейтинг"},
 }
 for _,r:=range required{
  if exactCell(rows,r.row,0)!=r.label{return catalog.Location{},fmt.Errorf("лист «%s»: неверная структура поля «%s»",sheet,r.label)}
 }
 if exactCell(rows,15,0)!="Порядок" || exactCell(rows,15,1)!="Название раздела"{
  return catalog.Location{},fmt.Errorf("лист «%s»: отсутствует заголовок разделов",sheet)
 }
 lat,err:=parseCoordinate(exactCell(rows,8,1),-90,90)
 if err!=nil{return catalog.Location{},fmt.Errorf("лист «%s»: неверная широта",sheet)}
 lon,err:=parseCoordinate(exactCell(rows,9,1),-180,180)
 if err!=nil{return catalog.Location{},fmt.Errorf("лист «%s»: неверная долгота",sheet)}
 if (lat==nil)!=(lon==nil){return catalog.Location{},fmt.Errorf("лист «%s»: заполните обе координаты или оставьте обе пустыми",sheet)}
 loc:=catalog.Location{
  ID:exactCell(rows,1,1),Title:exactCell(rows,2,1),Category:exactCell(rows,3,1),
  LineStatus:exactCell(rows,4,1),Landmarks:exactCell(rows,5,1),Description:exactCell(rows,6,1),
  MapURL:exactCell(rows,7,1),Latitude:lat,Longitude:lon,Rating:exactCell(rows,10,1),
  Sections:[]catalog.CardSection{},
 }
 if loc.Title==""{return loc,fmt.Errorf("лист «%s»: отсутствует название локации",sheet)}
 seenPositions:=map[int]bool{}
 for i:=16;i<len(rows);i++{
  row:=rows[i]
  if blankRow(row){continue}
  position,err:=strconv.Atoi(readValue(row,0))
  if err!=nil||position<1||position>15||seenPositions[position]{
   return loc,fmt.Errorf("лист «%s», строка %d: порядок раздела должен быть уникальным от 1 до 15",sheet,i+1)
  }
  seenPositions[position]=true
  title:=readValue(row,1)
  if title==""{return loc,fmt.Errorf("лист «%s», строка %d: отсутствует название раздела",sheet,i+1)}
  v:=strings.ToLower(readValue(row,3))
  if v!=""&&v!="да"&&v!="нет"&&v!="yes"&&v!="no"&&v!="true"&&v!="false"&&v!="1"&&v!="0"{
   return loc,fmt.Errorf("лист «%s», строка %d: показывать = Да или Нет",sheet,i+1)
  }
  enabled:=v==""||v=="да"||v=="yes"||v=="true"||v=="1"
  sectionID:=readValue(row,4)
  if sectionID==""{sectionID=fmt.Sprintf("section-%02d",position)}
  loc.Sections=append(loc.Sections,catalog.CardSection{
   ID:sectionID,Title:title,Body:readValue(row,2),Enabled:enabled,
  })
  if len(loc.Sections)>15{return loc,fmt.Errorf("лист «%s»: максимум 15 разделов",sheet)}
 }
 sort.Slice(loc.Sections,func(i,j int)bool{return loc.Sections[i].ID<loc.Sections[j].ID})
 if err:=catalog.Revision(loc).Validate();err!=nil{return loc,fmt.Errorf("лист «%s»: %w",sheet,err)}
 return loc,nil
}
func parseFullCatalog(data []byte,store *catalog.Store)(catalogSyncPreview,error){
 var preview catalogSyncPreview
 names,err:=xlsx.SheetNames(data);if err!=nil{return preview,err}
 if len(names)<2||len(names)>maxSyncLocations+1{return preview,fmt.Errorf("неверное число листов: %d",len(names))}
 if names[0]!=fullSyncRegistry{return preview,fmt.Errorf("первый лист должен называться «%s»",fullSyncRegistry)}
 sheets,err:=xlsx.Read(data,names...);if err!=nil{return preview,err}
 registry:=sheets[fullSyncRegistry]
 if exactCell(registry,0,0)!=fullSyncMarker{return preview,fmt.Errorf("файл не является полной выгрузкой Karty; скачайте её в админке")}
 digest:=exactCell(registry,0,1)
 if digest!=store.CatalogFingerprint(){return preview,fmt.Errorf("каталог изменился с момента выгрузки; скачайте свежий Excel")}
 preview.Fingerprint=digest
 original:=map[string]catalog.Location{}
 for _,loc:=range store.List(){original[loc.ID]=loc}
 seenIDs:=map[string]bool{}
 seenNames:=map[string]bool{}
 for _,name:=range names[1:]{
  loc,err:=parseSyncLocation(sheets[name],name)
  if err!=nil{return preview,err}
  if loc.ID!="" {
   if seenIDs[loc.ID]{return preview,fmt.Errorf("ID %s присутствует в нескольких вкладках",loc.ID)}
   seenIDs[loc.ID]=true
   prior,ok:=original[loc.ID]
   if !ok{return preview,fmt.Errorf("лист «%s»: неизвестный ID %s. Для новой карточки очистите B2",name,loc.ID)}
   // Preserve server-managed metadata excluded from the Excel format.
   loc.Filename,loc.ImageExt,loc.ImageURL=prior.Filename,prior.ImageExt,prior.ImageURL
   loc.SourceModified=prior.SourceModified
   loc.Traffic,loc.Payers,loc.Opportunity,loc.Conclusion=prior.Traffic,prior.Payers,prior.Opportunity,prior.Conclusion
   if catalog.RevisionHash(catalog.Revision(loc))!=catalog.RevisionHash(catalog.Revision(prior)){
    preview.Updated++
    preview.UpdatedNames=append(preview.UpdatedNames,loc.Title)
   }else{preview.Unchanged++}
  }else{
   id,e:=catalog.NewAdminLocationID();if e!=nil{return preview,e}
   loc.ID=id
   preview.Added++
   preview.AddedNames=append(preview.AddedNames,loc.Title)
  }
  key:=catalog.Normalize(loc.Title)
  if key==""||seenNames[key]{return preview,fmt.Errorf("повторяющееся название локации «%s»",loc.Title)}
  seenNames[key]=true
  preview.Desired=append(preview.Desired,loc)
 }
 for id,loc:=range original{
  if !seenIDs[id]{
   preview.Removals=append(preview.Removals,id)
   preview.RemovedNames=append(preview.RemovedNames,fmt.Sprintf("%s · %s",id,loc.Title))
  }
 }
 sort.Strings(preview.Removals)
 sort.Strings(preview.RemovedNames)
 return preview,nil
}
func compactLocationNames(names []string,limit int)string{
 if len(names)==0{return "нет"}
 if len(names)>limit {
  return html.EscapeString(strings.Join(names[:limit],", "))+fmt.Sprintf(" и ещё %d",len(names)-limit)
 }
 return html.EscapeString(strings.Join(names,", "))
}
func (b *Bot) receiveFullSyncExcel(ctx context.Context,m *message)error{
 if m.Document==nil{return nil}
 session:=b.adminSession(m.From.ID)
 if !strings.HasSuffix(strings.ToLower(m.Document.FileName),".xlsx"){
  return b.Send(ctx,m.Chat.ID,"Нужен файл с расширением .xlsx.",nil)
 }
 data,err:=b.getXLSX(ctx,m.Document.FileID,m.Document.FileSize)
 if err!=nil{return b.Send(ctx,m.Chat.ID,"❌ Не удалось получить Excel: "+html.EscapeString(err.Error()),nil)}
 preview,err:=parseFullCatalog(data,b.store)
 if err!=nil {
  return b.Send(ctx,m.Chat.ID,"❌ <b>Проверка не пройдена</b>\n\n"+html.EscapeString(err.Error())+"\n\nНичего не изменено. Исправьте файл и отправьте снова.",nil)
 }
 session.AwaitingCatalogSync=false
 session.PendingCatalogSync=preview.Desired
 session.PendingCatalogRemovals=preview.Removals
 session.CatalogSyncFingerprint=preview.Fingerprint
 session.CatalogSyncDeleteConfirmed=false
 return b.Send(ctx,m.Chat.ID,fmt.Sprintf(
  "📊 <b>Полная синхронизация каталога</b>\n\n✅ Без изменений: %d\n✏️ Изменено: %d\n➕ Добавлено: %d\n🗑 На удаление: %d\n\n<b>Изменения:</b> %s\n<b>Новые:</b> %s\n<b>Удаления:</b> %s\n\nПроверьте список. Пока ничего не опубликовано.",
  preview.Unchanged,preview.Updated,preview.Added,len(preview.Removals),
  compactLocationNames(preview.UpdatedNames,6),compactLocationNames(preview.AddedNames,6),compactLocationNames(preview.RemovedNames,8),
 ),markup(
  []Button{callbackButton("✅ Продолжить синхронизацию","adm:xlsx:sync:confirm")},
  []Button{callbackButton("❌ Отмена","adm:xlsx:sync:cancel")},
 ))
}
func (b *Bot) handleFullSyncCallback(ctx context.Context,chatID,userID int64,action string)(bool,error){
 if !strings.HasPrefix(action,"adm:xlsx:sync:"){return false,nil}
 if !b.isAdmin(userID)||chatID!=userID{return true,nil}
 session:=b.adminSession(userID)
 switch action{
 case "adm:xlsx:sync:export":
  raw,err:=exportFullCatalog(b.store)
  if err!=nil{return true,b.Send(ctx,chatID,"❌ Не удалось создать Excel: "+html.EscapeString(err.Error()),nil)}
  return true,b.sendAdminDocument(ctx,chatID,raw,"Karty_Полный_каталог_"+time.Now().UTC().Format("20060102")+".xlsx")
 case "adm:xlsx:sync:upload":
  session.AwaitingExcel=false
  session.AwaitingCatalogSync=true
  session.PendingCatalogSync=nil
  session.PendingCatalogRemovals=nil
  session.CatalogSyncFingerprint=""
  return true,b.Send(ctx,chatID,"📥 <b>Полная синхронизация</b>\n\nОтправьте документ XLSX, который ранее скачали кнопкой «Полный каталог». Один лист = одна локация. Удалённая вкладка означает окончательное удаление после отдельного подтверждения.\n\nПроверим все изменения до применения.",markup([]Button{callbackButton("❌ Отмена","adm:xlsx:sync:cancel")}))
 case "adm:xlsx:sync:cancel":
  session.AwaitingCatalogSync=false
  session.PendingCatalogSync=nil
  session.PendingCatalogRemovals=nil
  session.CatalogSyncFingerprint=""
  session.CatalogSyncDeleteConfirmed=false
  return true,b.showExcelMenu(ctx,chatID)
 case "adm:xlsx:sync:confirm":
  if session.CatalogSyncFingerprint==""{return true,b.showExcelMenu(ctx,chatID)}
  if len(session.PendingCatalogRemovals)>0 {
   session.CatalogSyncDeleteConfirmed=true
   original:=map[string]string{}
   for _,loc:=range b.store.List(){original[loc.ID]=loc.Title}
   details:=[]string{}
   for _,id:=range session.PendingCatalogRemovals{details=append(details,id+" · "+original[id])}
   return true,b.Send(ctx,chatID,fmt.Sprintf("⚠️ <b>Подтвердите окончательное удаление</b>\n\nСледующие локации исчезнут из каталога без корзины:\n%s\n\nВсего на удаление: %d. Будет создана защитная ZIP-копия перед синхронизацией.",html.EscapeString(strings.Join(details,"\n")),len(details)),markup(
    []Button{callbackButton("🗑 Подтверждаю удаления и синхронизацию","adm:xlsx:sync:apply")},
    []Button{callbackButton("❌ Отмена","adm:xlsx:sync:cancel")},
   ))
  }
  session.CatalogSyncDeleteConfirmed=true
  return true,b.applyFullSync(ctx,chatID,userID)
 case "adm:xlsx:sync:apply":
  if !session.CatalogSyncDeleteConfirmed||len(session.PendingCatalogRemovals)==0{return true,b.showExcelMenu(ctx,chatID)}
  return true,b.applyFullSync(ctx,chatID,userID)
 }
 return true,b.showExcelMenu(ctx,chatID)
}
func (b *Bot) applyFullSync(ctx context.Context,chatID,userID int64)error{
 session:=b.adminSession(userID)
 if session.CatalogSyncFingerprint==""||!session.CatalogSyncDeleteConfirmed {
  return b.Send(ctx,chatID,"Подтверждение устарело, загрузите файл снова.",nil)
 }
 desired:=session.PendingCatalogSync
 removed:=session.PendingCatalogRemovals
 fingerprint:=session.CatalogSyncFingerprint
 // Single-use approval. Replaying an old callback cannot delete more entries.
 session.PendingCatalogSync=nil
 session.PendingCatalogRemovals=nil
 session.CatalogSyncFingerprint=""
 session.CatalogSyncDeleteConfirmed=false
 b.backupMu.Lock()
 defer b.backupMu.Unlock()
 if b.store.CatalogFingerprint()!=fingerprint{
  return b.Send(ctx,chatID,"❌ Каталог изменился после выгрузки. Скачайте новый Excel и повторите синхронизацию.",nil)
 }
 data,err:=b.snapshotBackup()
 if err==nil{_,err=adminbackup.Examine(data)}
 if err!=nil{return b.Send(ctx,chatID,"❌ Защитная копия не создана. Синхронизация отменена: "+html.EscapeString(err.Error()),nil)}
 filename:="pre-catalog-sync-"+time.Now().UTC().Format("20060102-150405.000000000")+".zip"
 if err=atomicAdminFile(filepath.Join(b.autoBackupDirectory(),filename),data);err!=nil{
  return b.Send(ctx,chatID,"❌ Не удалось сохранить защитную копию. Синхронизация отменена.",nil)
 }
 if err=b.store.ApplyCatalogSync(desired,removed,fingerprint);err!=nil{
  return b.Send(ctx,chatID,"❌ Синхронизация не выполнена: "+html.EscapeString(err.Error()),nil)
 }
 failedFavorites:=false
 for _,id:=range removed{
  if b.favorites!=nil {
   if err=b.favorites.RemoveLocationEverywhere(id);err!=nil{failedFavorites=true}
  }
 }
 warning:=""
 if failedFavorites{warning="\n⚠️ Не все избранные обновились; нужно повторить очистку."}
 return b.Send(ctx,chatID,fmt.Sprintf("✅ <b>Каталог синхронизирован</b>\n\nЛокаций теперь: %d\nУдалено: %d\n\nЗащитный ZIP: <code>%s</code>%s",len(desired),len(removed),html.EscapeString(filename),warning),markup(
  []Button{callbackButton("📍 К локациям","adm:locations:0")},
 ))
}
