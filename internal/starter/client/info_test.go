package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

func infoClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return New(&group.Group{Coordinator: ts.URL, Token: "tok"})
}

func TestInfo(t *testing.T) {
	var auth string
	c := infoClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"title":"Wandergruppe","description":"Survival\nmit Freunden","members":3,"you":"angefragt"}`))
	})
	gi, err := c.Info(context.Background())
	if err != nil || gi.Title != "Wandergruppe" || gi.Members != 3 || gi.You != api.YouRequested || auth != "Bearer tok" {
		t.Fatalf("%+v %v %q", gi, err, auth)
	}
}

func TestInfoStreng(t *testing.T) {
	bad := map[string]string{
		"müll":             `kein json`,
		"leer":             ``,
		"zustand":          `{"title":"t","members":1,"you":"admin"}`,
		"kein zustand":     `{"title":"t","members":1}`,
		"negativ":          `{"title":"t","members":-1,"you":"keiner"}`,
		"riesig":           `{"title":"t","members":99999999,"you":"keiner"}`,
		"titel lang":       `{"title":"` + strings.Repeat("x", 101) + `","members":1,"you":"keiner"}`,
		"beschr lang":      `{"title":"t","description":"` + strings.Repeat("x", 501) + `","members":1,"you":"keiner"}`,
		"steuerzeichen":    `{"title":"a\u0000b","members":1,"you":"keiner"}`,
		"umbruch im titel": `{"title":"a\nb","members":1,"you":"keiner"}`,
		"bidi":             `{"title":"a‮b","members":1,"you":"keiner"}`,
		"escape":           `{"title":"t","description":"\u001b[31m","members":1,"you":"keiner"}`,
		"zu gross":         `{"title":"t","description":"` + strings.Repeat(" ", maxInfoBody) + `","members":1,"you":"keiner"}`,
	}
	for name, body := range bad {
		c := infoClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
		if gi, err := c.Info(context.Background()); err == nil {
			t.Errorf("%s: angenommen: %+v", name, gi)
		}
	}
	// HTML ist erlaubter Text; die Oberfläche fügt ihn nie als HTML ein.
	c := infoClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"title":"<b>x</b>","members":0,"you":"keiner"}`))
	})
	if gi, err := c.Info(context.Background()); err != nil || gi.Title != "<b>x</b>" {
		t.Errorf("%+v %v", gi, err)
	}
}

func TestInfoFehlerstatus(t *testing.T) {
	for _, code := range []int{401, 403, 404, 429, 500, 503} {
		c := infoClient(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", code) })
		_, err := c.Info(context.Background())
		se, ok := err.(*StatusError)
		if !ok || se.StatusCode != code {
			t.Errorf("%d: %v", code, err)
		}
	}
}
