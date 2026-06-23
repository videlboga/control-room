package dashboard

import (
	"embed"
	"html/template"
	"net/http"
	"time"
)

//go:embed templates/layout.html templates/index.html templates/project.html templates/epic.html templates/task.html templates/run.html
var templatesFS embed.FS

var pageTemplates map[string]*template.Template

func init() {
	funcMap := template.FuncMap{
		"formatTime": func(t string) string {
			pt, err := time.Parse(time.RFC3339, t)
			if err != nil {
				return t
			}
			return pt.Format("2006-01-02 15:04")
		},
	}
	pageTemplates = map[string]*template.Template{}
	pages := []string{"index", "project", "epic", "task", "run"}
	for _, page := range pages {
		t, err := template.New("layout").Funcs(funcMap).ParseFS(templatesFS,
			"templates/layout.html",
			"templates/"+page+".html",
		)
		if err != nil {
			panic(err)
		}
		pageTemplates[page] = t
	}
}

func render(w http.ResponseWriter, page string, data any) {
	t := pageTemplates[page]
	if t == nil {
		http.Error(w, "template not found: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
