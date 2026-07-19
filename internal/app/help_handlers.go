package app

import "net/http"

type HelpView struct{ Navigation []NavigationItem }

func (a *App) help(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	if _, ok := a.sessionScope(r); !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodHead {
		writeHTMLHead(w)
		return
	}
	a.renderTemplate(r.Context(), w, "help.html", HelpView{Navigation: navigationForPath("/help")})
}
