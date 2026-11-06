// Model data and proxy capabilities injected server-side.
var PROXY_ORIGIN = location.origin;
var PROXY_URL = PROXY_ORIGIN + "/v1";

// ---- Populate model overview table ----
(function(){
  var tbody = document.getElementById("modelTableBody");
  for (var i = 0; i < MODELS.length; i++) {
    var m = MODELS[i];
    var row = document.createElement("tr");
    var badges = "";
    if (m.supports_vision) badges += ' <span class="tag tag-cap">vision</span>';
    if (m.supports_audio) badges += ' <span class="tag tag-cap">audio</span>';
    var healthStatus = HEALTH[m.id] || { online: true, error: '' };
    var status = healthStatus.online
      ? '<span class="health-dot health-online"></span><span class="st-word">online</span>'
      : '<span class="health-dot health-offline"></span><span class="st-word" style="color:var(--red)" title="' + esc(healthStatus.error || '') + '">offline</span>';
    var path = m.protocol === "anthropic" ? "/anthropic" : "/v1";
    var fullURL = PROXY_ORIGIN + path;
    var urlCell = '<code>' + path + '</code> <button class="copy-icon" type="button" title="Copy ' + esc(fullURL) + '" onclick="copyText(\'' + esc(fullURL) + '\', this)"><svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="5" y="5" width="9" height="9"/><path d="M11 5V2H2v9h3"/></svg></button>';
    var safety;
    if (m.local) {
      safety = '<span class="tag tag-local" title="Runs on your own infrastructure. No third party sees your data.">Local &middot; safe</span>';
    } else if (m.type === 'bedrock') {
      safety = '<span class="tag tag-bedrock" title="Hosted by AWS Bedrock. Model provider has no access to your data. AWS does not log or train on prompts.">Bedrock &middot; safe</span>';
    } else {
      safety = '<span class="tag tag-3p" title="Sent directly to the model provider. Subject to their terms of service.">3rd party</span>';
    }
    row.innerHTML = '<td><strong>' + esc(m.id) + '</strong>' + badges + '</td>' +
      '<td>' + status + '</td>' +
      '<td><span class="mono">' + m.protocol + '</span></td>' +
      '<td>' + urlCell + '</td>' +
      '<td>' + (m.context_window > 0 ? m.context_window.toLocaleString() : '<span class="mono">&mdash;</span>') + '</td>' +
      '<td>' + safety + '</td>';
    tbody.appendChild(row);
  }
})();

function esc(s){ var d=document.createElement("div"); d.textContent=s; return d.innerHTML; }
function copyText(txt, btn){
  function done(){ if(btn){ var t = btn.innerHTML; btn.innerHTML = "&check;"; setTimeout(function(){ btn.innerHTML = t; }, 1200); } }
  if(navigator.clipboard && navigator.clipboard.writeText){ navigator.clipboard.writeText(txt).then(done); return; }
  var ta = document.createElement("textarea");
  ta.value = txt; ta.style.position = "fixed"; ta.style.opacity = "0";
  document.body.appendChild(ta); ta.select();
  try { document.execCommand("copy"); done(); } finally { document.body.removeChild(ta); }
}
function openGen(){ document.getElementById("genModal").classList.add("open"); }
function closeGen(){ document.getElementById("genModal").classList.remove("open"); }
document.addEventListener("keydown", function(e){ if(e.key === "Escape") closeGen(); });
document.getElementById("epOai").textContent = PROXY_URL;
document.getElementById("epAnt").textContent = PROXY_ORIGIN + "/anthropic";
(function(){
  var inp = document.getElementById("modelTableFilter");
  inp.addEventListener("input", function(){
    var q = inp.value.toLowerCase();
    var rows = document.getElementById("modelTableBody").rows;
    for(var i=0;i<rows.length;i++){
      rows[i].style.display = (!q || rows[i].textContent.toLowerCase().indexOf(q) >= 0) ? "" : "none";
    }
  });
})();
function getModel(id){ for(var i=0;i<MODELS.length;i++) if(MODELS[i].id===id) return MODELS[i]; return null; }

// ---- Harness change ----
var harnessEl    = document.getElementById("harness");
var claudeSel    = document.getElementById("claudeSelectors");
var openCodeSel  = document.getElementById("openCodeSelectors");
var multiSel     = document.getElementById("multiSelectors");
var codexSel     = document.getElementById("codexSelectors");
var generateBtn  = document.getElementById("generateBtn");

harnessEl.addEventListener("change", function(){
  var h = this.value;
  claudeSel.classList.toggle("hidden", h!=="claude-code");
  openCodeSel.classList.toggle("hidden", h!=="opencode");
  multiSel.classList.toggle("hidden", h!=="qwen-code");
  codexSel.classList.toggle("hidden", h!=="codex");
  document.getElementById("outputFormatField").classList.toggle("hidden", h!=="claude-code" && h!=="codex");
  generateBtn.disabled = !h;
  document.getElementById("outputArea").classList.remove("visible");
  if(h==="claude-code") populateClaudeSelects();
  if(h==="codex") populateCodexSelects();
  if(h==="opencode") populateOpenCodeSelects();
  if(h==="qwen-code") populateMultiSelects();
});

function chatModels(){ return MODELS.filter(function(m){ return !m.id.toLowerCase().match(/embed/); }); }

function optionText(m){
  var tags=[];
  if(m.protocol==="anthropic") tags.push("Anthropic API");
  if(!m.local) tags.push("3rd party");
  var h=HEALTH[m.id];
  if(h && !h.online) tags.push("OFFLINE");
  return m.id + (tags.length ? "  ["+tags.join(", ")+"]" : "");
}


function populateSelects(ids, defaults){
  var cms = chatModels();
  ids.forEach(function(id){
    var sel=document.getElementById(id);
    sel.innerHTML="";
    cms.forEach(function(m){
      var o=document.createElement("option"); o.value=m.id; o.textContent=optionText(m);
      sel.appendChild(o);
    });
  });
  Object.keys(defaults).forEach(function(id){ setDefault(id,defaults[id]); });
}

function populateClaudeSelects(){
  populateSelects(["sonnetModel","opusModel","haikuModel"], {});
}

function populateCodexSelects(){
  var cms = chatModels().filter(function(m){ return m.protocol !== "anthropic"; });
  var sel = document.getElementById("codexModel");
  sel.innerHTML="";
  cms.forEach(function(m){
    var o=document.createElement("option"); o.value=m.id; o.textContent=optionText(m);
    sel.appendChild(o);
  });
  sel.addEventListener("change", updateCodexCtxHint);
  updateCodexCtxHint();
}

function updateCodexCtxHint(){
  var m = getModel(document.getElementById("codexModel").value);
  var hint = document.getElementById("codexCtxHint");
  var sel = document.getElementById("codexCtxWindow");
  var detected = m && m.context_window > 0;
  if(detected){
    var label = m.context_window >= 1024 ? Math.round(m.context_window/1024)+"K" : m.context_window;
    hint.textContent = "Detected: " + label + " tokens";
    hint.style.color = "";
    sel.style.borderColor = "";
  } else {
    hint.textContent = "Not detected \u2014 set manually for best results";
    hint.style.color = "var(--danger)";
    sel.style.borderColor = "var(--danger)";
  }
}

function getCodexCtxWindow(){
  var sel = document.getElementById("codexCtxWindow");
  if(sel.value === "auto"){
    var m = getModel(document.getElementById("codexModel").value);
    return (m && m.context_window > 0) ? m.context_window : 0;
  }
  return parseInt(sel.value, 10);
}

// Sync checkboxes: check+disable models selected in dropdowns
function syncCheckboxes(containerId, selectIds){
  var selected = {};
  selectIds.forEach(function(id){ selected[document.getElementById(id).value]=true; });
  var cbs = document.querySelectorAll("#"+containerId+" input[type=checkbox]");
  for(var i=0;i<cbs.length;i++){
    if(selected[cbs[i].value]){
      cbs[i].checked=true;
      cbs[i].disabled=true;
    } else {
      cbs[i].disabled=false;
    }
  }
}

function buildCheckboxGroup(containerId, selectIds){
  var container = document.getElementById(containerId);
  container.innerHTML="";
  chatModels().forEach(function(m){
    var label=document.createElement("label");
    var cb=document.createElement("input"); cb.type="checkbox"; cb.value=m.id; cb.checked=true;
    label.appendChild(cb);
    var safety;
    if (m.local) {
      safety = ' <span class="tag" style="margin-left:4px">Local</span>';
    } else if (m.type === 'bedrock') {
      safety = ' <span class="tag" style="margin-left:4px">Bedrock</span>';
    } else {
      safety = ' <span class="tag tag-warn" style="margin-left:4px">3rd party</span>';
    }
    var proto = m.protocol==="anthropic"
      ? ' <span class="tag tag-cap" style="margin-left:4px">Anthropic</span>' : '';
    var span=document.createElement("span");
    span.innerHTML = esc(m.id) + safety + proto;
    label.appendChild(span);
    container.appendChild(label);
  });
  var sync = function(){ syncCheckboxes(containerId, selectIds); };
  sync();
  selectIds.forEach(function(id){ document.getElementById(id).onchange = sync; });
}

function populateOpenCodeSelects(){
  populateSelects(["buildModel","planModel"], {});
  buildCheckboxGroup("ocAdditionalModels", ["buildModel","planModel"]);
}

function populateMultiSelects(){
  var cms = chatModels();
  var defSel = document.getElementById("defaultModel");
  defSel.innerHTML="";
  cms.forEach(function(m){
    var o=document.createElement("option"); o.value=m.id; o.textContent=optionText(m);
    defSel.appendChild(o);
  });
  buildCheckboxGroup("additionalModels", ["defaultModel"]);
}

function setDefault(id,val){ var s=document.getElementById(id); for(var i=0;i<s.options.length;i++) if(s.options[i].value===val){s.value=val;return;} }
function selectedAdditional(){ var r=[]; var cbs=document.querySelectorAll("#additionalModels input:checked"); for(var i=0;i<cbs.length;i++) r.push(cbs[i].value); return r; }

// ---- Generate ----
function getOutputFormat(){
  var r = document.querySelector('input[name="outputFormat"]:checked');
  return r ? r.value : "config";
}

function generate(){
  var harness = harnessEl.value;
  var apiKey = document.getElementById("apiKey").value.trim() || "<your-proxy-api-key>";
  var fmt = getOutputFormat();
  var result;
  switch(harness){
    case "claude-code":
      result = fmt==="command" ? genClaudeCodeCommand(apiKey) : genClaudeCode(apiKey);
      break;
    case "codex":
      result = fmt==="command" ? genCodexCommand(apiKey) : genCodex(apiKey);
      break;
    case "qwen-code":   result = genQwenCode(apiKey); break;
    case "opencode":    result = genOpenCode(apiKey); break;
  }
  if(result) renderOutput(result);
}

function renderOutput(r){
  var area = document.getElementById("outputArea");
  area.classList.add("visible");

  document.getElementById("configTitle").textContent = r.title || "Configuration File";
  var co = document.getElementById("configOutput");

  if(r.configTabs){
    // Tabbed code blocks (e.g. start command with macOS/Linux + PowerShell)
    var tabKeys = Object.keys(r.configTabs);
    var tabsHtml = '<div class="tabs" style="margin-bottom:0">';
    tabKeys.forEach(function(k,i){
      tabsHtml += '<button class="tab' + (i===0?' active':'') + '" onclick="switchCmdTab(this,\'' + i + '\')">' + esc(k) + '</button>';
    });
    tabsHtml += '</div>';
    tabKeys.forEach(function(k,i){
      tabsHtml += '<div class="cmd-tab' + (i===0?' active':'') + '" data-idx="' + i + '">' +
        '<div class="code-block"><button class="copy-btn" onclick="copyCode(this)">Copy</button>' + esc(r.configTabs[k]) + '</div></div>';
    });
    co.innerHTML = tabsHtml;
  } else {
    co.innerHTML = '<div style="margin-bottom:6px"><span class="file-path">' + esc(r.filename) + '</span></div>' +
      '<div class="code-block"><button class="copy-btn" onclick="copyCode(this)">Copy</button>' + esc(r.config) + '</div>';
  }

  // Download buttons (per-OS scripts)
  if(r.downloads){
    var dlHtml = '<div style="margin-top:10px">';
    r.downloads.forEach(function(d){
      dlHtml += '<a class="dl-btn" href="#" onclick="downloadFile(\'' + esc(d.name) + '\',this);return false" data-content="' +
        btoa(d.content) + '"><svg viewBox="0 0 16 16"><path d="M8 1v9m0 0L5 7m3 3l3-3M2 12v1a2 2 0 002 2h8a2 2 0 002-2v-1" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>' +
        esc(d.label) + '</a>';
    });
    dlHtml += '</div>';
    co.innerHTML += dlHtml;
  }

  ["macos","linux","windows"].forEach(function(os){
    document.getElementById("os-"+os).innerHTML = '<div class="install-steps">' + r.install[os] + '</div>';
  });
  area.scrollIntoView({behavior:"smooth",block:"start"});
}

// ---- Claude Code ----
function thinkingCaps(checkboxId){
  return document.getElementById(checkboxId).checked ? "thinking,interleaved_thinking" : "";
}

// Shared env var builder for both config-file and start-command modes.
function claudeEnvVars(apiKey){
  var sonnetId = document.getElementById("sonnetModel").value;
  var opusId = document.getElementById("opusModel").value;
  var haikuId = document.getElementById("haikuModel").value;
  return [
    ["ANTHROPIC_BASE_URL", PROXY_ORIGIN],
    ["ANTHROPIC_API_KEY", apiKey],
    ["ANTHROPIC_DEFAULT_SONNET_MODEL", sonnetId],
    ["ANTHROPIC_DEFAULT_SONNET_MODEL_NAME", displayName(sonnetId)],
    ["ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES", thinkingCaps("sonnetThinking")],
    ["ANTHROPIC_DEFAULT_OPUS_MODEL", opusId],
    ["ANTHROPIC_DEFAULT_OPUS_MODEL_NAME", displayName(opusId)],
    ["ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES", thinkingCaps("opusThinking")],
    ["ANTHROPIC_DEFAULT_HAIKU_MODEL", haikuId],
    ["ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME", displayName(haikuId)],
    ["ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES", thinkingCaps("haikuThinking")],
    ["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1"],
    ["CLAUDE_CODE_AUTO_MODE_SERVER", "0"],
    ["API_TIMEOUT_MS", "900000"]
  ].concat(claudeContextVars(sonnetId));
}

// The main model (the Sonnet slot, Claude Code's default) sets the context:
// its window, and auto-compaction at 85% of it. Unknown window: Claude Code's
// own default (200K) applies.
function claudeContextVars(modelId){
  var m = getModel(modelId);
  if(!m || !(m.context_window > 0)) return [];
  return [
    ["CLAUDE_CODE_MAX_CONTEXT_TOKENS", String(m.context_window)],
    ["CLAUDE_CODE_AUTO_COMPACT_WINDOW", String(Math.floor(m.context_window * 0.85))]
  ];
}

function genClaudeCode(apiKey){
  var env = {};
  claudeEnvVars(apiKey).forEach(function(kv){ env[kv[0]]=kv[1]; });

  var settings = {
    attribution: { commit: "", pr: "" },
    permissions: { defaultMode: "auto" },
    env: env
  };

  var fn = "settings.json";

  var macLinuxSteps = [
    'Create the config directory:<br><code>mkdir -p ~/.claude</code>',
    'Save the generated file as:<br><code>~/.claude/settings.json</code>'
  ];
  
  
  macLinuxSteps.push('Restart Claude Code for changes to take effect.');

  var winSteps = [
    'Create the config directory:<br><code>mkdir %USERPROFILE%\\.claude</code>',
    'Save the generated file as:<br><code>%USERPROFILE%\\.claude\\settings.json</code>'
  ];
  
  
  winSteps.push('Restart Claude Code for changes to take effect.');

  return {
    config: JSON.stringify(settings, null, 2),
    filename: fn,
    install: {
      macos: ol(macLinuxSteps),
      linux: ol(macLinuxSteps),
      windows: ol(winSteps)
    }
  };
}

// ---- Claude Code (start command) ----
function genClaudeCodeCommand(apiKey){
  var vars = claudeEnvVars(apiKey);
  var settingsJSON = JSON.stringify({attribution:{commit:"",pr:""}});


  var shLines = ["#!/usr/bin/env bash", "# go-llm-proxy: Claude Code start script", ""];
  
  shLines.push("env \\");
  vars.forEach(function(kv){ shLines.push('  ' + kv[0] + '="' + kv[1] + '" \\'); });
  shLines.push("  claude --permission-mode auto --settings '" + settingsJSON + "' \"$@\"");
  var shContent = shLines.join("\n") + "\n";

  var batLines = ["@echo off", "setlocal", "REM go-llm-proxy: Claude Code start script", ""];
  vars.forEach(function(kv){ batLines.push("set " + kv[0] + "=" + kv[1]); });
  
  batLines.push("", 'claude --permission-mode auto --settings "' + settingsJSON.replace(/"/g, '\\"') + '" %*');
  var batContent = batLines.join("\r\n") + "\r\n";

  var ps1Lines = ["# go-llm-proxy: Claude Code start script", ""];
  vars.forEach(function(kv){ ps1Lines.push('$env:' + kv[0] + ' = "' + kv[1] + '"'); });
  
  // Write settings JSON to a temp file to avoid PowerShell quoting issues
  // with inline JSON containing curly braces and colons.
  ps1Lines.push("", "$settingsFile = [System.IO.Path]::GetTempFileName()");
  ps1Lines.push("'" + settingsJSON + "' | Set-Content -Path $settingsFile -Encoding UTF8");
  ps1Lines.push("try {");
  ps1Lines.push("  claude --permission-mode auto --settings $settingsFile @args");
  ps1Lines.push("} finally {");
  ps1Lines.push("  Remove-Item $settingsFile -ErrorAction SilentlyContinue");
  vars.forEach(function(kv){ ps1Lines.push('  Remove-Item Env:' + kv[0] + ' -ErrorAction SilentlyContinue'); });
  ps1Lines.push("}");
  var ps1Content = ps1Lines.join("\r\n") + "\r\n";

  var shDisplay = shLines.slice(2).join("\n").trim();
  var ps1Display = ps1Lines.slice(2).join("\n").trim();

  return {
    title: "Start Command",
    configTabs: {
      "macOS / Linux": shDisplay,
      "PowerShell": ps1Display
    },
    downloads: [
      { name: "claude-proxy.sh", label: "Download .sh (macOS/Linux)", content: shContent },
      { name: "claude-proxy.bat", label: "Download .bat (Windows)", content: batContent },
      { name: "claude-proxy.ps1", label: "Download .ps1 (PowerShell)", content: ps1Content }
    ],
    install: {
      macos: ol([
        'Download <code>claude-proxy.sh</code> using the button above.',
        'Make it executable:<br><code>chmod +x claude-proxy.sh</code>',
        'Run it:<br><code>./claude-proxy.sh</code>',
        'Optional: move it to your PATH for easy access:<br><code>mv claude-proxy.sh /usr/local/bin/claude-proxy</code>',
        'Then launch from anywhere:<br><code>claude-proxy</code>'
      ]),
      linux: ol([
        'Download <code>claude-proxy.sh</code> using the button above.',
        'Make it executable:<br><code>chmod +x claude-proxy.sh</code>',
        'Run it:<br><code>./claude-proxy.sh</code>',
        'Optional: move it to your PATH for easy access:<br><code>mv claude-proxy.sh ~/.local/bin/claude-proxy</code>',
        'Then launch from anywhere:<br><code>claude-proxy</code>'
      ]),
      windows: ol([
        'Download <code>claude-proxy.bat</code> or <code>claude-proxy.ps1</code> using the buttons above.',
        'For <strong>.bat</strong>: Double-click the file, or run from Command Prompt:<br><code>claude-proxy.bat</code>',
        'For <strong>.ps1</strong>: Run from PowerShell:<br><code>powershell -ExecutionPolicy Bypass -File .\\claude-proxy.ps1</code> (Windows refuses to run downloaded scripts by default; this allows it for this one run)',
        'Optional: move the script to a folder in your PATH for easy access.'
      ])
    }
  };
}

// ---- Codex ----
function codexToml(modelId, effort, apiKey){
  var ctxWindow = getCodexCtxWindow();
  var toml = 'model = "' + modelId + '"\n' +
    'model_provider = "go-llm-proxy"\n' +
    'model_reasoning_effort = "' + effort + '"\n';
  // Disable built-in web_search when proxy doesn't handle it (client MCP or no search).
  // When proxy has search, leave it enabled so proxy-side search works.
  
  if(ctxWindow > 0){
    toml += 'model_context_window = ' + ctxWindow + '\n';
  }
  toml += '\n[model_providers.go-llm-proxy]\n' +
    'name = "Go-LLM-Proxy"\n' +
    'base_url = "' + PROXY_URL + '"\n' +
    'wire_api = "responses"\n' +
    '# API key embedded directly (no env var needed):\n' +
    'experimental_bearer_token = "' + apiKey + '"\n' +
    '# Or use an environment variable instead:\n' +
    '# env_key = "OPENAI_API_KEY"\n';

  
  return toml;
}

function genCodex(apiKey){
  var modelId = document.getElementById("codexModel").value;
  var effort = document.getElementById("codexEffort").value;
  var toml = codexToml(modelId, effort, apiKey);


  var unixSteps = [
    'Create the config directory:<br><code>mkdir -p ~/.codex</code>',
    'Save the generated file as:<br><code>~/.codex/config.toml</code>',
    'The API key' + (' is') + ' embedded in the config. To use environment variables instead, ' +
      'edit the file and swap the commented lines, then set:<br>' +
      '<code>export OPENAI_API_KEY=' + esc(apiKey) + '</code>' +
      (''),
    'Restart Codex for changes to take effect.' 
  ];

  var winSteps = [
    'Create the config directory:<br><code>mkdir %USERPROFILE%\\.codex</code>',
    'Save the generated file as:<br><code>%USERPROFILE%\\.codex\\config.toml</code>',
    'The API key' + (' is') + ' embedded in the config. To use environment variables instead, ' +
      'edit the file and swap the commented lines, then set:<br>' +
      '<code>setx OPENAI_API_KEY ' + esc(apiKey) + '</code>' +
      (''),
    'Restart Codex for changes to take effect.' 
  ];

  return {
    config: toml,
    filename: "config.toml",
    install: {
      macos: ol(unixSteps),
      linux: ol(unixSteps),
      windows: ol(winSteps)
    }
  };
}

function genCodexCommand(apiKey){
  var modelId = document.getElementById("codexModel").value;
  var effort = document.getElementById("codexEffort").value;

  var ctxWindow = getCodexCtxWindow();
  var cfgFlags = [
    '-c \'model="' + modelId + '"\'',
    '-c \'model_provider="go-llm-proxy"\'',
    '-c \'model_reasoning_effort="' + effort + '"\''
  ];
  
  if(ctxWindow > 0){
    cfgFlags.push('-c \'model_context_window=' + ctxWindow + '\'');
  }
  cfgFlags.push(
    '-c \'model_providers.go-llm-proxy.name="Go-LLM-Proxy"\'',
    '-c \'model_providers.go-llm-proxy.base_url="' + PROXY_URL + '"\'',
    '-c \'model_providers.go-llm-proxy.env_key="OPENAI_API_KEY"\'',
    '-c \'model_providers.go-llm-proxy.wire_api="responses"\''
  );
  

  // Build PowerShell array entries
  var ps1FlagPairs = cfgFlags.map(function(f){
    var val = f.replace(/^-c '/,"").replace(/'$/,"");
    return "  '-c', '" + val + "'";
  });

  var shLines = [
    "#!/usr/bin/env bash",
    "# go-llm-proxy: Codex start script",
    "",
    'export OPENAI_API_KEY="' + apiKey + '"'
  ];
  
  shLines.push("");
  shLines.push("codex \\");
  cfgFlags.forEach(function(f){
    shLines.push("  " + f + " \\");
  });
  shLines.push('  "$@"');
  var shContent = shLines.join("\n") + "\n";

  var ps1Lines = [
    "# go-llm-proxy: Codex start script",
    "",
    '$env:OPENAI_API_KEY = "' + apiKey + '"'
  ];
  
  ps1Lines.push("");
  ps1Lines.push("# Build argument list.");
  ps1Lines.push("$codexArgs = @(");
  ps1FlagPairs.forEach(function(p){ ps1Lines.push(p); });
  ps1Lines.push(") + $args");
  ps1Lines.push("");
  ps1Lines.push("try {");
  ps1Lines.push("  codex @codexArgs");
  ps1Lines.push("} finally {");
  ps1Lines.push("  Remove-Item Env:OPENAI_API_KEY -ErrorAction SilentlyContinue");
  
  ps1Lines.push("}");
  var ps1Content = ps1Lines.join("\r\n") + "\r\n";

  var shDisplay = shLines.slice(2).join("\n").trim();
  var ps1Display = ps1Lines.slice(2).join("\n").trim();

  return {
    title: "Start Command",
    configTabs: {
      "macOS / Linux": shDisplay,
      "PowerShell": ps1Display
    },
    downloads: [
      { name: "codex-proxy.sh", label: "Download .sh (macOS/Linux)", content: shContent },
      { name: "codex-proxy.ps1", label: "Download .ps1 (PowerShell)", content: ps1Content }
    ],
    install: {
      macos: ol([
        'Download <code>codex-proxy.sh</code> using the button above.',
        'Make it executable:<br><code>chmod +x codex-proxy.sh</code>',
        'Run it:<br><code>./codex-proxy.sh</code>',
        'Optional: move it to your PATH:<br><code>mv codex-proxy.sh /usr/local/bin/codex-proxy</code>'
      ]),
      linux: ol([
        'Download <code>codex-proxy.sh</code> using the button above.',
        'Make it executable:<br><code>chmod +x codex-proxy.sh</code>',
        'Run it:<br><code>./codex-proxy.sh</code>',
        'Optional: move it to your PATH:<br><code>mv codex-proxy.sh ~/.local/bin/codex-proxy</code>'
      ]),
      windows: ol([
        'Download <code>codex-proxy.ps1</code> using the button above.',
        'Run from PowerShell:<br><code>powershell -ExecutionPolicy Bypass -File .\\codex-proxy.ps1</code> (Windows refuses to run downloaded scripts by default; this allows it for this one run)',
        'Optional: move the script to a folder in your PATH.'
      ])
    }
  };
}

// ---- Qwen Code ----
function genQwenCode(apiKey){
  var defModel = document.getElementById("defaultModel").value;
  var additional = selectedAdditional();
  var defInfo = getModel(defModel);

  var envKeyName = "PROXY_API_KEY";
  var oaiModels = [];
  var antModels = [];
  additional.forEach(function(id){
    var m = getModel(id);
    if(!m) return;
    var entry = {
      id: id,
      name: displayName(id),
      envKey: envKeyName,
      baseUrl: m.protocol==="anthropic" ? (PROXY_ORIGIN+"/anthropic") : PROXY_URL,
      generationConfig: { timeout: 300000, maxRetries: 1 }
    };
    if(m.protocol==="anthropic") antModels.push(entry);
    else oaiModels.push(entry);
  });

  var mp = {};
  if(oaiModels.length) mp.openai = oaiModels;
  if(antModels.length) mp.anthropic = antModels;

  var authType = defInfo && defInfo.protocol==="anthropic" ? "anthropic" : "openai";

  var obj = {
    "$version": 3,
    model: { name: defModel },
    security: { auth: { selectedType: authType } },
    modelProviders: mp,
    env: {}
  };

  obj.env[envKeyName] = apiKey;

  

  var unixSteps = ol([
    'Create the config directory:<br><code>mkdir -p ~/.qwen</code>',
    'Save the generated file as:<br><code>~/.qwen/settings.json</code>',
    'Launch Qwen Code. Use <code>/model</code> to switch between models.'
  ]);

  return {
    config: JSON.stringify(obj, null, 2),
    filename: "settings.json",
    install: {
      macos: unixSteps,
      linux: unixSteps,
      windows: ol([
        'Create the config directory:<br><code>mkdir %USERPROFILE%\\.qwen</code>',
        'Save the generated file as:<br><code>%USERPROFILE%\\.qwen\\settings.json</code>',
        'Launch Qwen Code. Use <code>/model</code> to switch between models.'
      ])
    }
  };
}

// ---- OpenCode ----
function genOpenCode(apiKey){
  var agentId = document.getElementById("buildModel").value;
  var plannerId = document.getElementById("planModel").value;

  var selectedOC = {};
  var cbs = document.querySelectorAll("#ocAdditionalModels input:checked");
  for(var i=0;i<cbs.length;i++) selectedOC[cbs[i].value]=true;
  selectedOC[agentId]=true;
  selectedOC[plannerId]=true;

  var oaiModels = chatModels().filter(function(m){ return selectedOC[m.id] && m.protocol!=="anthropic"; });
  var antModels = chatModels().filter(function(m){ return selectedOC[m.id] && m.protocol==="anthropic"; });

  var oaiModelsObj = {};
  oaiModels.forEach(function(m){ oaiModelsObj[m.id] = { name: displayName(m.id) }; });

  var antModelsObj = {};
  antModels.forEach(function(m){ antModelsObj[m.id] = { name: displayName(m.id) }; });

  var providers = {};
  if(oaiModels.length){
    providers["go-llm-proxy"] = {
      npm: "@ai-sdk/openai-compatible",
      name: "go-llm-proxy (OpenAI)",
      options: { baseURL: PROXY_URL, apiKey: apiKey },
      models: oaiModelsObj
    };
  }
  if(antModels.length){
    providers["go-llm-proxy-ant"] = {
      npm: "@ai-sdk/anthropic",
      name: "go-llm-proxy (Anthropic)",
      options: { baseURL: PROXY_ORIGIN + "/anthropic/v1", apiKey: apiKey },
      models: antModelsObj
    };
  }

  function ocModel(id){
    var m = getModel(id);
    if(m && m.protocol==="anthropic") return "go-llm-proxy-ant/" + id;
    return "go-llm-proxy/" + id;
  }

  var obj = {
    "$schema": "https://opencode.ai/config.json",
    provider: providers,
    model: ocModel(agentId),
    small_model: ocModel(agentId),
    agent: {
      build: { model: ocModel(agentId), description: "Coding agent" },
      plan:  { model: ocModel(plannerId), description: "Planning agent" }
    }
  };

  var mcpObj = {};
  
  
  if(Object.keys(mcpObj).length) obj.mcp = mcpObj;

  var unixSteps = ol([
    'Save <code>opencode.json</code> to your project root, or globally:<br><code>mkdir -p ~/.config/opencode &amp;&amp; cp opencode.json ~/.config/opencode/opencode.json</code>',
    'Launch OpenCode from your project directory.'
  ]);

  return {
    config: JSON.stringify(obj, null, 2),
    filename: "opencode.json",
    install: {
      macos: unixSteps,
      linux: unixSteps,
      windows: ol([
        'Save <code>opencode.json</code> to your project root, or globally. The correct location depends on which OpenCode you run:',
        '<b>TUI (terminal):</b> <code>%APPDATA%\\opencode\\opencode.json</code>',
        '<b>GUI (desktop app):</b> <code>%USERPROFILE%\\.config\\opencode\\opencode.json</code>',
        'Launch OpenCode from your project directory.'
      ])
    }
  };
}

// ---- Helpers ----
function displayName(id){
  return {"MiniMax-M2.5":"MiniMax M2.5","MiniMax-M2.7":"MiniMax M2.7","qwen-3.5":"Qwen 3.5",
    "glm-5.1":"GLM 5.1","glm-4.7":"GLM 4.7","Nemotron-3-Super":"Nemotron 3 Super","nomic-embed":"Nomic Embed"}[id]||id;
}

function ol(items){ return "<ol>"+items.map(function(i){ return "<li>"+i+"</li>"; }).join("")+"</ol>"; }

function downloadFile(name, el){
  var raw = atob(el.dataset.content);
  var bytes = new Uint8Array(raw.length);
  for(var i=0;i<raw.length;i++) bytes[i]=raw.charCodeAt(i);
  var blob = new Blob([bytes], {type:"application/octet-stream"});
  var url = URL.createObjectURL(blob);
  var a = document.createElement("a");
  a.href = url; a.download = name; a.click();
  URL.revokeObjectURL(url);
}

function copyCode(btn){
  var text = btn.parentElement.textContent.replace(/^Copy/,"").trim();
  if(navigator.clipboard){
    navigator.clipboard.writeText(text).then(function(){
      btn.textContent="Copied!"; setTimeout(function(){btn.textContent="Copy";},1500);
    });
  } else {
    var ta=document.createElement("textarea");
    ta.value=text; ta.style.position="fixed"; ta.style.opacity="0";
    document.body.appendChild(ta); ta.select();
    document.execCommand("copy");
    document.body.removeChild(ta);
    btn.textContent="Copied!"; setTimeout(function(){btn.textContent="Copy";},1500);
  }
}

function switchCmdTab(btn, idx){
  var parent = btn.parentElement.parentElement;
  var tabs = parent.querySelectorAll(".tabs .tab");
  for(var i=0;i<tabs.length;i++) tabs[i].classList.remove("active");
  btn.classList.add("active");
  var ct = parent.querySelectorAll(".cmd-tab");
  for(var i=0;i<ct.length;i++) ct[i].classList.toggle("active",ct[i].dataset.idx===idx);
}

function switchOS(os){
  var tabs = document.querySelectorAll("#osTabs .tab");
  for(var i=0;i<tabs.length;i++) tabs[i].classList.toggle("active",tabs[i].dataset.os===os);
  ["macos","linux","windows"].forEach(function(o){ document.getElementById("os-"+o).classList.toggle("active",o===os); });
}
