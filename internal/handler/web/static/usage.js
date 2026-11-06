var MODEL_COLORS=["#2a78d6","#eb6834","#1baf7a","#eda100","#e87ba4","#008300","#4a3aa7","#e34948"];
var OTHER_COLOR="#9a9a97";
var chartMode="tokens";
var lastData=null;
(function(){loadData()})();
function setChartMode(mode){
	chartMode=mode;
	document.getElementById("toggleRequests").classList.toggle("active",mode==="requests");
	document.getElementById("toggleTokens").classList.toggle("active",mode==="tokens");
	document.getElementById("chartTitle").textContent=mode==="tokens"?"Daily Tokens":"Daily Requests";
	if(lastData)renderChart(lastData.daily,lastData.daily_models);
}
function loadData(){
	var days=document.getElementById("periodSelect").value;
	var status=document.getElementById("usageStatus");
	status.textContent="Loading…";
	fetch("/admin/usage/data?days="+days)
		.then(function(r){if(r.status===401){location.href="/admin/login";throw new Error("Sign in required");}if(!r.ok)throw new Error("Could not load usage");return r.json();})
		.then(function(d){renderData(d);status.textContent="";})
		.catch(function(e){status.textContent=e.message;});
}
function renderData(d){
	lastData=d;
	var sc=document.getElementById("summaryCards");
	sc.innerHTML=
		summaryCard("Total Requests",fmtNum(d.totals.requests))+
		summaryCard("Total Tokens",fmtNum(d.totals.total_tokens))+
		summaryCard("Active Users",d.totals.users)+
		summaryCard("Error Rate",d.totals.error_rate.toFixed(1)+"%");
	renderChart(d.daily,d.daily_models);
	renderTable("usersBody",d.users,function(u){
		return "<td>"+esc(u.name)+"</td><td><code>"+esc(u.key_hash)+"</code></td>"+
			"<td>"+fmtNum(u.requests)+"</td><td>"+fmtNum(u.total_tokens)+"</td>"+
			"<td>"+u.active_days+"</td><td>"+esc(u.last_seen)+"</td>";
	});
	renderTable("modelsBody",d.models,function(m){
		var errCell=m.errors>0
			?"<td style=\"color:var(--danger);font-weight:700\">"+fmtNum(m.errors)+"</td>"
			:"<td style=\"color:var(--faint)\">0</td>";
		return "<td>"+esc(m.model)+"</td><td>"+fmtNum(m.requests)+"</td>"+
			"<td>"+m.users+"</td><td>"+fmtNum(m.total_tokens)+"</td>"+
			"<td>"+Math.round(m.avg_latency_ms)+" ms</td>"+errCell;
	});
	// Backends table: shown only when pooled routing has produced rows.
	var bc=document.getElementById("backendsCard");
	if(d.backends&&d.backends.length){
		bc.style.display="";
		var perModel={};
		for(var i=0;i<d.backends.length;i++){
			perModel[d.backends[i].model]=(perModel[d.backends[i].model]||0)+d.backends[i].requests;
		}
		renderTable("backendsBody",d.backends,function(b){
			var share=perModel[b.model]>0?(100*b.requests/perModel[b.model]).toFixed(0)+"%":"—";
			return "<td>"+esc(b.model)+"</td><td><code>"+esc(b.backend)+"</code></td>"+
				"<td>"+fmtNum(b.requests)+"</td><td>"+share+"</td>"+
				"<td>"+fmtNum(b.total_tokens)+"</td>"+
				"<td>"+Math.round(b.avg_latency_ms)+" ms</td><td>"+fmtNum(b.errors)+"</td>";
		});
	}else{
		bc.style.display="none";
	}
}
function summaryCard(label,value){
	return "<div class=\"summary-card\"><div class=\"summary-value\">"+value+"</div><div class=\"summary-label\">"+label+"</div></div>";
}
function renderChart(rows,modelRows){
	var el=document.getElementById("dailyChart");
	if(!rows||rows.length===0){el.innerHTML="<p style=\"color:var(--muted);padding:20px 0\">No data for this period.</p>";return;}
	var useTokens=chartMode==="tokens";
	var valKey=useTokens?"total_tokens":"requests";
	var valLabel=useTokens?"tokens":"requests";
	// Series get palette slots in fixed order by total volume; past 8, fold into "Other".
	var totals={};
	if(modelRows){for(var i=0;i<modelRows.length;i++){var mr=modelRows[i];totals[mr.model]=(totals[mr.model]||0)+(mr[valKey]||0);}}
	var allModels=Object.keys(totals).sort(function(a,b){return totals[b]-totals[a];});
	var top=allModels.slice(0,8);
	var hasOther=allModels.length>8;
	function colorFor(m){var i=top.indexOf(m);return i>=0?MODEL_COLORS[i]:OTHER_COLOR;}
	var dateMap={};
	for(var i=0;i<rows.length;i++){dateMap[rows[i].date]={total:rows[i][valKey],models:{}};}
	if(modelRows){for(var i=0;i<modelRows.length;i++){
		var dm=modelRows[i];
		if(!dateMap[dm.date])continue;
		var key=top.indexOf(dm.model)>=0?dm.model:"Other";
		dateMap[dm.date].models[key]=(dateMap[dm.date].models[key]||0)+(dm[valKey]||0);
	}}
	var max=0;
	for(var i=0;i<rows.length;i++){if(rows[i][valKey]>max)max=rows[i][valKey];}
	var html="";
	if(allModels.length>1){
		html+="<div class=\"chart-legend\">";
		for(var i=0;i<top.length;i++){
			html+="<span class=\"legend-item\"><span class=\"legend-swatch\" style=\"background:"+MODEL_COLORS[i]+"\"></span>"+esc(top[i])+"</span>";
		}
		if(hasOther)html+="<span class=\"legend-item\"><span class=\"legend-swatch\" style=\"background:"+OTHER_COLOR+"\"></span>Other</span>";
		html+="</div>";
	}
	html+="<div class=\"bars\">";
	for(var i=0;i<rows.length;i++){
		var r=rows[i];
		var val=r[valKey];
		var pct=max>0?(val/max*100):0;
		var dateLabel=r.date.substring(5);
		var dm=dateMap[r.date];
		var inner="";
		if(allModels.length>1&&dm){
			var segOrder=top.slice();
			if(hasOther)segOrder.push("Other");
			// Stack in fixed series order so each model keeps its position day to day.
			for(var j=0;j<segOrder.length;j++){
				var name=segOrder[j];
				var v=dm.models[name];
				if(!v)continue;
				var segPct=max>0?(v/max*100):0;
				inner+="<div class=\"bar-segment\" style=\"height:"+segPct+"%;background:"+colorFor(name==="Other"?"__other__":name)+"\" title=\""+esc(name)+": "+fmtNum(v)+" "+valLabel+"\"></div>";
			}
		}else{
			inner="<div class=\"bar-segment\" style=\"height:"+pct+"%;background:"+MODEL_COLORS[0]+"\"></div>";
		}
		html+="<div class=\"bar-group\" title=\""+esc(r.date)+": "+fmtNum(r.requests)+" requests, "+fmtNum(r.total_tokens)+" tokens\">"+
			"<div class=\"bar-stack\">"+inner+"</div>"+
			"<div class=\"bar-label\">"+esc(dateLabel)+"</div></div>";
	}
	html+="</div>";
	el.innerHTML=html;
}
function renderTable(id,rows,cellFn){
	var tbody=document.getElementById(id);
	var html="";
	for(var i=0;i<rows.length;i++){html+="<tr>"+cellFn(rows[i])+"</tr>";}
	if(!rows.length)html="<tr><td colspan=\"99\" style=\"text-align:center;color:var(--muted);padding:16px\">No data</td></tr>";
	tbody.innerHTML=html;
}
function fmtNum(n){
	if(typeof n!=="number")return String(n);
	return n.toLocaleString();
}
function esc(s){var d=document.createElement("div");d.textContent=s;return d.innerHTML;}
function tableFilter(inputId,bodyId){
	var inp=document.getElementById(inputId);
	if(!inp)return;
	inp.addEventListener("input",function(){
		var q=inp.value.toLowerCase();
		var rows=document.getElementById(bodyId).rows;
		for(var i=0;i<rows.length;i++){
			rows[i].style.display=(!q||rows[i].textContent.toLowerCase().indexOf(q)>=0)?"":"none";
		}
	});
}
tableFilter("userTableFilter","usersBody");
tableFilter("backendTableFilter","backendsBody");
