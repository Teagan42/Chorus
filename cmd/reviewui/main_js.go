//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall/js"
)

// On the demo site the page is the client and this is the server: the shell
// (cmd/reviewui/web) hands every request the review UI makes to chorusServe
// and renders what comes back. Nothing leaves the tab.
func main() {
	s, err := newDemoServer(context.Background())
	if err != nil {
		js.Global().Call("chorusFailed", err.Error())
		return
	}
	h := s.routes()
	js.Global().Set("chorusServe", js.FuncOf(func(_ js.Value, args []js.Value) any {
		method, url, headers, body := args[0].String(), args[1].String(), args[2], args[3].String()
		return promise(func() (js.Value, error) { return serve(h, method, url, headers, body) })
	}))
	js.Global().Call("chorusReady")
	select {}
}

// serve runs one request through the handler, as net/http would.
func serve(h http.Handler, method, url string, headers js.Value, body string) (js.Value, error) {
	r, err := http.NewRequestWithContext(context.Background(), method, "http://demo.chorus"+url, strings.NewReader(body))
	if err != nil {
		return js.Undefined(), err
	}
	keys := js.Global().Get("Object").Call("keys", headers)
	for i := range keys.Length() {
		k := keys.Index(i).String()
		r.Header.Set(k, headers.Get(k).String())
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	res := js.Global().Get("Object").New()
	res.Set("status", w.Code)
	hdr := js.Global().Get("Object").New()
	for k := range w.Header() {
		hdr.Set(strings.ToLower(k), w.Header().Get(k))
	}
	res.Set("headers", hdr)
	b := w.Body.Bytes()
	out := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(out, b)
	res.Set("body", out)
	return res, nil
}

// promise runs f off the event loop, as a callback into Go must not block.
func promise(f func() (js.Value, error)) js.Value {
	return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			defer func() {
				if p := recover(); p != nil {
					reject.Invoke(js.Global().Get("Error").New(fmt.Sprint("reviewui: ", p)))
				}
			}()
			v, err := f()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	}))
}
