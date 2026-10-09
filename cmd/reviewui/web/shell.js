// The demo shell. reviewui runs in this tab as WebAssembly (main_js.go) and
// this file is everything a browser and a network would otherwise do for it:
// it routes the address bar's #/path to the server, hands it every htmx
// request in place of XMLHttpRequest, and plays audio from what it returns.
// The server's HTML is unchanged; only its root-absolute URLs are re-pointed,
// because on a static host the demo lives under a path, not at the root.
"use strict";

(() => {
  const WASM = "reviewui.wasm.gz";
  const HOME = "/conversations";

  let ready, failed;
  const booted = new Promise((ok, no) => { ready = ok; failed = no; });
  globalThis.chorusReady = () => ready();
  globalThis.chorusFailed = (msg) => failed(new Error(msg));

  const text = new TextDecoder();

  async function serve(method, path, headers = {}, body = "") {
    await booted;
    const res = await globalThis.chorusServe(method, path, headers, body);
    return { status: res.status, headers: res.headers, body: res.body };
  }

  // Links become hash routes, static files resolve beside this page, and
  // audio waits for hydrate(): an <audio> would otherwise ask the network.
  function repoint(html) {
    return html
      .replaceAll('href="/static/', 'href="static/')
      .replaceAll('src="/static/', 'src="static/')
      .replaceAll('src="/audio?', 'data-chorus-src="/audio?')
      .replaceAll('href="/', 'href="#/');
  }

  // "#/conversations/x#seq-7" is the route and the anchor within it.
  function route(hash = location.hash) {
    const h = hash.startsWith("#/") ? hash.slice(1) : HOME;
    const i = h.indexOf("#", 1);
    return i < 0 ? { path: h, anchor: "" } : { path: h.slice(0, i), anchor: h.slice(i + 1) };
  }

  // An htmx URL is already a server path; anything else is resolved against
  // the route being shown.
  function serverPath(url) {
    if (url.startsWith("/")) return url;
    if (url.startsWith("#/")) return route(url).path;
    const u = new URL(url, "http://demo.chorus" + route().path);
    return u.pathname + u.search;
  }

  const clips = new Map();
  async function hydrate(root) {
    for (const el of root.querySelectorAll("[data-chorus-src]")) {
      const src = el.getAttribute("data-chorus-src");
      el.removeAttribute("data-chorus-src");
      if (!clips.has(src)) {
        clips.set(src, serve("GET", src).then((r) => {
          if (r.status !== 200) throw new Error(`${src}: ${r.status} ${text.decode(r.body)}`);
          return URL.createObjectURL(new Blob([r.body], { type: r.headers["content-type"] }));
        }));
      }
      el.src = await clips.get(src);
    }
  }

  function save(res, path) {
    const name = /filename="([^"]+)"/.exec(res.headers["content-disposition"] || "")?.[1] || path.split("/").pop();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([res.body], { type: res.headers["content-type"] }));
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 10_000);
  }

  let shown = null; // the hash on screen, to return to after a download
  async function show() {
    const { path, anchor } = route();
    let res = await serve("GET", path);
    for (let hops = 0; res.status >= 300 && res.status < 400 && hops < 5; hops++) {
      history.replaceState(null, "", "#" + res.headers.location);
      res = await serve("GET", res.headers.location);
    }
    const html = (res.headers["content-type"] || "").startsWith("text/html");
    if (res.status === 200 && !html) {
      save(res, path);
      if (shown !== null) history.replaceState(null, "", shown);
      return;
    }
    const doc = html ? new DOMParser().parseFromString(repoint(text.decode(res.body)), "text/html") : refusal(res, path);
    document.title = doc.title.replace(" · Chorus", " · Chorus demo");
    document.body.replaceChildren(...doc.body.childNodes);
    shown = location.hash || "#" + path;
    globalThis.htmx?.process(document.body);
    const target = anchor && document.getElementById(anchor);
    target ? target.scrollIntoView() : window.scrollTo(0, 0);
    await hydrate(document.body);
    document.documentElement.dataset.route = path;
  }

  // The server's plain-text refusal, as a page: a browser would show it bare.
  function refusal(res, path) {
    const doc = document.implementation.createHTMLDocument(`${res.status} · Chorus`);
    const main = doc.createElement("main");
    main.className = "boot is-failed";
    const h1 = doc.createElement("h1");
    h1.textContent = text.decode(res.body).trim();
    const meta = doc.createElement("span");
    meta.className = "meta";
    meta.textContent = `${res.status} for ${path}`;
    const back = doc.createElement("a");
    back.href = "#" + HOME;
    back.textContent = "Back to the day";
    main.append(h1, meta, back);
    doc.body.append(main);
    return doc;
  }

  // htmx's transport: the same calls htmx makes on a real XMLHttpRequest,
  // answered by the server in this tab.
  class ServerRequest extends EventTarget {
    constructor() {
      super();
      Object.assign(this, {
        readyState: 0, status: 0, statusText: "", response: "", responseText: "",
        responseURL: "", timeout: 0, withCredentials: false, upload: new EventTarget(),
      });
      this.sent = {};
      this.got = {};
    }
    open(method, url) { this.method = method; this.url = serverPath(url); this.readyState = 1; }
    setRequestHeader(k, v) { this.sent[k] = String(v); }
    overrideMimeType() {}
    getResponseHeader(k) { return this.got[k.toLowerCase()] ?? null; }
    getAllResponseHeaders() { return Object.entries(this.got).map(([k, v]) => `${k}: ${v}`).join("\r\n"); }
    abort() { this.aborted = true; this.fire("abort"); this.fire("loadend"); }
    send(body) {
      if (body instanceof FormData) body = new URLSearchParams(body).toString();
      serve(this.method, this.url, this.sent, body == null ? "" : String(body)).then((res) => {
        if (this.aborted) return;
        this.status = res.status;
        this.got = res.headers;
        this.response = this.responseText = repoint(text.decode(res.body));
        this.responseURL = location.href;
        this.readyState = 4;
        this.fire("readystatechange");
        this.fire("load");
        this.fire("loadend");
      }, (err) => {
        console.error(err);
        this.readyState = 4;
        this.fire("error");
        this.fire("loadend");
      });
    }
    fire(type) {
      const ev = new ProgressEvent(type);
      this["on" + type]?.call(this, ev);
      this.dispatchEvent(ev);
    }
  }
  globalThis.XMLHttpRequest = ServerRequest;

  document.addEventListener("DOMContentLoaded", () => {
    // htmx would push the server's own paths; the route lives in the hash.
    htmx.config.historyEnabled = false;
    htmx.config.historyCacheSize = 0;
    document.body.addEventListener("htmx:pushedIntoHistory", (e) => {
      shown = "#" + e.detail.path;
      history.pushState(null, "", shown);
    });
    document.body.addEventListener("htmx:afterSettle", (e) => hydrate(document.body));
    document.body.addEventListener("htmx:oobAfterSwap", (e) => hydrate(document.body));
  });
  window.addEventListener("hashchange", () => show().catch(fail));

  function status(msg, fraction) {
    const s = document.getElementById("boot-status");
    if (s) s.textContent = msg;
    const p = document.getElementById("boot-progress");
    if (p && fraction !== undefined) p.value = fraction;
  }

  function fail(err) {
    console.error(err);
    const boot = document.getElementById("boot");
    if (boot) {
      boot.classList.add("is-failed");
      status(`The demo could not start: ${err.message}`);
    } else {
      alert(`The demo hit an error: ${err.message}`);
    }
  }

  // Fetched gzipped and inflated here, so the download is small whatever
  // the host does about compressing WebAssembly.
  async function boot() {
    const res = await fetch(WASM);
    if (!res.ok) throw new Error(`${WASM}: ${res.status}`);
    const total = Number(res.headers.get("content-length")) || 0;
    let got = 0;
    const counted = res.body.pipeThrough(new TransformStream({
      transform(chunk, ctl) {
        got += chunk.byteLength;
        if (total) status(`Fetching the server… ${(got / 1e6).toFixed(1)} of ${(total / 1e6).toFixed(1)} MB`, got / total);
        ctl.enqueue(chunk);
      },
    }));
    const wasm = new Response(counted.pipeThrough(new DecompressionStream("gzip")), {
      headers: { "content-type": "application/wasm" },
    });
    const go = new Go();
    const { instance } = await WebAssembly.instantiateStreaming(wasm, go.importObject);
    status("Starting the server…", 1);
    go.run(instance).then(() => failed(new Error("the server stopped")));
    await booted;
    if (!location.hash.startsWith("#/")) history.replaceState(null, "", "#" + HOME);
    await show();
  }

  boot().catch(fail);
})();
