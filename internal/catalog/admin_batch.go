package catalog

import (
 "encoding/json"
 "errors"
 "fmt"
 "os"
 "strings"
)

// CreateAdminLocations publishes a validated import as one atomic runtime file.
// If any entry fails validation, nothing is published.
func (s *Store) CreateAdminLocations(locations []Location) error {
 if len(locations)==0||len(locations)>50{return errors.New("укажите от 1 до 50 локаций")}
 s.adminMu.Lock()
 defer s.adminMu.Unlock()
 entries,err:=ReadAdminLocations(s.AdminLocationsPath());if err!=nil{return err}
 if len(entries)+len(locations)>500{return errors.New("превышен лимит дополнительных локаций")}
 known:=map[string]bool{}
 for _,item:=range s.List(){known[strings.ToLower(strings.TrimSpace(item.Title))]=true}
 for _,loc:=range locations{
  if !IsAdminLocationID(loc.ID){return fmt.Errorf("некорректный ID новой локации: %s",loc.ID)}
  if _,ok:=entries[loc.ID];ok{return errors.New("ID локации уже занят")}
  if err:=Revision(loc).Validate();err!=nil{return fmt.Errorf("%s: %w",loc.Title,err)}
  title:=strings.ToLower(strings.TrimSpace(loc.Title))
  if known[title]{return fmt.Errorf("локация «%s» уже существует",loc.Title)}
  known[title]=true
  // Imported images are not considered trusted catalog metadata.
  loc.ImageURL,loc.ImageExt,loc.Filename="","",""
  entries[loc.ID]=loc
 }
 data,err:=json.MarshalIndent(entries,"","  ");if err!=nil{return err}
 previous,readErr:=os.ReadFile(s.AdminLocationsPath())
 if readErr!=nil&&!errors.Is(readErr,os.ErrNotExist){return readErr}
 if err=writePrivateAtomic(s.AdminLocationsPath(),data);err!=nil{return err}
 if err=s.Reload();err!=nil{
  if errors.Is(readErr,os.ErrNotExist){_ = os.Remove(s.AdminLocationsPath())}else{_ = writePrivateAtomic(s.AdminLocationsPath(),previous)}
  _ = s.Reload()
  return fmt.Errorf("импорт отменён, не удалось обновить каталог: %w",err)
 }
 return nil
}
