package telegram

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type fakeYandexRedirect struct { calls int; location string }
func (t *fakeYandexRedirect) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	h:=make(http.Header)
	h.Set("Location",t.location)
	return &http.Response{StatusCode:http.StatusMovedPermanently,Header:h,Body:io.NopCloser(strings.NewReader("")),Request:req},nil
}
func TestSharedYandexMapLinksAndCoordinates(t *testing.T) {
	cases:=[]struct{input string; lat,lon float64}{
		{"https://yandex.ru/maps/?ll=37.700%2C55.600&pt=37.668590455%2C55.6921867213,pm2rdm",55.6921867213,37.668590455},
		{"https://yandex.ru/maps/?whatshere%5Bpoint%5D=37.617%2C55.757&ll=37.61%2C55.75",55.757,37.617},
		{"https://yandex.ru/maps/?ll=37.615%2C55.751",55.751,37.615},
		{"55.751, 37.618",55.751,37.618},
		{"37.618, 55.751",55.751,37.618},
	}
	for _,test:=range cases{
		p,err:=resolveSharedMapPoint(context.Background(),nil,test.input)
		if err!=nil||p.Latitude!=test.lat||p.Longitude!=test.lon{
			t.Fatalf("bad point for %s: %+v %v",test.input,p,err)
		}
	}
	redirect:=&fakeYandexRedirect{location:"/maps/?whatshere%5Bpoint%5D=37.617%2C55.757"}
	p,err:=resolveSharedMapPoint(context.Background(),&http.Client{Transport:redirect},"https://yandex.ru/maps/-/CCQtjTBt1A")
	if err!=nil||p.Latitude!=55.757||p.Longitude!=37.617||redirect.calls!=1{
		t.Fatalf("short Yandex URL not expanded: %+v %v calls=%d",p,err,redirect.calls)
	}
}
func TestYandexMapLinkSafetyAndSearchIsolation(t *testing.T) {
	for _,value:=range []string{
		"https://evil.com/maps/?ll=37,55",
		"https://yandex.ru.evil.com/maps/?ll=37,55",
		"http://yandex.ru/maps/?ll=37,55",
		"https://yandex.ru:444/maps/?ll=37,55",
		"https://yandex.ru/maps/?ll=xyz",
		"https://yandex.ru/maps/?pt=200,95",
		"https://yandex.ru/maps/?um=constructor%3Amap",
	}{
		if p,err:=resolveSharedMapPoint(context.Background(),nil,value);err==nil{
			t.Errorf("unsafe or ambiguous map URL %q accepted: %+v",value,p)
		}
	}
	redirect:=&fakeYandexRedirect{location:"https://example.com/maps/?ll=37%2C55"}
	if _,err:=resolveSharedMapPoint(context.Background(),&http.Client{Transport:redirect},"https://yandex.ru/maps/-/ABC");err==nil{t.Fatal("external redirect accepted")}
	if redirect.calls!=1{t.Fatal("attempted to follow an external redirect")}
	if _,yes:=sharedMapInput("Метро Троицк");yes{t.Fatal("normal name search was captured")}
	u,_:=url.Parse("https://yandex.ru/maps/-/XYZ")
	if !allowedMapURL(u){t.Fatal("short Yandex Maps URL blocked")}
}
func TestSharedYandexLinkGuideAndFallback(t *testing.T) {
	b,tr:=makeInlineTestBot(t)
	if err:=b.requestLocation(context.Background(),99);err!=nil{t.Fatal(err)}
	text,_:=tr.payload["text"].(string)
	for _,want:=range []string{"Яндекс Карты","Что здесь?","Поделиться","Скопировать ссылку","55.751, 37.618"}{
		if !strings.Contains(text,want){t.Errorf("guide missing %q: %q",want,text)}
	}
	if err:=b.handle(context.Background(),update{Message:&message{
		Chat:chat{ID:99,Type:"private"},From:user{ID:7},Text:"https://yandex.ru/maps/?um=constructor%3Atest",
	}});err!=nil{t.Fatal(err)}
	text,_=tr.payload["text"].(string)
	if !strings.Contains(text,"Не получилось определить точку")||!strings.Contains(text,"координаты"){
		t.Fatalf("invalid map link did not produce fallback: %q",text)
	}
}
