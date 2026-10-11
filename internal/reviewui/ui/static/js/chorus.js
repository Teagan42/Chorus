// A failed request, said out loud. htmx drops the body of a 4xx or 5xx, so a
// verdict, label or promotion the server refused would leave a button that
// silently did nothing. This puts what the server said into the alert region
// above the header (#alerts in layout/base.tmpl), in the kit's own alert.
"use strict";

(() => {
  const region = () => document.getElementById("alerts");

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  function say(title, body, note) {
    const r = region();
    if (!r) return;
    const alert = el("div", "alert tone-conv");
    alert.setAttribute("role", "alert");
    alert.append(el("span", "alert__title", title));
    if (body) alert.append(el("span", "alert__body", body));
    const dismiss = el("button", "btn", "Dismiss");
    dismiss.type = "button";
    dismiss.addEventListener("click", () => alert.remove());
    const row = el("div", "btn-row");
    row.append(dismiss);
    alert.append(row);
    if (note) alert.append(el("span", "meta", note));
    r.replaceChildren(alert);
    alert.scrollIntoView({ block: "nearest" });
  }

  // The control as the reviewer read it, when it is a button with a short
  // label; a form or a long one is just "that".
  function what(elt) {
    const label = elt && (elt.tagName === "BUTTON" || elt.tagName === "A") ? elt.innerText.trim() : "";
    return label && label.length <= 40 ? `“${label}”` : "That";
  }

  function request(detail) {
    const c = detail.requestConfig || {};
    return `${(c.verb || "").toUpperCase()} ${c.path || ""}`.trim();
  }

  document.addEventListener("htmx:responseError", (e) => {
    const { xhr, elt } = e.detail;
    const said = (xhr.responseText || "").trim();
    const status = `${xhr.status}${xhr.statusText ? " " + xhr.statusText : ""}`;
    say(
      `${what(elt)} did not go through.`,
      said || `The server answered ${status} and said nothing more.`,
      `${status} from ${request(e.detail)}`,
    );
  });

  document.addEventListener("htmx:sendError", (e) => {
    say(
      `${what(e.detail.elt)} did not reach the server.`,
      "The request got no answer. Check that the review UI is still running, then try again.",
      request(e.detail),
    );
  });

  // A request that lands clears the last failure: the retry worked.
  document.addEventListener("htmx:afterRequest", (e) => {
    if (e.detail.successful) region()?.replaceChildren();
  });
})();
