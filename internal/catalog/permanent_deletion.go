package catalog

import (
 "encoding/json"
 "errors"
 "fmt"
 "os"
)

// PurgeLocation permanently removes custom card content and records an
// irreversible visibility tombstone for curated (bundled) location IDs.
// Bundled definitions are part of the executable and cannot be erased at
// runtime; tombstones ensure they never reappear after sync or restart.
func (s *Store) PurgeLocation(id string) error {
 if !ValidID(id) { return errors.New("неверный ID локации") }
 s.adminMu.Lock()
 defer s.adminMu.Unlock()
 if _,ok:=s.Find(id);!ok{return errors.New("локация отсутствует в активном каталоге")}
 tombstones,err:=ReadDeletedLocations(s.DeletedLocationsPath())
 if err!=nil{return err}
 if tombstones[id] {return errors.New("локация уже удалена")}
 custom,err:=ReadAdminLocations(s.AdminLocationsPath())
 if err!=nil{return err}
 cards,err:=s.adminCardsOnDisk()
 if err!=nil{return err}
 // Stage all changes before persisting anything.
 delete(custom,id)
 delete(cards,id)
 tombstones[id]=true
 if err:=ValidateDeletedLocations(tombstones);err!=nil{return err}
 if err:=ValidateAdminLocations(custom);err!=nil{return err}
 customData,err:=json.MarshalIndent(custom,"","  ");if err!=nil{return err}
 cardData,err:=json.MarshalIndent(cards,"","  ");if err!=nil{return err}
 deletedData,err:=json.MarshalIndent(tombstones,"","  ");if err!=nil{return err}
 type diskFile struct{path string; data []byte; old []byte; absent bool}
 paths:=[]diskFile{{path:s.AdminLocationsPath(),data:customData},{path:s.AdminCardsPath(),data:cardData},{path:s.DeletedLocationsPath(),data:deletedData}}
 for i:=range paths {
  paths[i].old,err=os.ReadFile(paths[i].path)
  if errors.Is(err,os.ErrNotExist){paths[i].absent=true;err=nil}
  if err!=nil{return err}
 }
 // The tombstone must be written first so a partial write can never
 // expose an entry that was successfully purged from its source.
 written:=0
 for _,i:=range []int{2,0,1} {
  if err=writePrivateAtomic(paths[i].path,paths[i].data);err!=nil {
   // Roll back all modified files, preserving current state.
   for _,j:=range []int{1,0,2} {
    if written==0 {break}
    if j==2 && written<1 || j==0 && written<2 || j==1 && written<3 {continue}
    if paths[j].absent {_=os.Remove(paths[j].path)}else{_ = writePrivateAtomic(paths[j].path,paths[j].old)}
   }
   return fmt.Errorf("не удалось окончательно удалить локацию: %w",err)
  }
  written++
 }
 if err=s.Reload();err!=nil{
  // Keep the tombstone to avoid accidentally exposing a purged location.
  return fmt.Errorf("локация скрыта, но каталог не перезагрузился: %w",err)
 }
 return nil
}
