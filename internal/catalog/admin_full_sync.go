package catalog

import (
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "os"
)

// CatalogFingerprint identifies the exact visible catalog used to produce an
// export. It prevents a stale spreadsheet from overwriting subsequent edits.
func (s *Store) CatalogFingerprint() string {
 data,_:=json.Marshal(s.List())
 hash:=sha256.Sum256(data)
 return hex.EncodeToString(hash[:])
}

// ApplyCatalogSync replaces the visible catalog contents in a single
// administrator-authorized operation. Every current visible ID must be either
// present in desired or explicitly included in removals, so an accidentally
// partial workbook cannot silently erase unrelated cards.
// It does not modify historic requests or analytics.
func (s *Store) ApplyCatalogSync(desired []Location, removals []string, expectedFingerprint string) error {
 if len(expectedFingerprint)!=64||len(desired)+len(removals)>750 {
  return errors.New("неверные данные синхронизации каталога")
 }
 s.adminMu.Lock()
 defer s.adminMu.Unlock()
 if s.CatalogFingerprint()!=expectedFingerprint {
  return errors.New("каталог изменился после скачивания Excel; скачайте свежую выгрузку")
 }
 current:=s.List()
 active:=make(map[string]Location,len(current))
 names:=map[string]bool{}
 for _,loc:=range current{active[loc.ID]=loc}
 removed:=map[string]bool{}
 for _,id:=range removals{
  if !ValidID(id)||removed[id] {return errors.New("повторяющийся или неверный ID на удаление")}
  if _,ok:=active[id];!ok{return fmt.Errorf("локация %s уже отсутствует в каталоге",id)}
  removed[id]=true
 }
 desiredIDs:=map[string]bool{}
 custom,err:=ReadAdminLocations(s.AdminLocationsPath());if err!=nil{return err}
 revisions,err:=s.adminCardsOnDisk();if err!=nil{return err}
 tombstones,err:=ReadDeletedLocations(s.DeletedLocationsPath());if err!=nil{return err}
 for _,loc:=range desired{
  if !ValidID(loc.ID)||desiredIDs[loc.ID]||removed[loc.ID] {
   return fmt.Errorf("неверный или повторный ID %s",loc.ID)
  }
  desiredIDs[loc.ID]=true
  if tombstones[loc.ID] {return fmt.Errorf("локация %s ранее удалена навсегда",loc.ID)}
  title:=Normalize(loc.Title)
  if title==""||names[title] {return fmt.Errorf("повторяющееся или пустое название «%s»",loc.Title)}
  names[title]=true
  if err:=Revision(loc).Validate();err!=nil{return fmt.Errorf("%s: %w",loc.ID,err)}
  previous,exists:=active[loc.ID]
  if !exists && !IsAdminLocationID(loc.ID){return fmt.Errorf("новая локация %s должна иметь ID custom-",loc.ID)}
  if !exists {
   if _,found:=custom[loc.ID];found{return fmt.Errorf("новый ID %s уже занят",loc.ID)}
  }
  if IsAdminLocationID(loc.ID){
   if exists {
    // User-managed image metadata is preserved on updates.
    loc.Filename,loc.ImageExt,loc.ImageURL=previous.Filename,previous.ImageExt,previous.ImageURL
   }else{
    loc.Filename,loc.ImageExt,loc.ImageURL="","",""
   }
   custom[loc.ID]=loc
   delete(revisions,loc.ID)
  }else{
   revisions[loc.ID]=Revision(loc)
  }
 }
 for _,loc:=range current{
  if !desiredIDs[loc.ID] && !removed[loc.ID] {
   return fmt.Errorf("в Excel отсутствует локация %s без разрешённого удаления",loc.ID)
  }
 }
 for id:=range removed{
  tombstones[id]=true
  delete(custom,id)
  delete(revisions,id)
 }
 if len(custom)>500{return errors.New("достигнут лимит 500 дополнительных локаций")}
 if err:=ValidateAdminLocations(custom);err!=nil{return err}
 if err:=ValidateDeletedLocations(tombstones);err!=nil{return err}
 customData,err:=json.MarshalIndent(custom,"","  ");if err!=nil{return err}
 revisionsData,err:=json.MarshalIndent(revisions,"","  ");if err!=nil{return err}
 deletedData,err:=json.MarshalIndent(tombstones,"","  ");if err!=nil{return err}
 type fileChange struct{
  path string
  contents []byte
  old []byte
  absent bool
 }
 changes:=[]fileChange{
  {path:s.DeletedLocationsPath(),contents:deletedData},
  {path:s.AdminLocationsPath(),contents:customData},
  {path:s.AdminCardsPath(),contents:revisionsData},
 }
 for i:=range changes{
  changes[i].old,err=os.ReadFile(changes[i].path)
  if errors.Is(err,os.ErrNotExist){changes[i].absent=true;err=nil}
  if err!=nil{return err}
 }
 rollback:=func(count int){
  for i:=count-1;i>=0;i--{
   if changes[i].absent {_=os.Remove(changes[i].path)} else {_=writePrivateAtomic(changes[i].path,changes[i].old)}
  }
  _=s.Reload()
 }
 for i,change:=range changes{
  if err=writePrivateAtomic(change.path,change.contents);err!=nil{
   rollback(i)
   return fmt.Errorf("синхронизация отменена: %w",err)
  }
 }
 if err=s.Reload();err!=nil{
  rollback(len(changes))
  return fmt.Errorf("синхронизация отменена при перезагрузке каталога: %w",err)
 }
 // A count sanity check catches a silently missing runtime card.
 if len(s.List())!=len(desired){
  rollback(len(changes))
  return errors.New("контроль количества локаций не пройден; синхронизация отменена")
 }
 return nil
}
