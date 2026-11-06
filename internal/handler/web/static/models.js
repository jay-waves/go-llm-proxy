var chatState={models:[],aliases:[],unknownModels:[],editing:null,helpers:{},searchAction:null};
function loadModels(){apiGet('/admin/models/data').then(function(d){chatState.models=d.models||[];chatState.aliases=d.aliases||[];chatState.unknownModels=d.unknown_models||[];chatState.helpers=d.helpers||{};renderModels();renderAliases();renderHelpers();}).catch(function(e){flash('Load failed: '+e.message,'error');});}
function backendDot(b){
  var cls='health-unknown',label='no health data';
  if(b.disabled){cls='health-unknown';label='disabled';}
  else if(b.online===true){cls='health-online';label='online';}
  else if(b.online===false){cls='health-offline';label='offline'+(b.error?': '+b.error:'');}
  return '<span class="health-dot '+cls+'" title="'+escAttr(b.url)+' — '+escAttr(label)+'"></span>';
}
function modelDot(m){
  var h=m.health||{};
  if(h.online){
    var down=(m.backends||[]).filter(function(b){return !b.disabled&&b.online===false;}).length;
    if(down>0)return '<span class="health-dot health-degraded" title="Degraded: '+down+' backend(s) down"></span>';
    return '<span class="health-dot health-online" title="Online"></span>';
  }
  if(h.error)return '<span class="health-dot health-offline" title="Offline: '+escAttr(h.error)+'"></span>';
  return '<span class="health-dot health-unknown" title="No health data"></span>';
}
function renderModels(){var el=document.getElementById('modelsBody');if(!chatState.models.length){el.innerHTML='<tr><td colspan="7" class="empty-cell">No models configured</td></tr>';return;}el.innerHTML=chatState.models.map(function(m){var bs=m.backends||[];var active=bs.filter(function(b){return !b.disabled;}).length;var dots=bs.map(backendDot).join('');return '<tr>'+
  '<td>'+modelDot(m)+'<strong>'+esc(m.name)+'</strong></td>'+
  '<td class="mono">'+esc(m.type||'openai')+'</td>'+
  '<td class="mono">'+esc(m.model||m.name)+' <button class="copy-icon" type="button" title="Copy upstream model id" onclick="event.stopPropagation();copyToClipboard(\''+escAttr(m.model||m.name)+'\').then(function(){flash(\'Copied\',\'success\');})"><svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="5" y="5" width="9" height="9"/><path d="M11 5V2H2v9h3"/></svg></button></td>'+
  '<td>'+dots+' <span class="mono">'+active+'/'+bs.length+'</span></td>'+
  '<td class="mono">'+(function(){var c=[];if(m.supports_vision)c.push('vision');if(m.supports_audio)c.push('audio');if(m.code_link)c.push('code-link');return c.length?c.join(' \u00b7 '):'\u2014';})()+'</td>'+
  '<td class="mono">'+(function(){var g=[];if(m.force_pipeline)g.push('pipeline');if(chatState.helpers.vision&&!m.supports_vision)g.push('vision');return g.length?g.join(' \u00b7 '):'\u2014';})()+'</td>'+
  '<td class="row-actions"><div class="action-group"><button class="btn btn-secondary btn-sm btn-icon" onclick="openModel(\''+escAttr(m.name)+'\')" title="Edit model"><svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M11.3 2.2l2.5 2.5L5.5 13H3v-2.5z"/><path d="M9.8 3.7l2.5 2.5"/></svg></button><button class="btn btn-danger btn-sm btn-icon" onclick="deleteModel(\''+escAttr(m.name)+'\')" title="Delete model"><svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M2.5 4h11M5.5 4V2.5h5V4M4 4l.7 9.5h6.6L12 4M6.5 6.8v4.4M9.5 6.8v4.4"/></svg></button></div></td></tr>';}).join('');reapplyFilter('modelFilter','modelsBody');}
function renderAliases(){
  var unknown=document.getElementById('unknownModelsBody');
  if(unknown){unknown.innerHTML=chatState.unknownModels.length?chatState.unknownModels.map(function(u,i){
    var options=chatState.models.map(function(m){return '<option value="'+escAttr(m.name)+'">'+esc(m.name)+'</option>';}).join('');
    var seen=u.last_seen?new Date(u.last_seen).toLocaleString():'\u2014';
    var requester=u.requester||'Unauthenticated'; if(u.key_hash) requester+=' · '+u.key_hash;
    return '<tr><td><code>'+esc(u.id)+'</code></td><td class="mono">'+u.count+'</td><td>'+esc(seen)+'</td><td class="mono">'+esc(u.endpoint)+'</td><td>'+esc(requester)+'</td><td><div class="action-group"><select id="unknownTarget-'+i+'">'+options+'</select><button class="btn btn-primary btn-sm" type="button" onclick="aliasUnknown('+i+')" '+(options?'':'disabled')+'>Alias</button></div></td></tr>';
  }).join(''):'<tr><td colspan="6" class="empty-cell">No unrecognized model requests since restart</td></tr>';}
  var aliases=document.getElementById('aliasesBody');
  if(aliases){aliases.innerHTML=chatState.aliases.length?chatState.aliases.map(function(a,i){var options=chatState.models.map(function(m){return '<option value="'+escAttr(m.name)+'" '+(m.name===a.target?'selected':'')+'>'+esc(m.name)+'</option>';}).join('');return '<tr><td><code>'+esc(a.alias)+'</code></td><td><select id="aliasTarget-'+i+'">'+options+'</select></td><td class="row-actions"><div class="action-group"><button class="btn btn-primary btn-sm" type="button" onclick="updateAlias('+i+')">Save</button><button class="btn btn-danger btn-sm" type="button" onclick="deleteAlias('+i+')">Delete</button></div></td></tr>';}).join(''):'<tr><td colspan="3" class="empty-cell">No aliases configured</td></tr>';}
}
function aliasUnknown(i){var u=chatState.unknownModels[i],sel=document.getElementById('unknownTarget-'+i);if(!u||!sel||!sel.value)return;apiPost('/admin/models/mutate',{action:'alias_add',alias:u.id,target:sel.value}).then(function(r){if(!r.ok){flash(r.json.error&&r.json.error.message||'Alias failed','error');return;}flash('Alias created','success');loadModels();});}
function deleteAlias(i){var a=chatState.aliases[i];if(!a)return;openConfirm('Delete alias','Delete alias <strong>'+esc(a.alias)+'</strong>?','','Delete alias',function(){apiPost('/admin/models/mutate',{action:'alias_delete',alias:a.alias}).then(function(r){if(!r.ok){flash(r.json.error&&r.json.error.message||'Delete failed','error');return;}flash('Alias deleted','success');loadModels();});});}
function updateAlias(i){var a=chatState.aliases[i],sel=document.getElementById('aliasTarget-'+i);if(!a||!sel||!sel.value)return;apiPost('/admin/models/mutate',{action:'alias_update',alias:a.alias,target:sel.value}).then(function(r){if(!r.ok){flash(r.json.error&&r.json.error.message||'Alias update failed','error');return;}flash('Alias updated','success');loadModels();});}
function renderHelpers(){
  chatState.visionList=(chatState.helpers.vision_models||[]).slice();
  renderVisionCascade();
  renderAutoMode();
}
function renderAutoMode(){
  var a=chatState.helpers.auto_mode||{model:'',block_probability:0};
  var sel=document.getElementById('autoModeModel');
  var opts='<option value="">Off</option>'+chatState.models.filter(function(m){return !m.type||m.type==='openai';}).map(function(m){return '<option value="'+escAttr(m.name)+'">'+esc(m.name)+'</option>';}).join('');
  if(a.model&&!chatState.models.some(function(m){return m.name===a.model;}))opts+='<option value="'+escAttr(a.model)+'">'+esc(a.model)+'</option>';
  sel.innerHTML=opts;sel.value=a.model||'';
  document.getElementById('autoModeThreshold').value=a.block_probability?a.block_probability:'';
}
function saveAutoMode(){
  var t=parseFloat(document.getElementById('autoModeThreshold').value);
  apiPost('/admin/models/mutate',{action:'auto_mode',auto_model:document.getElementById('autoModeModel').value,block_probability:isNaN(t)?0:t}).then(function(r){
    if(!r.ok){flash(r.json.error&&r.json.error.message||'Save failed','error');return;}
    flash('Auto mode saved','success');loadModels();
  });
}
function renderVisionCascade(){
  var el=document.getElementById('visionCascade');
  var list=chatState.visionList;
  el.innerHTML=list.length?list.map(function(name,i){
    return '<div class="cascade-row"><span class="cascade-rank">'+(i+1)+'</span>'+
      '<span class="cascade-label">'+esc(name)+'</span>'+
      '<span class="cascade-actions">'+
      (i>0?'<button type="button" class="btn-link" title="Move up" onclick="moveVision('+i+',-1)">&uarr;</button>':'<span class="cascade-spacer"></span>')+
      (i<list.length-1?'<button type="button" class="btn-link" title="Move down" onclick="moveVision('+i+',1)">&darr;</button>':'<span class="cascade-spacer"></span>')+
      '<button type="button" class="btn-link" style="color:var(--red)" title="Remove" onclick="removeVision('+i+')">&times;</button>'+
      '</span></div>';
  }).join(''):'<p class="hint">No vision models configured — images sent to non-vision models will be rejected.</p>';
  var sel=document.getElementById('visionAddSelect');
  var avail=chatState.models.filter(function(m){return m.supports_vision&&list.indexOf(m.name)<0;});
  sel.innerHTML=avail.length?avail.map(function(m){return '<option value="'+escAttr(m.name)+'">'+esc(m.name)+'</option>';}).join(''):'<option value="">No vision-capable models available</option>';
}
function addVisionModel(){
  var sel=document.getElementById('visionAddSelect');
  if(!sel.value)return;
  chatState.visionList.push(sel.value);
  renderVisionCascade();
}
function moveVision(i,dir){
  var list=chatState.visionList,j=i+dir;
  if(j<0||j>=list.length)return;
  var t=list[i];list[i]=list[j];list[j]=t;
  renderVisionCascade();
}
function removeVision(i){ chatState.visionList.splice(i,1); renderVisionCascade(); }

function saveHelpers(){
  var body={
    audio:chatState.helpers.legacy_audio||'',
    vision_models:chatState.visionList,
  };
  apiPost('/admin/processors/mutate',body).then(function(r){
    if(!r.ok){flash(r.json.error&&r.json.error.message||'Save failed','error');return;}
    flash('Model helpers saved','success');loadModels();
  });
}
function openModel(name){var m=name?chatState.models.find(function(x){return x.name===name;}):null;chatState.editing=name;var f=document.getElementById('modelForm');f.reset();document.getElementById('modelTitle').textContent=m?name:'Add model';document.getElementById('backendRows').innerHTML='';if(m){['name','type','model','timeout','context_window','region','aws_access_key','responses_mode'].forEach(function(k){if(f.elements[k])f.elements[k].value=m[k]||'';});f.elements.supports_vision.checked=!!m.supports_vision;f.elements.code_link.checked=!!m.code_link;f.elements.rewrite_vision.value=m.rewrite_vision||(m.force_pipeline?'on':'');var d=m.defaults||{};['temperature','top_p','top_k','reasoning_effort'].forEach(function(k){f.elements[k].value=d[k]==null?'':d[k];});(m.backends||[]).forEach(addBackend);}else{f.elements.timeout.value=300;addBackend(null);}toggleBedrock();updateAutoHints();document.getElementById('modelErr').style.display='none';document.getElementById('modelModal').classList.add('open');}
function closeModel(){document.getElementById('modelModal').classList.remove('open');chatState.editing=null;}
function toggleBedrock(){document.getElementById('bedrockFields').style.display=document.getElementById('modelForm').elements.type.value==='bedrock'?'':'none';}
function updateAutoHints(){
  var visionAuto=document.getElementById('modelForm').elements.supports_vision.checked?'off':'on';
  ['rewriteVisionSelect','rewriteDocumentsSelect'].forEach(function(id){var sel=document.getElementById(id);if(sel&&sel.options[0])sel.options[0].textContent='Auto ('+visionAuto+')';});
  var searchSel=document.getElementById('rewriteSearchSelect');
  if(searchSel&&searchSel.options[0]){var searchAuto=(chatState.helpers&&chatState.helpers.has_search_key)?'on':'off';searchSel.options[0].textContent='Auto ('+searchAuto+')';}
}
function addBackend(b){b=b||{url:'',weight:1,max_inflight:0,disabled:false};var row=document.createElement('div');row.className='be-row-wrap';row.dataset.originalUrl=b.url||'';row.dataset.hasKey=b.has_api_key?'1':'0';row.dataset.clearKey='0';
var dotTitle=b.url?(b.disabled?'disabled':(b.online===true?('online \u00b7 '+(b.inflight||0)+' in-flight \u00b7 breaker '+(b.breaker||'closed')):(b.online===false?('offline: '+(b.error||'')):'no health data'))):'new backend';
var dotCls=b.disabled?'health-unknown':(b.online===true?'health-online':(b.online===false?'health-offline':'health-unknown'));
row.innerHTML='<div class="be-row">'+
 '<span><span class="health-dot '+dotCls+'" title="'+escAttr(dotTitle)+'"></span></span>'+
 '<input class="be-url" type="url" placeholder="http://server:8000/v1 or connect://site/service/v1" value="'+escAttr(b.url||'')+'">'+
 '<span class="be-keywrap"><input class="be-key" type="password" placeholder="'+escAttr(b.has_api_key?(b.api_key_mask+' \u2014 blank keeps'):'optional')+'"><button type="button" class="copy-icon be-clearkey" title="Clear the stored key on save">&times;</button></span>'+
 '<input class="be-weight" type="number" min="1" value="'+(b.weight||1)+'" title="Load-balancing weight">'+
 '<input class="be-max" type="number" min="0" value="'+(b.max_inflight||0)+'" title="Max concurrent (0 = unlimited)">'+
 '<span style="text-align:center"><input class="be-disabled" type="checkbox" '+(b.disabled?'checked':'')+' title="Temporarily disabled"></span>'+
 '<span class="be-actions"><button type="button" class="btn btn-secondary btn-sm be-test">Test</button><button type="button" class="btn btn-danger btn-sm btn-icon be-remove" title="Remove backend"><svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M2.5 4h11M5.5 4V2.5h5V4M4 4l.7 9.5h6.6L12 4M6.5 6.8v4.4M9.5 6.8v4.4"/></svg></button></span>'+
 '</div>'+
 '<div class="be-note"><span class="be-status mono"'+((b.online===false&&b.error)?' style="color:var(--red)"':'')+'>'+((b.online===false&&b.error)?esc(b.error):'')+'</span></div>';
if(!(b.online===false&&b.error))row.querySelector('.be-note').style.display='none';
row.querySelector('.be-remove').onclick=function(){row.remove();};
row.querySelector('.be-key').oninput=function(){row.dataset.clearKey='0';};
row.querySelector('.be-clearkey').onclick=function(){var i=row.querySelector('.be-key');i.value='';i.placeholder='will be cleared on save';row.dataset.clearKey='1';};
row.querySelector('.be-test').onclick=function(){testBackend(row);};
document.getElementById('backendRows').appendChild(row);}
function testBackend(row){var u=row.querySelector('.be-url').value.trim(),key=row.querySelector('.be-key').value,s=row.querySelector('.be-status');row.querySelector('.be-note').style.display='';if(!u){s.textContent='Enter a URL';return;}s.textContent='Testing\u2026';var body={url:u,model:chatState.editing||'',upstream_model:(document.getElementById('modelForm').elements.model.value.trim()||document.getElementById('modelForm').elements.name.value.trim()),type:document.getElementById('modelForm').elements.type.value};if(key)body.api_key=key;apiPost('/admin/backends/probe',body).then(function(r){
  if(!r.ok){s.innerHTML='';s.textContent=(r.json.error&&r.json.error.message)||'Failed';s.style.color='var(--danger)';return;}
  var txt=r.json.engine?'Connected \u00b7 '+r.json.engine:'Connected';
  if(r.json.context_window){
    txt+=' \u00b7 context '+Number(r.json.context_window).toLocaleString();
    s.innerHTML=esc(txt)+' <button type="button" class="btn-link" onclick="applyContextWindow('+r.json.context_window+')">Use</button>';
  } else {
    s.textContent=txt;
  }
  s.style.color='var(--success)';
});}
function applyContextWindow(n){ document.getElementById('modelForm').elements.context_window.value=n; flash('Context window set to '+Number(n).toLocaleString(),'success'); }
function detectContextWindow(btn){
  var rows=Array.from(document.querySelectorAll('#backendRows .be-row-wrap'));
  var row=rows.find(function(r){return r.querySelector('.be-url').value.trim();});
  var status=document.getElementById('ctxDetectStatus');
  status.style.display='';
  if(!row){status.textContent='Add a backend URL first.';status.style.color='var(--red)';return;}
  var u=row.querySelector('.be-url').value.trim(),key=row.querySelector('.be-key').value;
  var body={url:u,model:chatState.editing||'',upstream_model:(document.getElementById('modelForm').elements.model.value.trim()||document.getElementById('modelForm').elements.name.value.trim()),type:document.getElementById('modelForm').elements.type.value};
  if(key)body.api_key=key;
  btn.disabled=true;status.textContent='Checking '+u+'\u2026';status.style.color='var(--steel)';
  apiPost('/admin/backends/probe',body).then(function(r){
    btn.disabled=false;
    if(!r.ok){status.textContent=(r.json.error&&r.json.error.message)||'Probe failed';status.style.color='var(--red)';return;}
    if(r.json.context_window){
      document.getElementById('modelForm').elements.context_window.value=r.json.context_window;
      status.textContent='Detected '+Number(r.json.context_window).toLocaleString()+' from '+u;
      status.style.color='var(--ok)';
    } else {
      status.textContent='This backend\u2019s API does not publish a context window \u2014 enter it manually from the provider\u2019s docs.';
      status.style.color='var(--steel)';
    }
  });
}
function collectBackends(){return Array.from(document.querySelectorAll('#backendRows .be-row-wrap')).map(function(r){var b={url:r.querySelector('.be-url').value.trim(),weight:parseInt(r.querySelector('.be-weight').value,10)||1,max_inflight:parseInt(r.querySelector('.be-max').value,10)||0,disabled:r.querySelector('.be-disabled').checked};var k=r.querySelector('.be-key').value;if(k)b.api_key=k;else if(r.dataset.clearKey==='1')b.api_key='';return b;});}
function saveModel(e){e.preventDefault();var f=e.target,backends=collectBackends(),err=document.getElementById('modelErr');err.style.display='none';if(!f.elements.name.value.trim()||!backends.length||backends.some(function(b){return !b.url;})){err.textContent='Name and at least one backend URL are required.';err.style.display='block';return;}var body={name:f.elements.name.value.trim(),type:f.elements.type.value,model:f.elements.model.value.trim(),responses_mode:f.elements.responses_mode.value,timeout:parseInt(f.elements.timeout.value,10)||300,context_window:parseInt(f.elements.context_window.value,10)||0,supports_vision:f.elements.supports_vision.checked,code_link:f.elements.code_link.checked,rewrite_vision:f.elements.rewrite_vision.value,region:f.elements.region.value.trim(),aws_access_key:f.elements.aws_access_key.value.trim(),backends:backends};var d={};['temperature','top_p'].forEach(function(k){if(f.elements[k].value!=='')d[k]=parseFloat(f.elements[k].value);});if(f.elements.top_k.value!=='')d.top_k=parseInt(f.elements.top_k.value,10);if(f.elements.reasoning_effort.value)d.reasoning_effort=f.elements.reasoning_effort.value;body.defaults=d;if(f.elements.aws_secret_key.value)body.aws_secret_key=f.elements.aws_secret_key.value;if(f.elements.aws_session_token.value)body.aws_session_token=f.elements.aws_session_token.value;var payload=chatState.editing?{action:'update',original_name:chatState.editing,model:body}:{action:'add',model:body};var btn=document.getElementById('modelSave');btn.disabled=true;apiPost('/admin/models/mutate',payload).then(function(r){btn.disabled=false;if(!r.ok){err.textContent=r.json.error&&r.json.error.message||'Save failed';err.style.display='block';return;}closeModel();flash('Model saved','success');loadModels();});}
function deleteModel(name){openConfirm('Delete model','Delete model <strong>'+esc(name)+'</strong>? Its backends are removed from the running config immediately.','','Delete model',function(){apiPost('/admin/models/mutate',{action:'delete',name:name}).then(function(r){if(!r.ok){flash(r.json.error&&r.json.error.message||'Delete failed','error');return;}flash('Deleted','success');loadModels();});});}
function switchLLMTab(t){
var h=t==='helpers',a=t==='aliasing',m=!h&&!a;
document.getElementById('pane-models').style.display=m?'':'none';
document.getElementById('pane-helpers').style.display=h?'':'none';
document.getElementById('pane-aliasing').style.display=a?'':'none';
document.getElementById('subtabModels').classList.toggle('active',m);
document.getElementById('subtabHelpers').classList.toggle('active',h);
document.getElementById('subtabAliasing').classList.toggle('active',a);
if(history.replaceState)history.replaceState(null,'',h?'?tab=helpers':(a?'?tab=aliasing':location.pathname));
}
var initialLLMTab=new URLSearchParams(location.search).get('tab');
if(initialLLMTab==='helpers'||initialLLMTab==='aliasing')switchLLMTab(initialLLMTab);
document.addEventListener('keydown',function(e){if(e.key==='Escape')closeModel();});attachFilter('modelFilter','modelsBody');loadModels();
function loadConnect(){apiGet('/admin/connect/data').then(function(d){
  var card=document.getElementById('connectCard');if(!d.configured){card.style.display='none';return;}
  card.style.display='';document.getElementById('connectHub').textContent=d.url||'';
  var body=document.getElementById('connectBody');
  if(d.error){body.innerHTML='<tr><td colspan="5" class="empty-cell" style="color:var(--red)">'+esc(d.error)+'</td></tr>';return;}
  var dot=function(ok,t){return '<span class="health-dot '+(ok?'health-online':'health-offline')+'" title="'+t+'"></span>'+t;};
  var rows=[];(d.sites||[]).forEach(function(s){var svcs=s.services||[];
    if(!svcs.length){rows.push('<tr><td><strong>'+esc(s.name)+'</strong></td><td>'+dot(s.online,s.online?'yes':'no')+'</td><td colspan="3" class="empty-cell">no services</td></tr>');return;}
    svcs.forEach(function(v,i){rows.push('<tr><td>'+(i?'':'<strong>'+esc(s.name)+'</strong>')+'</td><td>'+(i?'':dot(s.online,s.online?'yes':'no'))+'</td><td class="mono">'+esc(v.name)+' <span class="hint">'+esc(v.kind||'')+'</span></td><td class="mono">'+esc((v.models||[]).join(', '))+'</td><td>'+dot(v.healthy,v.healthy?'yes':'no')+'</td></tr>');});});
  body.innerHTML=rows.length?rows.join(''):'<tr><td colspan="5" class="empty-cell">No sites in the catalog</td></tr>';
}).catch(function(){});}
loadConnect();
