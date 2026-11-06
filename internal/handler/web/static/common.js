function esc(s){var d=document.createElement("div");d.textContent=s==null?"":String(s);return d.innerHTML;}
function escAttr(s){ return esc(s).replace(/\\/g, "\\\\").replace(/'/g, "\\'"); }
function flash(msg, kind){
  var bar = document.getElementById("flashBar");
  if(!bar){
    bar = document.createElement("div");
    bar.id = "flashBar";
    bar.className = "flash-bar";
    document.querySelector(".container").prepend(bar);
  }
  bar.textContent = msg;
  bar.className = "flash-bar flash-" + (kind||"info");
  bar.style.display = "block";
  if(kind !== "persistent"){
    setTimeout(function(){ bar.style.display = "none"; }, 3500);
  }
}
function showPersistentBanner(html){
  var bar = document.getElementById("persistBar");
  if(!bar){
    bar = document.createElement("div");
    bar.id = "persistBar";
    bar.className = "persist-bar";
    document.querySelector(".container").prepend(bar);
  }
  bar.innerHTML = html;
  bar.style.display = "block";
}
function dismissPersistent(){
  var bar = document.getElementById("persistBar");
  if(bar) bar.style.display = "none";
}
function apiPost(url, body){
  return fetch(url, {
    method: "POST",
    headers: {"Content-Type":"application/json"},
    credentials: "same-origin",
    body: JSON.stringify(body)
  }).then(function(r){
    return r.json().then(function(j){ return {ok: r.ok, status: r.status, json: j}; });
  });
}
function apiGet(url){
  return fetch(url, {credentials:"same-origin"}).then(function(r){
    if(!r.ok) throw new Error("HTTP "+r.status);
    return r.json();
  });
}
// attachFilter wires a text input to hide non-matching rows of a tbody.
// Rows marked data-detail="1" follow the visibility of the row above them
// (used by the chat page's expandable backend rows; data-open tracks state).
function attachFilter(inputId, tbodyId){
  var inp = document.getElementById(inputId);
  if(!inp) return;
  inp.addEventListener("input", function(){ applyFilter(inputId, tbodyId); });
}
function applyFilter(inputId, tbodyId){
  var inp = document.getElementById(inputId);
  var q = (inp.value||"").toLowerCase();
  var rows = document.getElementById(tbodyId).rows;
  for(var i=0;i<rows.length;i++){
    var r = rows[i];
    if(r.dataset.detail !== undefined) continue;
    var match = !q || r.textContent.toLowerCase().indexOf(q) >= 0;
    r.style.display = match ? "" : "none";
    var next = rows[i+1];
    if(next && next.dataset.detail !== undefined){
      next.style.display = (match && next.dataset.open === "1") ? "" : "none";
    }
  }
}
function reapplyFilter(inputId, tbodyId){
  var inp = document.getElementById(inputId);
  if(inp && inp.value) applyFilter(inputId, tbodyId);
}
var __confirmFn = null;
function openConfirm(title, bodyHTML, hintText, btnLabel, fn){
  document.getElementById("confirmTitle").textContent = title;
  document.getElementById("confirmBody").innerHTML = bodyHTML;
  document.getElementById("confirmActivity").textContent = hintText || "";
  var btn = document.getElementById("confirmBtn");
  btn.textContent = btnLabel;
  __confirmFn = fn;
  document.getElementById("confirmModal").classList.add("open");
  btn.focus();
}
function closeConfirm(){
  document.getElementById("confirmModal").classList.remove("open");
  __confirmFn = null;
}
document.addEventListener("DOMContentLoaded", function(){
  var btn = document.getElementById("confirmBtn");
  if(btn) btn.addEventListener("click", function(){ var fn = __confirmFn; closeConfirm(); if(fn) fn(); });
});
document.addEventListener("keydown", function(e){ if(e.key === "Escape") closeConfirm(); });
var __tipEl=null;
function showTip(t){
  hideTip();
  var txt=t.getAttribute("data-tip"); if(!txt) return;
  __tipEl=document.createElement("div"); __tipEl.className="tooltip-pop"; __tipEl.textContent=txt;
  document.body.appendChild(__tipEl);
  var r=t.getBoundingClientRect();
  var top=r.top-8-__tipEl.offsetHeight, left=r.left+r.width/2-__tipEl.offsetWidth/2;
  if(top<4) top=r.bottom+8;
  if(left<4) left=4;
  var maxLeft=window.innerWidth-__tipEl.offsetWidth-4;
  if(left>maxLeft) left=maxLeft;
  __tipEl.style.top=top+"px"; __tipEl.style.left=left+"px";
}
function hideTip(){ if(__tipEl){ __tipEl.remove(); __tipEl=null; } }
document.addEventListener("mouseover", function(e){ var t=e.target.closest(".tip"); if(t) showTip(t); });
document.addEventListener("mouseout", function(e){ var t=e.target.closest(".tip"); if(t) hideTip(); });
document.addEventListener("focusin", function(e){ var t=e.target.closest(".tip"); if(t) showTip(t); });
document.addEventListener("focusout", function(e){ var t=e.target.closest(".tip"); if(t) hideTip(); });
function copyToClipboard(txt){
  if(navigator.clipboard && navigator.clipboard.writeText){
    return navigator.clipboard.writeText(txt);
  }
  var ta = document.createElement("textarea");
  ta.value = txt; document.body.appendChild(ta); ta.select();
  try { document.execCommand("copy"); } finally { document.body.removeChild(ta); }
  return Promise.resolve();
}
