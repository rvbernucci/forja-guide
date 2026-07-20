const state = {
  bootstrap: null,
  researching: false,
};

const elements = {
  hero: document.querySelector("#hero"),
  suggestions: document.querySelector("#suggestions"),
  researchOutput: document.querySelector("#research-output"),
  researchTitle: document.querySelector("#research-title"),
  researchState: document.querySelector("#research-state"),
  researchNotice: document.querySelector("#research-notice"),
  planList: document.querySelector("#plan-list"),
  form: document.querySelector("#research-form"),
  prompt: document.querySelector("#research-prompt"),
  submit: document.querySelector("#submit-research"),
  companyList: document.querySelector("#company-list"),
  universeCount: document.querySelector("#universe-count"),
  capabilityList: document.querySelector("#capability-list"),
  runtimeBrief: document.querySelector("#runtime-brief"),
  runtimeLabel: document.querySelector("#runtime-label"),
  accelerator: document.querySelector("#accelerator-name"),
  softwareStack: document.querySelector("#software-stack"),
  modelStatus: document.querySelector("#model-status"),
  embeddingStatus: document.querySelector("#embedding-status"),
  navigation: document.querySelector("#navigation"),
  inspector: document.querySelector("#inspector"),
  scrim: document.querySelector("#scrim"),
};

async function requestJSON(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    throw new Error(payload?.error?.message || `Request failed with status ${response.status}.`);
  }
  return payload;
}

function renderBootstrap(bootstrap) {
  state.bootstrap = bootstrap;
  elements.universeCount.textContent = String(bootstrap.universe.length).padStart(2, "0");
  elements.companyList.replaceChildren(...bootstrap.universe.map(companyRow));
  elements.suggestions.replaceChildren(...bootstrap.suggested_prompts.map(suggestionButton));
  elements.capabilityList.replaceChildren(...bootstrap.capabilities.map(capabilityCard));

  const ready = bootstrap.runtime.core_inference_ready;
  const dot = elements.runtimeBrief.querySelector(".runtime-dot");
  dot.dataset.state = ready ? "ready" : "waiting";
  elements.runtimeLabel.textContent = ready ? "Local runtime configured" : "Runtime setup required";
  elements.accelerator.textContent = bootstrap.runtime.accelerator;
  elements.softwareStack.textContent = bootstrap.runtime.software_stack;
  elements.modelStatus.textContent = formatStatus(bootstrap.runtime.model.status);
  elements.embeddingStatus.textContent = formatStatus(bootstrap.runtime.embeddings.status);
}

function companyRow(company) {
  const row = document.createElement("div");
  row.className = "company-row";
  const ticker = document.createElement("b");
  ticker.textContent = company.ticker;
  const name = document.createElement("span");
  name.textContent = company.name;
  const status = document.createElement("i");
  row.append(ticker, name, status);
  return row;
}

function suggestionButton(prompt) {
  const button = document.createElement("button");
  button.className = "suggestion";
  button.type = "button";
  button.textContent = prompt;
  button.addEventListener("click", () => {
    elements.prompt.value = prompt;
    resizeComposer();
    elements.prompt.focus();
  });
  return button;
}

function capabilityCard(capability) {
  const card = document.createElement("div");
  card.className = "capability";
  const heading = document.createElement("div");
  const name = document.createElement("strong");
  name.textContent = capability.name;
  const status = document.createElement("b");
  status.textContent = capability.status;
  const description = document.createElement("p");
  description.textContent = capability.description;
  heading.append(name, status);
  card.append(heading, description);
  return card;
}

function renderResearch(session, prompt) {
  elements.hero.classList.add("hidden");
  elements.suggestions.classList.add("hidden");
  elements.researchOutput.classList.remove("hidden");
  elements.researchTitle.textContent = compactTitle(prompt);
  elements.researchState.textContent = formatStatus(session.state);
  elements.researchNotice.textContent = session.notice;
  elements.planList.replaceChildren(...session.plan.map((step, index) => planStep(step, index)));
  document.querySelector("#workspace-scroll").scrollTo({ top: 0, behavior: "smooth" });
}

function planStep(step, index) {
  const row = document.createElement("article");
  row.className = "plan-step";
  const number = document.createElement("span");
  number.className = "plan-number";
  number.textContent = String(index + 1).padStart(2, "0");
  const body = document.createElement("div");
  const title = document.createElement("h3");
  title.textContent = step.name;
  const description = document.createElement("p");
  description.textContent = step.description;
  body.append(title, description);
  const tool = document.createElement("span");
  tool.className = "tool-chip";
  tool.textContent = step.tool;
  row.append(number, body, tool);
  return row;
}

async function submitResearch(event) {
  event.preventDefault();
  const prompt = elements.prompt.value.trim();
  if (!prompt || state.researching) return;
  state.researching = true;
  elements.submit.disabled = true;
  elements.submit.querySelector("span").textContent = "Planning";
  try {
    const session = await requestJSON("/api/v1/research", {
      method: "POST",
      body: JSON.stringify({ prompt }),
    });
    renderResearch(session, prompt);
  } catch (error) {
    elements.researchOutput.classList.remove("hidden");
    elements.researchNotice.textContent = error.message;
    elements.researchState.textContent = "Request failed";
  } finally {
    state.researching = false;
    elements.submit.disabled = false;
    elements.submit.querySelector("span").textContent = "Research";
  }
}

function resetResearch() {
  elements.prompt.value = "";
  elements.prompt.style.height = "auto";
  elements.hero.classList.remove("hidden");
  elements.suggestions.classList.remove("hidden");
  elements.researchOutput.classList.add("hidden");
  elements.prompt.focus();
  closeDrawers();
}

function compactTitle(prompt) {
  const compact = prompt.replace(/\s+/g, " ").trim();
  return compact.length > 116 ? `${compact.slice(0, 115)}…` : compact;
}

function formatStatus(value) {
  return value.replaceAll("-", " ").replaceAll("_", " ");
}

function resizeComposer() {
  elements.prompt.style.height = "auto";
  elements.prompt.style.height = `${Math.min(elements.prompt.scrollHeight, 160)}px`;
}

function openDrawer(drawer) {
  closeDrawers();
  drawer.classList.add("open");
  elements.scrim.classList.add("visible");
}

function closeDrawers() {
  elements.navigation.classList.remove("open");
  elements.inspector.classList.remove("open");
  elements.scrim.classList.remove("visible");
}

function bindInteractions() {
  elements.form.addEventListener("submit", submitResearch);
  elements.prompt.addEventListener("input", resizeComposer);
  elements.prompt.addEventListener("keydown", event => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      elements.form.requestSubmit();
    }
  });
  document.querySelector("#new-research").addEventListener("click", resetResearch);
  document.querySelector("#open-navigation").addEventListener("click", () => openDrawer(elements.navigation));
  document.querySelector("#close-navigation").addEventListener("click", closeDrawers);
  document.querySelector("#open-inspector").addEventListener("click", () => openDrawer(elements.inspector));
  document.querySelector("#close-inspector").addEventListener("click", closeDrawers);
  elements.scrim.addEventListener("click", closeDrawers);
  document.addEventListener("keydown", event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      resetResearch();
    }
    if (event.key === "Escape") closeDrawers();
  });
  document.querySelectorAll(".inspector-tab").forEach(tab => {
    tab.addEventListener("click", () => {
      document.querySelectorAll(".inspector-tab").forEach(candidate => candidate.classList.toggle("active", candidate === tab));
      document.querySelectorAll(".tab-panel").forEach(panel => panel.classList.toggle("active", panel.id === `tab-${tab.dataset.tab}`));
    });
  });
}

async function start() {
  bindInteractions();
  try {
    renderBootstrap(await requestJSON("/api/v1/bootstrap"));
  } catch (error) {
    elements.runtimeLabel.textContent = "Runtime unavailable";
    elements.accelerator.textContent = "Interface disconnected";
    elements.softwareStack.textContent = error.message;
  }
}

start();
