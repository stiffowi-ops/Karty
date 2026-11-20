// Package xlsx provides a bounded, dependency-free reader and writer for
// spreadsheet files used to exchange location catalog data with administrators.
package xlsx

import (
 "archive/zip"
 "bytes"
 "encoding/xml"
 "errors"
 "fmt"
 "io"
 "path"
 "strconv"
 "strings"
)

const MaxFileSize = 5 << 20
const MaxInflatedSize = 25 << 20

type Sheet struct {
 Name string
 Rows [][]string
}

type workbook struct {
 Sheets []struct {
  Name string `xml:"name,attr"`
  RID string `xml:"id,attr"`
 } `xml:"sheets>sheet"`
}
type relationships struct {
 Entries []struct {
  ID string `xml:"Id,attr"`
  Target string `xml:"Target,attr"`
 } `xml:"Relationship"`
}
type richText struct {
 Text string `xml:"t"`
 Runs []struct{Text string `xml:"t"`} `xml:"r"`
}
func (r richText) String() string {
 if len(r.Runs)==0 {return r.Text}
 var b strings.Builder
 if r.Text!="" {b.WriteString(r.Text)}
 for _,part:=range r.Runs {b.WriteString(part.Text)}
 return b.String()
}
type sharedStrings struct {
 Items []richText `xml:"si"`
}
type cell struct {
 Ref string `xml:"r,attr"`
 Type string `xml:"t,attr"`
 Value string `xml:"v"`
 Inline richText `xml:"is"`
 Formula string `xml:"f"`
}
type sheetXML struct {
 Rows []struct {
  Index int `xml:"r,attr"`
  Cells []cell `xml:"c"`
 } `xml:"sheetData>row"`
}
func readEntry(f *zip.File, limit int64)([]byte,error){
 if f.UncompressedSize64>uint64(limit) {return nil,errors.New("слишком большой лист Excel")}
 r,e:=f.Open();if e!=nil{return nil,e};defer r.Close()
 data,e:=io.ReadAll(io.LimitReader(r,limit+1))
 if e!=nil{return nil,e}
 if int64(len(data))>limit{return nil,errors.New("слишком большой лист Excel")}
 return data,nil
}
func columnIndex(ref string)(int,error){
 col:=0
 for _,r:=range ref {
  if r>='A'&&r<='Z' {col=col*26+int(r-'A')+1
  }else if r>='a'&&r<='z'{col=col*26+int(r-'a')+1
  }else{break}
 }
 if col<=0||col>200 {return 0,fmt.Errorf("неверный адрес ячейки %q",ref)}
 return col-1,nil
}
// Read loads required named worksheets, including shared and inline strings.
// It rejects formulas, huge files, unexpected paths and malformed references.
func Read(data []byte, required ...string)(map[string][][]string,error){
 if len(data)==0||len(data)>MaxFileSize{return nil,errors.New("Excel-файл должен быть не больше 5 МБ")}
 zr,e:=zip.NewReader(bytes.NewReader(data),int64(len(data)));if e!=nil{return nil,fmt.Errorf("не удалось прочитать XLSX: %w",e)}
 files:=map[string]*zip.File{}
 var inflated uint64
 for _,f:=range zr.File {
  name:=path.Clean(strings.TrimPrefix(f.Name,"/"))
  if strings.HasPrefix(name,"../")||name==".."||strings.Contains(name,`\\`){return nil,errors.New("некорректная структура XLSX")}
  if f.UncompressedSize64>MaxInflatedSize{return nil,errors.New("слишком большой лист Excel")}
  inflated+=f.UncompressedSize64
  if inflated>MaxInflatedSize{return nil,errors.New("слишком большой XLSX")}
  files[name]=f
 }
 read:=func(name string)([]byte,error){
  f:=files[name];if f==nil{return nil,fmt.Errorf("нет файла %s в XLSX",name)}
  return readEntry(f,MaxInflatedSize)
 }
 raw,e:=read("xl/workbook.xml");if e!=nil{return nil,e}
 var wb workbook
 if e=xml.Unmarshal(raw,&wb);e!=nil{return nil,fmt.Errorf("ошибка книги Excel: %w",e)}
 raw,e=read("xl/_rels/workbook.xml.rels");if e!=nil{return nil,e}
 var rel relationships
 if e=xml.Unmarshal(raw,&rel);e!=nil{return nil,fmt.Errorf("ошибка листов Excel: %w",e)}
 paths:=map[string]string{}
 for _,r:=range rel.Entries{
  target:=strings.TrimPrefix(r.Target,"/")
  if !strings.HasPrefix(target,"xl/") {target=path.Join("xl",target)}
  if strings.HasPrefix(target,"xl/worksheets/"){paths[r.ID]=target}
 }
 stringsTable:=[]string{}
 if f:=files["xl/sharedStrings.xml"];f!=nil {
  raw,e=readEntry(f,MaxInflatedSize);if e!=nil{return nil,e}
  var shared sharedStrings
  if e=xml.Unmarshal(raw,&shared);e!=nil{return nil,fmt.Errorf("ошибка строк Excel: %w",e)}
  for _,v:=range shared.Items{stringsTable=append(stringsTable,v.String())}
 }
 wanted:=map[string]bool{};for _,v:=range required{wanted[v]=true}
 result:=map[string][][]string{}
 for _,entry:=range wb.Sheets {
  if !wanted[entry.Name]{continue}
  target:=paths[entry.RID];if target==""{return nil,fmt.Errorf("не найден лист %q",entry.Name)}
  raw,e=read(target);if e!=nil{return nil,e}
  var sh sheetXML
  if e=xml.Unmarshal(raw,&sh);e!=nil{return nil,fmt.Errorf("ошибка листа %s: %w",entry.Name,e)}
  if len(sh.Rows)>1200{return nil,errors.New("слишком много строк в Excel")}
  matrix:=make([][]string,0,len(sh.Rows))
  for i,row:=range sh.Rows{
   index:=row.Index
   if index==0{index=i+1}
   if index<1||index>1201{return nil,errors.New("некорректный номер строки Excel")}
   for len(matrix)<index{matrix=append(matrix,[]string{})}
   out:=matrix[index-1]
   for _,c:=range row.Cells{
    if c.Formula!="" {return nil,fmt.Errorf("%s: формулы не допускаются, замените их значениями",entry.Name)}
    col,e:=columnIndex(c.Ref);if e!=nil{return nil,e}
    if col>25{return nil,errors.New("слишком много столбцов Excel")}
    for len(out)<=col{out=append(out,"")}
    value:=c.Value
    switch c.Type {
    case "s":
     n,err:=strconv.Atoi(value);if err!=nil||n<0||n>=len(stringsTable){return nil,errors.New("повреждённая таблица строк Excel")}
     value=stringsTable[n]
    case "inlineStr":
     value=c.Inline.String()
    case "b":
     if value=="1"{value="Да"}else{value="Нет"}
    }
    out[col]=strings.TrimSpace(value)
   }
   matrix[index-1]=out
  }
  result[entry.Name]=matrix
 }
 for name:=range wanted{
  if _,ok:=result[name];!ok{return nil,fmt.Errorf("в Excel отсутствует лист «%s»",name)}
 }
 return result,nil
}
func xmlText(value string)string{
 var out bytes.Buffer
 _=xml.EscapeText(&out,[]byte(value))
 return out.String()
}
func colName(n int)string{
 name:=""
 for n>=0 {name=string(rune('A'+n%26))+name;n=n/26-1}
 return name
}
func sheetData(rows [][]string)[]byte{
 var b strings.Builder
 b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultRowHeight="18"/><sheetData>`)
 for ri, row:=range rows {
  fmt.Fprintf(&b,`<row r="%d">`,ri+1)
  for ci,value:=range row{
   if value==""{continue}
   style:="";if ri==0{style=` s="1"`}
   fmt.Fprintf(&b,`<c r="%s%d" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`,colName(ci),ri+1,style,xmlText(value))
  }
  b.WriteString("</row>")
 }
 b.WriteString("</sheetData></worksheet>")
 return []byte(b.String())
}
// Write creates an Excel workbook without third-party dependencies. All user
// content is explicitly typed as text, preventing formula injection on export.
func Write(sheets []Sheet)([]byte,error){
 if len(sheets)<1||len(sheets)>750{return nil,errors.New("неверное число листов")}
 var out bytes.Buffer
 zw:=zip.NewWriter(&out)
 add:=func(name string,body []byte)error{
  f,e:=zw.Create(name);if e!=nil{return e};_,e=f.Write(body);return e
 }
 var book, rels, overrides strings.Builder
 book.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
 rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
 overrides.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
 for i,sh:=range sheets{
  if len(sh.Rows)>1201{return nil,errors.New("слишком много строк")}
  id:=i+1
  fmt.Fprintf(&book,`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`,xmlText(sh.Name),id,id)
  fmt.Fprintf(&rels,`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`,id,id)
  fmt.Fprintf(&overrides,`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`,id)
  if e:=add(fmt.Sprintf("xl/worksheets/sheet%d.xml",id),sheetData(sh.Rows));e!=nil{return nil,e}
 }
 book.WriteString("</sheets></workbook>")
 fmt.Fprintf(&rels,`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`,len(sheets)+1)
 rels.WriteString("</Relationships>")
 overrides.WriteString("</Types>")
 if e:=add("xl/workbook.xml",[]byte(book.String()));e!=nil{return nil,e}
 if e:=add("xl/_rels/workbook.xml.rels",[]byte(rels.String()));e!=nil{return nil,e}
 if e:=add("[Content_Types].xml",[]byte(overrides.String()));e!=nil{return nil,e}
 if e:=add("_rels/.rels",[]byte(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`));e!=nil{return nil,e}
 if e:=add("xl/styles.xml",[]byte(`<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><color rgb="FFFFFFFF"/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FF126782"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/></cellXfs></styleSheet>`));e!=nil{return nil,e}
 if e:=zw.Close();e!=nil{return nil,e}
 return out.Bytes(),nil
}


 // SheetNames returns all worksheet names in workbook order so a complete
 // catalog export can contain one dedicated sheet for each location.
func SheetNames(data []byte)([]string,error){
 if len(data)==0||len(data)>MaxFileSize{return nil,errors.New("XLSX слишком большой (максимум 5 МБ)")}
 zr,err:=zip.NewReader(bytes.NewReader(data),int64(len(data)));if err!=nil{return nil,err}
 for _,file:=range zr.File{
  if file.Name!="xl/workbook.xml"{continue}
  raw,err:=readEntry(file,MaxInflatedSize);if err!=nil{return nil,err}
  var wb workbook
  if err=xml.Unmarshal(raw,&wb);err!=nil{return nil,err}
  if len(wb.Sheets)==0||len(wb.Sheets)>750{return nil,errors.New("некорректное число листов Excel")}
  names:=make([]string,0,len(wb.Sheets))
  seen:=map[string]bool{}
  for _,sh:=range wb.Sheets{
   key:=strings.ToLower(sh.Name)
   if strings.TrimSpace(sh.Name)==""||seen[key]{return nil,errors.New("пустое или повторяющееся имя вкладки")}
   seen[key]=true
   names=append(names,sh.Name)
  }
  return names,nil
 }
 return nil,errors.New("XLSX не содержит книгу xl/workbook.xml")
}
