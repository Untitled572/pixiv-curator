// Package admin serves the embedded, same-origin management interface.
package admin

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html static/*
var assets embed.FS

var page = template.Must(template.ParseFS(assets, "templates/*.html"))

// Login renders the token entry page. Token submission is handled by the
// authenticated POST /admin/login endpoint registered by httpapi.Auth.
func Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "login.html", struct {
		CSS template.CSS
		JS  template.JS
	}{template.CSS(readAsset("static/admin.css")), template.JS(readAsset("static/login.js"))}); err != nil {
		return
	}
}

// Home renders the management shell. API requests remain protected by the
// HTTP authentication middleware mounted by the application.
func Home(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "home.html", struct {
		CSS template.CSS
		JS  template.JS
	}{template.CSS(readAsset("static/admin.css")), template.JS(readAsset("static/admin.js"))}); err != nil {
		return
	}
}

func readAsset(name string) string {
	body, err := assets.ReadFile(name)
	if err != nil {
		return ""
	}
	return string(body)
}
