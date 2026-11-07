"use strict";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));

// Config keys the server accepts in PUT /api/config.
const CONFIG_KEYS = ["SiloUrl", "SiloApiKey", "ServerName", "ButtonUrl", "FromName", "FromEmail", "ReplyToEmail", "TestEmail",
  "SmtpHost", "SmtpPort", "SmtpUsername", "SmtpPassword", "SmtpSecurity", "TimeZone", "DaysBack", "MaxMovies", "MaxShows",
  "IncludeChildProfiles", "ExcludedUserIds", "IncludedLibraryIds", "EmailOverrides", "ScheduleEnabled", "ScheduleDay",
  "ScheduleTime", "SendDelaySeconds", "Layout"];
const NUMBER_KEYS = new Set(["SmtpPort", "DaysBack", "MaxMovies", "MaxShows", "SendDelaySeconds"]);
const DAY_NAMES = { Monday: "poniedziałek", Tuesday: "wtorek", Wednesday: "środa", Thursday: "czwartek", Friday: "piątek", Saturday: "sobota", Sunday: "niedziela" };

let config = null;
let users = [];

async function api(method, path, body) {
  const options = { method, headers: { "X-Silo-Newsletter": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `Błąd HTTP ${response.status}`);
  return data;
}

let toastTimer;
function toast(message, kind = "ok") {
  const element = $("#toast");
  element.textContent = message;
  element.className = kind;
  element.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { element.hidden = true; }, kind === "err" ? 9000 : 4000);
}

async function busy(button, work) {
  button.disabled = true;
  try { return await work(); } finally { button.disabled = false; }
}

function formatDate(value) {
  if (!value) return "—";
  return new Date(value).toLocaleString("pl-PL", { weekday: "long", day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" });
}

// ---------- tabs ----------

function openTab(name) {
  $$("#tabs button").forEach((button) => button.classList.toggle("active", button.dataset.tab === name));
  $$("[data-panel]").forEach((panel) => { panel.hidden = panel.dataset.panel !== name; });
  if (name === "dashboard") refreshStatus();
  if (name === "recipients" || name === "preview") loadUsers();
  if (name === "content") loadLibraries();
  if (name === "history") loadHistory();
}
$$("#tabs button").forEach((button) => button.addEventListener("click", () => openTab(button.dataset.tab)));

// ---------- config forms ----------

function fillForms() {
  for (const form of $$("[data-form]")) {
    for (const input of $$("[name]", form)) {
      const value = config[input.name];
      if (input.type === "checkbox") input.checked = Boolean(value);
      else if (input.dataset.secret) {
        input.value = "";
        input.placeholder = config[input.dataset.secret] ? "•••••• zapisane" : "";
      } else input.value = value ?? "";
    }
  }
  fillLayout(config.Layout);
  const mailProblem = $("[data-mail-problem]");
  mailProblem.textContent = config.MailProblem ? `Do wysyłki brakuje: ${config.MailProblem}` : "Ustawienia e-mail są kompletne.";
  mailProblem.className = "result " + (config.MailProblem ? "err" : "ok");
}

async function loadConfig() {
  config = await api("GET", "/api/config");
  fillForms();
}

function collectConfig() {
  const result = {};
  for (const key of CONFIG_KEYS) result[key] = config[key];
  for (const form of $$("[data-form]")) {
    for (const input of $$("[name]", form)) {
      if (input.type === "checkbox") result[input.name] = input.checked;
      else if (NUMBER_KEYS.has(input.name)) result[input.name] = Number(input.value || 0);
      else result[input.name] = input.value.trim();
    }
  }
  // Library and recipient choices live outside plain inputs.
  const libraryBoxes = $$("#libraries input[type=checkbox]");
  if (libraryBoxes.length) result.IncludedLibraryIds = libraryBoxes.filter((box) => box.checked).map((box) => box.value);
  const userRows = $$("#users tr[data-id]");
  if (userRows.length) {
    result.ExcludedUserIds = userRows.filter((row) => !$("input[type=checkbox]", row).checked).map((row) => row.dataset.id);
    const overrides = {};
    for (const row of userRows) {
      const value = $("input[type=email]", row)?.value.trim();
      if (value) overrides[row.dataset.id] = value;
    }
    result.EmailOverrides = overrides;
  }
  result.ExcludedUserIds = result.ExcludedUserIds || [];
  result.IncludedLibraryIds = result.IncludedLibraryIds || [];
  result.EmailOverrides = result.EmailOverrides || {};
  if ($$("#sections li").length) result.Layout = collectLayout();
  return result;
}

for (const form of $$("[data-form]")) {
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = $("button[type=submit]", form);
    await busy(button, async () => {
      try {
        await api("PUT", "/api/config", collectConfig());
        await loadConfig();
        toast("Zapisano.");
        refreshStatus();
      } catch (error) { toast(error.message, "err"); }
    });
  });
}

$("#test-silo").addEventListener("click", (event) => busy(event.target, async () => {
  const result = $("#test-silo-result");
  result.className = "result";
  result.textContent = "Sprawdzanie…";
  try {
    await api("PUT", "/api/config", collectConfig());
    await loadConfig();
    const data = await api("POST", "/api/test/silo", {});
    result.className = "result ok";
    result.textContent = `Połączenie działa: kont ${data.accounts}, bibliotek ${data.libraries}. Historia, katalog, oceny i plakaty są dostępne.`;
  } catch (error) {
    result.className = "result err";
    result.textContent = error.message;
  }
}));

// ---------- layout ----------

const SECTION_NAMES = { personal: "Twój tydzień (statystyki)", movies: "Nowe filmy", shows: "Nowe odcinki i seriale", popular: "Najpopularniejsze na serwerze" };
const THEME_ACCENTS = { dark: "#8fa3ff", light: "#4253e0" };

function sectionRow(section) {
  const item = document.createElement("li");
  item.dataset.id = section.Id;
  const label = document.createElement("label");
  const box = document.createElement("input");
  box.type = "checkbox";
  box.checked = section.Enabled;
  label.append(box, ` ${SECTION_NAMES[section.Id] || section.Id}`);
  item.append(label);
  if (section.Id === "movies" || section.Id === "shows") {
    const style = document.createElement("select");
    style.setAttribute("aria-label", `Układ: ${SECTION_NAMES[section.Id]}`);
    style.append(new Option("lista z opisem", "list"), ...[2, 3, 4].map((n) => new Option(`siatka plakatów, ${n} w rzędzie`, `grid-${n}`)));
    style.value = section.Style === "grid" ? `grid-${section.Columns}` : "list";
    item.append(style);
  }
  for (const [text, step, name] of [["↑", -1, "wyżej"], ["↓", 1, "niżej"]]) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = text;
    button.setAttribute("aria-label", `Przesuń ${name}`);
    button.addEventListener("click", () => {
      const sibling = step < 0 ? item.previousElementSibling : item.nextElementSibling;
      if (sibling) step < 0 ? sibling.before(item) : sibling.after(item);
    });
    item.append(button);
  }
  return item;
}

function fillLayout(layout) {
  for (const input of $$("[data-layout]")) {
    const value = layout[input.dataset.layout];
    if (input.type === "checkbox") input.checked = Boolean(value);
    else input.value = value ?? "";
  }
  $("#accent-default").checked = !layout.AccentColor;
  $("#accent-color").disabled = !layout.AccentColor;
  $("#accent-color").value = layout.AccentColor || THEME_ACCENTS[layout.Theme] || THEME_ACCENTS.dark;
  $("#sections").replaceChildren(...layout.Sections.map(sectionRow));
}

function collectLayout() {
  const layout = { ...config.Layout };
  for (const input of $$("[data-layout]")) layout[input.dataset.layout] = input.type === "checkbox" ? input.checked : input.value.trim();
  layout.AccentColor = $("#accent-default").checked ? "" : $("#accent-color").value;
  layout.Sections = $$("#sections li").map((item) => {
    const [style, columns] = ($("select", item)?.value || "list").split("-");
    return { Id: item.dataset.id, Enabled: $("input", item).checked, Style: style, Columns: Number(columns || 3) };
  });
  return layout;
}

$("#accent-default").addEventListener("change", (event) => {
  $("#accent-color").disabled = event.target.checked;
  if (event.target.checked) $("#accent-color").value = THEME_ACCENTS[$("[data-layout=Theme]").value];
});
$("[data-layout=Theme]").addEventListener("change", (event) => {
  if ($("#accent-default").checked) $("#accent-color").value = THEME_ACCENTS[event.target.value];
});

$("#layout-reset").addEventListener("click", () => {
  fillLayout(config.DefaultLayout);
  toast("Przywrócono domyślny wygląd. Kliknij „Zapisz”, żeby go zachować.");
});

$("#layout-preview").addEventListener("click", (event) => busy(event.target, async () => {
  try {
    await api("PUT", "/api/config", collectConfig());
    await loadConfig();
    openTab("preview");
    await loadUsers();
    $("#preview-run").click();
  } catch (error) { toast(error.message, "err"); }
}));

// ---------- libraries and users ----------

async function loadLibraries() {
  const container = $("#libraries");
  try {
    const libraries = await api("GET", "/api/silo/libraries");
    const included = new Set(config.IncludedLibraryIds || []);
    container.replaceChildren(...libraries.map((library) => {
      const label = document.createElement("label");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.value = library.id;
      box.checked = included.has(library.id);
      label.append(box, ` ${library.name}`);
      return label;
    }));
  } catch (error) {
    const note = document.createElement("small");
    note.textContent = `Nie udało się wczytać bibliotek: ${error.message}`;
    container.replaceChildren(note);
  }
}

function badge(text, kind) {
  const span = document.createElement("span");
  span.className = `badge ${kind}`;
  span.textContent = text;
  return span;
}

async function loadUsers() {
  const body = $("#users");
  try {
    users = await api("GET", "/api/silo/users");
  } catch (error) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 5;
    cell.textContent = `Nie udało się wczytać kont: ${error.message}`;
    row.append(cell);
    body.replaceChildren(row);
    return;
  }
  body.replaceChildren(...users.map((user) => {
    const row = document.createElement("tr");
    row.dataset.id = user.id;
    const send = document.createElement("input");
    send.type = "checkbox";
    send.checked = !user.excluded;
    send.disabled = !user.active;
    send.setAttribute("aria-label", `Wysyłać do ${user.name}`);
    const override = document.createElement("input");
    override.type = "email";
    override.value = user.emailOverride || "";
    override.placeholder = user.hasSiloEmail ? "niepotrzebny" : "adres@przyklad.pl";
    override.disabled = user.hasSiloEmail;
    const status = user.reason ? badge(user.reason, user.reason.includes("wyłączone") ? "" : "warn") : badge("dostanie newsletter", "ok");
    const cells = [send, `${user.name}${user.admin ? " (admin)" : ""} · ID ${user.id}`, user.hasSiloEmail ? "jest" : "brak", override, status];
    for (const content of cells) {
      const cell = document.createElement("td");
      cell.append(content);
      row.append(cell);
    }
    return row;
  }));
  const select = $("#preview-user");
  const previous = select.value;
  select.replaceChildren(...users.map((user) => new Option(`${user.name} (ID ${user.id})`, user.id)));
  if (previous) select.value = previous;
}

// ---------- preview and sending ----------

$("#preview-run").addEventListener("click", (event) => busy(event.target, async () => {
  const result = $("#preview-result");
  result.className = "result";
  result.textContent = "Tworzenie podglądu…";
  try {
    const data = await api("POST", "/api/preview", { UserID: $("#preview-user").value });
    $("#preview-frame").srcdoc = data.html;
    result.textContent = "";
  } catch (error) {
    result.className = "result err";
    result.textContent = error.message;
  }
}));

$("#send-test").addEventListener("click", (event) => busy(event.target, async () => {
  const result = $("#preview-result");
  result.className = "result";
  result.textContent = "Wysyłanie testu…";
  try {
    const data = await api("POST", "/api/send-test", { UserID: $("#preview-user").value });
    result.className = "result ok";
    result.textContent = `Wysłano test na ${data.sentTo}.`;
  } catch (error) {
    result.className = "result err";
    result.textContent = error.message;
  }
}));

$("#send-all").addEventListener("click", (event) => busy(event.target, async () => {
  try {
    await api("POST", "/api/send-all", { Confirm: $("#send-all-confirm").value.trim() });
    $("#send-all-confirm").value = "";
    toast("Wysyłka ruszyła. Postęp zobaczysz w Historii.");
    setTimeout(refreshStatus, 1500);
  } catch (error) { toast(error.message, "err"); }
}));

// ---------- status and history ----------

function describeRun(run) {
  if (!run) return "Jeszcze nie było wysyłki.";
  const state = run.status === "running" ? "trwa" : run.status === "done" ? "zakończona" : "z błędami";
  return `${formatDate(run.startedAt)} — ${state}: wysłane ${run.sent}, błędy ${run.failed}, pominięte ${run.skipped}${run.message ? ` (${run.message})` : ""}`;
}

async function refreshStatus() {
  const status = await api("GET", "/api/status");
  $("#version").textContent = `Silo Newsletter ${status.version}`;
  $("#dash-config").textContent = status.configured
    ? (status.mailConfigured ? "Wszystko ustawione." : "Silo połączone, uzupełnij ustawienia e-mail.")
    : "Uzupełnij połączenie z Silo.";
  $("#dash-next").textContent = status.scheduleEnabled && status.nextSend ? formatDate(status.nextSend) : "Harmonogram wyłączony.";
  $("#dash-last").textContent = describeRun(status.lastRun);
  if (status.sending) setTimeout(refreshStatus, 4000);
}

async function loadHistory() {
  const runs = await api("GET", "/api/history");
  const kinds = { scheduled: "harmonogram", manual: "ręczna", test: "test" };
  const body = $("#history");
  if (!runs.length) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 6;
    cell.textContent = "Brak wysyłek.";
    row.append(cell);
    body.replaceChildren(row);
    return;
  }
  body.replaceChildren(...runs.map((run) => {
    const row = document.createElement("tr");
    const status = run.status === "done" ? badge("OK", "ok") : run.status === "running" ? badge("trwa", "warn") : badge("błąd", "err");
    if (run.message || (run.failures && run.failures.length)) status.title = [run.message, ...(run.failures || [])].filter(Boolean).join("\n");
    for (const content of [formatDate(run.startedAt), kinds[run.kind] || run.kind, String(run.sent), String(run.failed), String(run.skipped), status]) {
      const cell = document.createElement("td");
      cell.append(content);
      row.append(cell);
    }
    return row;
  }));
}

// ---------- start ----------

async function start() {
  const status = await api("GET", "/api/status");
  await loadConfig();
  openTab(status.configured ? "dashboard" : "connection");
  if (!status.configured) toast("Zacznij od połączenia z Silo.");
}

start().catch((error) => toast(error.message, "err"));
