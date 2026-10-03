package auth

import (
	_ "embed"
	"html/template"
	"math"
	"net/http"
)

//go:embed callback.html
var callbackHTML string

var callbackTemplate = template.Must(template.New("callback").Parse(callbackHTML))

// callbackPage is what the browser shows after the authorization server
// redirects to the CLI. Received plays the arrival animation; every other
// outcome shows the mark at rest.
type callbackPage struct {
	Received bool
	Title    string
	Detail   string
	Retry    bool // show the login command under Detail
}

// pixel is one cell of the dither that streams into the mark on success. X and
// Y sit on the mark's diagonal lattice, so a pixel lines up with its diamonds.
type pixel struct {
	X, Y  int
	Color int
	Delay int // milliseconds
}

// streamPixels thins out with distance from the mark. The hash keeps the
// pattern the same on every login.
var streamPixels = func() []pixel {
	const first, last = 10, 60
	var pixels []pixel
	for x := first; x <= last; x++ {
		for y := 1; y <= 9; y++ {
			if (x+y)%2 != 0 {
				continue
			}
			h := uint32(x)*73856093 ^ uint32(y)*19349663
			h ^= h >> 13
			h *= 0x5bd1e995
			h ^= h >> 15
			density := 1 - float64(x-first)/float64(last-first)
			if float64(h%1000)/1000 >= math.Pow(density, 1.5)*0.9 {
				continue
			}
			pixels = append(pixels, pixel{X: x, Y: y, Color: int(h>>10) % 5, Delay: (last-x)*8 + int(h>>4)%70})
		}
	}
	return pixels
}()

func writeCallbackPage(w http.ResponseWriter, status int, page callbackPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	chars := make([]string, 0, len(page.Title))
	for _, r := range page.Title {
		chars = append(chars, string(r))
	}
	_ = callbackTemplate.Execute(w, struct {
		callbackPage
		Chars  []string
		Pixels []pixel
	}{page, chars, streamPixels})
}
