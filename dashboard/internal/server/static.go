package server

import (
	"io/fs"
	"net/http"
)

// staticHandler serves the embedded assets under /static/. Directory listings
// are disabled: a request for a directory 404s rather than exposing the asset
// inventory (see noDirFS).
func (a *app) staticHandler() http.Handler {
	return http.StripPrefix("/static/", http.FileServerFS(noDirFS{a.static}))
}

// noDirFS wraps an fs.FS and hides directories: opening one returns
// fs.ErrNotExist, so http.FileServerFS returns 404 instead of an autoindex
// listing. Files are served unchanged.
type noDirFS struct{ fsys fs.FS }

func (f noDirFS) Open(name string) (fs.File, error) {
	file, err := f.fsys.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if info.IsDir() {
		file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}
