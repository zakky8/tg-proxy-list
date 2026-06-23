/* mtproto-proxy-pro — loads proxies.json and renders a searchable, sortable proxy list,
   with extra help for users in countries that block Telegram. */
(() => {
  "use strict";

  const $ = (sel) => document.querySelector(sel);
  const rowsEl = $("#rows");
  const loadingEl = $("#loading");
  const emptyEl = $("#empty");
  const shownEl = $("#shown");
  const searchEl = $("#search");
  const countryEl = $("#country");
  const sortEl = $("#sort");
  const resistEl = $("#resistonly");
  const toastEl = $("#toast");
  const bannerEl = $("#banner");
  const bannerTextEl = $("#banner-text");

  let ALL = [];

  // Countries that actively block/throttle Telegram (2026). Used for the banner.
  const CENSORED = {
    IR: "Iran", RU: "Russia", CN: "China", TM: "Turkmenistan", VN: "Vietnam",
    VE: "Venezuela", PK: "Pakistan", BY: "Belarus", UZ: "Uzbekistan", MM: "Myanmar",
  };

  const COUNTRY_NAMES = new Intl.DisplayNames(["en"], { type: "region" });

  function flag(cc) {
    if (!cc || cc.length !== 2 || cc === "??" || cc === "XX") return "🌐";
    const A = 0x1f1e6;
    return String.fromCodePoint(A + (cc.charCodeAt(0) - 65), A + (cc.charCodeAt(1) - 65));
  }
  function countryName(cc) {
    if (!cc || cc === "??" || cc === "XX") return "Unknown";
    try { return COUNTRY_NAMES.of(cc) || cc; } catch { return cc; }
  }
  function latClass(ms) {
    if (ms < 120) return "lat--fast";
    if (ms < 350) return "lat--mid";
    return "lat--slow";
  }
  function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  function relTime(iso) {
    const t = Date.parse(iso);
    if (isNaN(t)) return "—";
    const mins = Math.round((Date.now() - t) / 60000);
    if (mins < 1) return "just now";
    if (mins < 60) return mins + "m ago";
    const h = Math.round(mins / 60);
    if (h < 24) return h + "h ago";
    return Math.round(h / 24) + "d ago";
  }

  // Two link forms for the same proxy:
  //  - tmeHref: https://t.me/proxy?... — the universal web link (shareable; used for Copy
  //    and as the no-JS / no-app fallback).
  //  - tgHref:  tg://proxy?...        — the app scheme that opens Telegram Desktop / the
  //    mobile app directly. Connect tries this first (see the click handler) so it opens
  //    the actual app instead of a web page on desktop.
  function tmeHref(p) {
    return p.link || `https://t.me/proxy?server=${encodeURIComponent(p.server)}&port=${p.port}&secret=${encodeURIComponent(p.secret)}`;
  }
  function tgHref(p) {
    return `tg://proxy?server=${encodeURIComponent(p.server)}&port=${p.port}&secret=${encodeURIComponent(p.secret)}`;
  }

  // Mirror of model.IsCensorshipResistant: proven reachable from a censored
  // network, or structurally resistant (FakeTLS on 443 with a real SNI domain).
  function isResistant(p) {
    if (p.reachable_from && p.reachable_from.length) return true;
    return p.type === "ee" && p.port === 443 && (p.secret || "").length > 34;
  }
  function resilienceTier(score) {
    if (score >= 75) return { cls: "res--high", label: "High" };
    if (score >= 50) return { cls: "res--mid", label: "Med" };
    return { cls: "res--low", label: "Low" };
  }

  function fillStats(data) {
    $('[data-stat="count"]').textContent = (data.count ?? ALL.length).toLocaleString();
    const resistant = typeof data.censorship_resistant === "number"
      ? data.censorship_resistant
      : ALL.filter(isResistant).length;
    $('[data-stat="resistant"]').textContent = resistant.toLocaleString();
    $('[data-stat="countries"]').textContent = Object.keys(data.countries || {}).filter((c) => c !== "??").length;
    const upd = $('[data-stat="updated"]');
    upd.textContent = relTime(data.generated_at_utc);
    upd.title = data.generated_at_utc;
  }

  function fillCountryFilter(data) {
    const entries = Object.entries(data.countries || {})
      .filter(([cc]) => cc !== "??")
      .sort((a, b) => b[1] - a[1]);
    for (const [cc, n] of entries) {
      const o = document.createElement("option");
      o.value = cc;
      o.textContent = `${flag(cc)} ${countryName(cc)} (${n})`;
      countryEl.appendChild(o);
    }
  }

  function reachChips(p) {
    if (!p.reachable_from || !p.reachable_from.length) return "";
    return ' ' + p.reachable_from.map((cc) =>
      `<span class="reach-chip" title="Tested reachable from inside ${esc(countryName(cc))}">${flag(cc)} ${esc(cc)}</span>`
    ).join(" ");
  }

  function rowHTML(p) {
    const name = countryName(p.country);
    const typeLabel = { ee: "FakeTLS", dd: "Secure", plain: "Basic" }[p.type] || p.type;
    const statusLabel = p.status === "handshake_ok"
      ? '<span class="status-tag">● handshake</span>'
      : '<span class="status-tag status-tag--reach">● reachable</span>';
    const tier = resilienceTier(p.resilience || 0);
    const link = tmeHref(p);
    const lat = typeof p.latency_ms === "number" ? p.latency_ms : null;
    return `<tr>
      <td class="col-country" data-label="Country"><span class="td-country"><span class="flag" aria-hidden="true">${flag(p.country)}</span><span>${esc(name)}</span></span></td>
      <td class="col-server" data-label="Server"><span class="server">${esc(p.server)}<span class="port">:${p.port}</span></span><br>${statusLabel}${reachChips(p)}</td>
      <td class="col-type" data-label="Type"><span class="badge badge--${esc(p.type)}">${esc(typeLabel)}</span> <span class="badge res ${tier.cls}" title="Censorship-resistance score ${p.resilience || 0}/100">🛡 ${tier.label}</span></td>
      <td class="col-num" data-label="Latency"><span class="lat ${latClass(lat ?? 9999)}">${lat ?? "—"} ms</span></td>
      <td class="col-num" data-label="Uptime"><span class="uptime">${p.uptime_pct ?? 0}%</span></td>
      <td class="col-actions" data-label="Connect"><div class="actions">
        <button class="btn btn--icon" type="button" data-copy="${esc(link)}" aria-label="Copy proxy link for ${esc(p.server)}" title="Copy link">
          <svg width="15" height="15" viewBox="0 0 24 24" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M9 9h10v10H9zM5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/></svg>
        </button>
        <a class="btn btn--go" href="${esc(link)}" data-tg="${esc(tgHref(p))}" target="_blank" rel="noopener" aria-label="Connect to ${esc(p.server)} in Telegram">Connect</a>
      </div></td>
    </tr>`;
  }

  function applyFilters() {
    const q = searchEl.value.trim().toLowerCase();
    const cc = countryEl.value;
    const resistOnly = resistEl.checked;
    const sort = sortEl.value;

    let list = ALL.filter((p) => {
      if (cc && p.country !== cc) return false;
      if (resistOnly && !isResistant(p)) return false;
      if (q && !(p.server.toLowerCase().includes(q) || countryName(p.country).toLowerCase().includes(q) || (p.country || "").toLowerCase().includes(q))) return false;
      return true;
    });

    if (sort === "resilience") list.sort((a, b) => (b.resilience || 0) - (a.resilience || 0) || a.latency_ms - b.latency_ms);
    else if (sort === "latency") list.sort((a, b) => a.latency_ms - b.latency_ms);
    else if (sort === "uptime") list.sort((a, b) => (b.uptime_pct ?? 0) - (a.uptime_pct ?? 0) || a.latency_ms - b.latency_ms);
    else if (sort === "country") list.sort((a, b) => countryName(a.country).localeCompare(countryName(b.country)) || a.latency_ms - b.latency_ms);

    rowsEl.innerHTML = list.map(rowHTML).join("");
    shownEl.textContent = list.length.toLocaleString();
    emptyEl.hidden = list.length !== 0;
  }

  // If the visitor is in a censored country, surface the resistant proxies up front.
  function detectCountry() {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), 4000);
    fetch("https://api.country.is/", { signal: ctrl.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        clearTimeout(t);
        const cc = d && d.country;
        if (!cc || !CENSORED[cc]) return;
        const reachable = ALL.some((p) => (p.reachable_from || []).includes(cc));
        bannerTextEl.innerHTML = `Telegram is restricted in <strong>${esc(CENSORED[cc])}</strong>. Showing <strong>censorship-resistant FakeTLS proxies</strong>`
          + (reachable ? ` tested reachable from inside ${esc(CENSORED[cc])} ${flag(cc)}.` : ` (disguised as HTTPS on port 443).`);
        bannerEl.hidden = false;
        resistEl.checked = true;
        sortEl.value = "resilience";
        applyFilters();
      })
      .catch(() => clearTimeout(t));
  }

  function debounce(fn, ms) {
    let t;
    return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
  }

  function showToast(msg) {
    toastEl.textContent = msg;
    toastEl.hidden = false;
    requestAnimationFrame(() => toastEl.classList.add("show"));
    clearTimeout(showToast._t);
    showToast._t = setTimeout(() => {
      toastEl.classList.remove("show");
      setTimeout(() => (toastEl.hidden = true), 250);
    }, 1800);
  }

  rowsEl.addEventListener("click", async (e) => {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    const link = btn.getAttribute("data-copy");
    try {
      await navigator.clipboard.writeText(link);
      showToast("Proxy link copied");
    } catch {
      const ta = document.createElement("textarea");
      ta.value = link; document.body.appendChild(ta); ta.select();
      try { document.execCommand("copy"); showToast("Proxy link copied"); } catch { showToast("Copy failed — long-press to copy"); }
      ta.remove();
    }
  });

  // Connect: open the Telegram app directly via the tg:// scheme (opens Telegram Desktop
  // or the mobile app with the "enable proxy?" prompt), and fall back to the t.me web link
  // only if no installed app handles it. This avoids landing on the telegram.org web page
  // on desktop, while still degrading gracefully when Telegram isn't installed.
  rowsEl.addEventListener("click", (e) => {
    const a = e.target.closest("a.btn--go");
    if (!a || !a.dataset.tg) return;
    e.preventDefault();
    const web = a.getAttribute("href");
    const app = a.dataset.tg;
    let handled = false;
    const cancel = () => { handled = true; };
    // When Telegram opens, this window loses focus / becomes hidden — then skip the fallback.
    window.addEventListener("blur", cancel, { once: true });
    document.addEventListener("visibilitychange", function vc() {
      if (document.hidden) { cancel(); document.removeEventListener("visibilitychange", vc); }
    });
    window.location.href = app; // try the Telegram app first
    window.setTimeout(() => {    // fall back to the universal web link only if nothing handled it
      if (!handled) window.location.href = web;
    }, 2000);
    showToast("Opening Telegram…");
  });

  searchEl.addEventListener("input", debounce(applyFilters, 120));
  countryEl.addEventListener("change", applyFilters);
  sortEl.addEventListener("change", applyFilters);
  resistEl.addEventListener("change", applyFilters);

  fetch("proxies.json", { cache: "no-store" })
    .then((r) => { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
    .then((data) => {
      ALL = Array.isArray(data.proxies) ? data.proxies : [];
      loadingEl.hidden = true;
      fillStats(data);
      fillCountryFilter(data);
      applyFilters();
      detectCountry();
    })
    .catch((err) => {
      loadingEl.textContent = "Could not load the proxy list. Try the raw .txt or JSON links above.";
      console.error("proxies.json load failed:", err);
    });
})();
