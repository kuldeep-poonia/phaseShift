package api

// CSS, HTML structure, and JavaScript are split across three constants
// so that JavaScript template literals (backtick strings) don't conflict
// with Go raw string literals. The JS section uses regular string
// concatenation instead of template literals everywhere.

const dashCSS =`~<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>qphysics — Saturation Prediction</title>
<style>
:root{
  --bg:#0d1117;--surf:#161b22;--bdr:#30363d;
  --txt:#e6edf3;--muted:#8b949e;
  --safe:#3fb950;--warn:#d29922;--crit:#f85149;
  --accent:#58a6ff;--purple:#bc8cff;
}
*{box-sizing:border-box;margin:0;padding:0}
body{background:var(--bg);color:var(--txt);font-family:'SF Mono','Fira Code',Consolas,monospace;font-size:13px;line-height:1.6;min-height:100vh}
a{color:var(--accent);text-decoration:none}

/* header */
header{border-bottom:1px solid var(--bdr);padding:14px 24px;display:flex;align-items:center;justify-content:space-between}
.logo{font-size:16px;font-weight:700;color:var(--accent);letter-spacing:-.5px}
.logo span{color:var(--muted);font-weight:400}
.hdr-right{display:flex;gap:14px;align-items:center}
.tick{font-size:11px;color:var(--muted);background:var(--surf);border:1px solid var(--bdr);padding:3px 10px;border-radius:20px}
.dot{width:8px;height:8px;border-radius:50%;background:var(--safe);display:inline-block;animation:pulse 2s infinite}
.dot.warning{background:var(--warn)}.dot.collapse{background:var(--crit)}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.4}}

/* discovery bar */
.disc{font-size:11px;color:var(--muted);padding:7px 24px;background:rgba(0,0,0,.25);border-bottom:1px solid var(--bdr)}

/* layout */
main{padding:18px 24px;max-width:1400px;margin:0 auto}
.sec-title{font-size:10px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;color:var(--muted);margin-bottom:12px;padding-bottom:7px;border-bottom:1px solid var(--bdr)}

/* waiting overlay */
.waiting{background:var(--surf);border:1px solid var(--bdr);border-radius:8px;padding:48px;text-align:center;margin-bottom:18px}
.waiting h2{font-size:17px;color:var(--txt);margin-bottom:10px}
.waiting p{color:var(--muted);line-height:1.9;max-width:500px;margin:0 auto}
.waiting code{background:var(--bg);border:1px solid var(--bdr);border-radius:4px;padding:10px 16px;display:block;margin-top:14px;color:var(--accent);font-size:12px;text-align:left}

/* hero */
.hero{background:var(--surf);border:1px solid var(--bdr);border-radius:8px;padding:18px 22px;margin-bottom:18px}
.hero-grid{display:grid;grid-template-columns:190px 1fr 1fr 1fr 1fr;gap:18px;align-items:start}
.zone-badge{padding:16px;border-radius:8px;text-align:center;border:2px solid}
.zone-badge.safe{border-color:var(--safe);background:rgba(63,185,80,.07)}
.zone-badge.warning{border-color:var(--warn);background:rgba(210,153,34,.07)}
.zone-badge.collapse{border-color:var(--crit);background:rgba(248,81,73,.09)}
.zone-lbl{font-size:20px;font-weight:800;letter-spacing:2px;margin-bottom:3px}
.zone-badge.safe .zone-lbl{color:var(--safe)}
.zone-badge.warning .zone-lbl{color:var(--warn)}
.zone-badge.collapse .zone-lbl{color:var(--crit)}
.zone-sub{font-size:10px;color:var(--muted);letter-spacing:1px;text-transform:uppercase}
.s-label{font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:1px;margin-bottom:5px}
.s-val{font-size:21px;font-weight:700}
.s-val.ok{color:var(--safe)}.s-val.warn{color:var(--warn)}.s-val.danger{color:var(--crit)}.s-val.accent{color:var(--accent)}
.s-sub{font-size:11px;color:var(--muted);margin-top:3px}
.path-chain{display:flex;flex-wrap:wrap;gap:4px;align-items:center;margin-top:5px}
.pn{background:rgba(88,166,255,.1);border:1px solid rgba(88,166,255,.25);padding:2px 8px;border-radius:4px;font-size:11px;color:var(--accent)}
.pa{color:var(--muted);font-size:11px}

/* table */
.tbl-wrap{background:var(--surf);border:1px solid var(--bdr);border-radius:8px;margin-bottom:18px;overflow:hidden}
.tbl-wrap .sec-title{padding:12px 18px;margin-bottom:0;border-bottom:1px solid var(--bdr)}
table{width:100%;border-collapse:collapse}
thead th{font-size:10px;text-transform:uppercase;letter-spacing:1px;color:var(--muted);padding:9px 13px;text-align:left;border-bottom:1px solid var(--bdr);background:rgba(0,0,0,.18)}
tbody tr{border-bottom:1px solid rgba(48,54,61,.5);transition:background .15s}
tbody tr:hover{background:rgba(88,166,255,.035)}
tbody tr:last-child{border-bottom:none}
td{padding:9px 13px;vertical-align:middle}
.zpill{display:inline-block;padding:2px 8px;border-radius:11px;font-size:10px;font-weight:700;letter-spacing:.7px;text-transform:uppercase}
.zpill.safe{background:rgba(63,185,80,.13);color:var(--safe)}
.zpill.warning{background:rgba(210,153,34,.13);color:var(--warn)}
.zpill.collapse{background:rgba(248,81,73,.13);color:var(--crit)}
.bdg{display:inline-block;padding:1px 6px;border-radius:9px;font-size:10px;margin-left:3px}
.bdg-k{background:rgba(188,140,255,.12);color:var(--purple);border:1px solid rgba(188,140,255,.25)}
.bdg-b{background:rgba(248,81,73,.1);color:var(--crit);border:1px solid rgba(248,81,73,.25)}
.bar-w{min-width:72px}
.bar-t{background:rgba(48,54,61,.8);border-radius:2px;height:5px;overflow:hidden;margin-top:3px}
.bar-f{height:100%;border-radius:2px;transition:width .4s}
.bar-f.safe{background:var(--safe)}.bar-f.warning{background:var(--warn)}.bar-f.collapse{background:var(--crit)}
.ts{color:var(--muted)}.tw{color:var(--warn)}.td{color:var(--crit)}.tg{color:var(--safe)}.ta{color:var(--accent)}

/* graph */
.graph-wrap{background:var(--surf);border:1px solid var(--bdr);border-radius:8px;margin-bottom:18px;overflow:hidden}
.graph-wrap .sec-title{padding:12px 18px;margin-bottom:0;border-bottom:1px solid var(--bdr)}
#gc{width:100%;height:340px;display:block;cursor:grab}
#gc:active{cursor:grabbing}

/* simulator */
.sim-wrap{background:var(--surf);border:1px solid var(--bdr);border-radius:8px;overflow:hidden}
.sim-wrap .sec-title{padding:12px 18px;margin-bottom:0;border-bottom:1px solid var(--bdr)}
.sim-body{padding:18px}
.sim-grid{display:grid;grid-template-columns:1fr 1fr;gap:18px}
.sim-form{display:flex;flex-direction:column;gap:12px}
.fr{display:flex;flex-direction:column;gap:4px}
.fr label{font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:1px}
.fr select,.fr input[type=number]{background:var(--bg);border:1px solid var(--bdr);color:var(--txt);padding:7px 11px;border-radius:5px;font-family:inherit;font-size:13px;width:100%}
.fr select:focus,.fr input:focus{outline:2px solid var(--accent);border-color:transparent}
.rr{display:flex;align-items:center;gap:10px}
.rr input[type=range]{flex:1;accent-color:var(--accent)}
.rv{min-width:52px;text-align:right;color:var(--accent);font-weight:700;font-size:12px}
.cbr{display:flex;align-items:center;gap:9px}
.cbr input[type=checkbox]{width:15px;height:15px;accent-color:var(--accent)}
.cbr label{font-size:12px;cursor:pointer}
.sim-btn{background:var(--accent);color:#0d1117;border:none;padding:9px 22px;border-radius:5px;font-family:inherit;font-size:13px;font-weight:700;cursor:pointer;transition:opacity .15s;letter-spacing:.4px}
.sim-btn:hover{opacity:.85}.sim-btn:active{opacity:.7}.sim-btn:disabled{opacity:.4;cursor:not-allowed}
#sr{background:var(--bg);border:1px solid var(--bdr);border-radius:5px;padding:14px;min-height:240px;display:flex;flex-direction:column;gap:10px}
.sr-hdr{font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:1px}
.sr-grid{display:grid;grid-template-columns:1fr 1fr;gap:9px}
.sr-stat-lbl{font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:1px;margin-bottom:3px}
.sr-stat-val{font-size:17px;font-weight:700}
.sr-delta{font-size:11px;margin-top:2px}
.du{color:var(--crit)}.dd{color:var(--safe)}.df{color:var(--muted)}
.sr-svc{display:flex;align-items:center;justify-content:space-between;padding:4px 0;border-bottom:1px solid var(--bdr);font-size:12px}
.sr-svc:last-child{border-bottom:none}
.nd{flex:1;display:flex;align-items:center;justify-content:center;color:var(--muted);font-size:12px}
</style>
</head>
<body>
`

const dashHTML = `
<header>
  <div class="logo">qphysics <span>/ saturation prediction engine</span></div>
  <div class="hdr-right">
    <span class="tick">tick <span id="tc">—</span></span>
    <span class="tick" id="ca">—</span>
    <span class="dot" id="dot"></span>
  </div>
</header>
<div class="disc" id="disc">Initialising auto-discovery...</div>
<main>

<div id="waiting" class="waiting" style="display:none">
  <h2>Waiting for live telemetry</h2>
  <p>qphysics-agent is running but has not received any metrics yet.<br>
  Point it at a Prometheus endpoint, or send OTel traces to port 4318.</p>
  <code>export QPHYSICS_PROMETHEUS_URL=http://your-prometheus:9090<br>
./qphysics-agent</code>
  <p style="margin-top:14px;font-size:12px">
  OTel SDK: export to <strong>http://localhost:4318/v1/traces</strong><br>
  Prometheus: set <strong>QPHYSICS_PROMETHEUS_URL</strong> env var</p>
</div>

<div id="hero" class="hero">
  <div class="sec-title">Section 1 — System Prediction</div>
  <div class="hero-grid">
    <div class="zone-badge safe" id="zb">
      <div class="zone-lbl" id="zl">SAFE</div>
      <div class="zone-sub">System State</div>
    </div>
    <div>
      <div class="s-label">Saturation Horizon</div>
      <div class="s-val ok" id="sh">&#x221E;</div>
      <div class="s-sub" id="sh2">No saturation predicted</div>
    </div>
    <div>
      <div class="s-label">Collapse Probability</div>
      <div class="s-val" id="cp">0.0%</div>
      <div class="s-sub" id="ns">—</div>
    </div>
    <div>
      <div class="s-label">Bottleneck / Highest Risk</div>
      <div class="s-val accent" id="bn">—</div>
      <div class="s-sub" id="hr">—</div>
    </div>
    <div>
      <div class="s-label">Most Dangerous Path</div>
      <div class="path-chain" id="dp"><span class="ts">—</span></div>
      <div class="s-sub" id="fr">—</div>
    </div>
  </div>
</div>

<div class="tbl-wrap">
  <div class="sec-title">Section 2 — Service Physics</div>
  <table id="tbl">
    <thead>
      <tr>
        <th>Service</th><th>State</th><th>Utilisation</th><th>Eq &#961;</th>
        <th>Queue</th><th>Sat Horizon</th><th>Collapse Risk</th>
        <th>Burst Amp</th><th>Cascade</th><th>Upstream P</th>
        <th>Hazard Z</th><th>Control</th><th>Signal</th>
      </tr>
    </thead>
    <tbody id="tb"></tbody>
  </table>
</div>

<div class="graph-wrap">
  <div class="sec-title">Section 2b — Dependency Graph</div>
  <canvas id="gc"></canvas>
</div>

<div class="sim-wrap">
  <div class="sec-title">Section 3 — What-If Simulator</div>
  <div class="sim-body">
    <div class="sim-grid">
      <div class="sim-form">
        <div class="fr"><label>Target Service</label><select id="ss"></select></div>
        <div class="fr">
          <label>Latency Injection</label>
          <div class="rr">
            <input type="range" id="sl" min="0" max="2000" step="50" value="0">
            <span class="rv" id="slv">0 ms</span>
          </div>
        </div>
        <div class="fr">
          <label>Traffic Multiplier</label>
          <div class="rr">
            <input type="range" id="st" min="10" max="500" step="10" value="100">
            <span class="rv" id="stv">1.0x</span>
          </div>
        </div>
        <div class="cbr">
          <input type="checkbox" id="sf">
          <label for="sf">Simulate node failure (remove service completely)</label>
        </div>
        <button class="sim-btn" id="sb">&#9654; Run Simulation</button>
      </div>
      <div id="sr"><div class="nd">Run a simulation to see predictions</div></div>
    </div>
  </div>
</div>
</main>
`

const dashJS = `
<script>
var state=null,gpos={},gnode={},dragN=null,dragOff={x:0,y:0};

function fetchState(){
  fetch('/api/state').then(function(r){return r.json()}).then(function(s){
    state=s; render(s);
  }).catch(function(){
    document.getElementById('disc').textContent='Connection error — retrying...';
  });
}
fetchState();
setInterval(fetchState,2000);

function render(s){
  if(!s) return;
  document.getElementById('tc').textContent=s.tick_count;
  var at=new Date(s.computed_at);
  document.getElementById('ca').textContent=at.toLocaleTimeString();
  document.getElementById('disc').textContent=s.discovery_status||'Auto-discovery active';

  var dot=document.getElementById('dot');
  dot.className='dot '+(s.collapse_zone||'safe');

  var zone=s.collapse_zone||'safe';
  var zb=document.getElementById('zb');
  zb.className='zone-badge '+zone;
  document.getElementById('zl').textContent=zone.toUpperCase();

  // sat horizon
  var satStr=s.saturation_horizon_str||'\u221e';
  var shEl=document.getElementById('sh');
  shEl.textContent=satStr;
  shEl.className='s-val '+(satStr==='NOW'?'danger':satStr==='\u221e'?'ok':'warn');
  document.getElementById('sh2').textContent=
    satStr==='NOW'?'Already saturated':satStr==='\u221e'?'No saturation predicted':'Until first saturation';

  // collapse prob
  var cp=s.collapse_prob||0;
  var cpEl=document.getElementById('cp');
  cpEl.textContent=(cp*100).toFixed(1)+'%';
  cpEl.className='s-val '+(cp>0.7?'danger':cp>0.3?'warn':'ok');
  document.getElementById('ns').textContent=
    'Net sat risk: '+((s.network_sat_risk||0)*100).toFixed(1)+'% · '+
    (s.is_converging?'\u2198 converging':'\u2197 diverging');

  // bottleneck
  document.getElementById('bn').textContent=s.bottleneck_service||'—';
  document.getElementById('hr').textContent=s.highest_risk_service?'Highest risk: '+s.highest_risk_service:'—';

  // dangerous path
  var dp=document.getElementById('dp');
  var path=s.most_dangerous_path||[];
  if(path.length===0){
    dp.innerHTML='<span class="ts">—</span>';
  } else {
    var html='';
    for(var i=0;i<path.length;i++){
      if(i>0) html+='<span class="pa">\u2192</span>';
      html+='<span class="pn">'+esc(path[i])+'</span>';
    }
    dp.innerHTML=html;
  }
  document.getElementById('fr').textContent='Fragility: '+((s.system_fragility||0)*100).toFixed(1)+'%';

  renderTable(s.services||[]);
  renderGraph(s.services||[],s.edges||[]);

  // update simulator service list
  var sel=document.getElementById('ss');
  var prev=sel.value;
  sel.innerHTML='';
  (s.services||[]).forEach(function(svc){
    var o=document.createElement('option');
    o.value=svc.id; o.textContent=svc.id; sel.appendChild(o);
  });
  if(prev) sel.value=prev;

  // waiting overlay
  var hasData=s.has_live_data&&(s.services||[]).length>0;
  document.getElementById('waiting').style.display=hasData?'none':'block';
  document.getElementById('hero').style.display=hasData?'block':'none';
  document.getElementById('tbl').closest('.tbl-wrap').style.display=hasData?'block':'none';
}

function renderTable(svcs){
  var tb=document.getElementById('tb');
  tb.innerHTML='';
  if(!svcs.length) return;
  var sorted=svcs.slice().sort(function(a,b){return b.collapse_risk-a.collapse_risk});
  sorted.forEach(function(svc){
    var zone=svc.collapse_zone||'safe';
    var rho=svc.utilisation||0;
    var eqRho=svc.equilibrium_rho||0;
    var w=Math.min(rho*100,100).toFixed(1);
    var tr=document.createElement('tr');
    tr.innerHTML=
      '<td><span>'+esc(svc.id)+'</span>'+
      (svc.is_keystone?'<span class="bdg bdg-k">KEY</span>':'')+
      (svc.is_bottleneck?'<span class="bdg bdg-b">BOT</span>':'')+
      '</td>'+
      '<td><span class="zpill '+zone+'">'+zone.toUpperCase()+'</span></td>'+
      '<td class="bar-w"><div style="font-size:11px">'+pct(rho)+'</div>'+
      '<div class="bar-t"><div class="bar-f '+zone+'" style="width:'+w+'%"></div></div></td>'+
      '<td class="'+(eqRho>=1?'td':eqRho>=0.85?'tw':'tg')+'">'+pct(eqRho)+'</td>'+
      '<td>'+f1(svc.mean_queue_depth)+'</td>'+
      '<td class="'+satCls(svc.saturation_horizon_sec)+'">'+fmtH(svc.saturation_horizon_sec)+'</td>'+
      '<td class="bar-w"><div style="font-size:11px">'+pct(svc.collapse_risk)+'</div>'+
      '<div class="bar-t"><div class="bar-f '+zone+'" style="width:'+((svc.collapse_risk||0)*100).toFixed(1)+'%"></div></div></td>'+
      '<td class="'+(svc.burst_amplification>2?'tw':'')+'">'+f2(svc.burst_amplification)+'</td>'+
      '<td>'+f2(svc.cascade_score)+'</td>'+
      '<td>'+pct(svc.upstream_pressure)+'</td>'+
      '<td class="'+(svc.hazard>0.1?'tw':'ts')+'">'+f3(svc.hazard)+'</td>'+
      '<td>'+ctrlCell(svc.control)+'</td>'+
      '<td class="'+(svc.signal_quality==='sparse'?'tw':'')+'">'+(svc.signal_quality||'—')+
      (svc.spike_detected?' \u26a1':'')+
      (svc.change_point?' \u26a0':'')+
      '</td>';
    tb.appendChild(tr);
  });
}

// ── Graph ──────────────────────────────────────────────────────────
function renderGraph(svcs,edges){
  var canvas=document.getElementById('gc');
  var rect=canvas.getBoundingClientRect();
  var dpr=window.devicePixelRatio||1;
  canvas.width=rect.width*dpr; canvas.height=rect.height*dpr;
  var ctx=canvas.getContext('2d');
  ctx.scale(dpr,dpr);
  var W=rect.width,H=rect.height;
  ctx.clearRect(0,0,W,H);

  if(!svcs.length){
    ctx.fillStyle='#8b949e'; ctx.font='13px monospace';
    ctx.textAlign='center';
    ctx.fillText('No services discovered — connect Prometheus or send OTel traces',W/2,H/2);
    return;
  }

  gnode={};
  svcs.forEach(function(s){gnode[s.id]=s});

  var ids=Object.keys(gnode);
  ids.forEach(function(id,i){
    if(!gpos[id]){
      var angle=(2*Math.PI*i/ids.length)-Math.PI/2;
      var r=Math.min(W,H)*0.34;
      gpos[id]={x:W/2+r*Math.cos(angle),y:H/2+r*Math.sin(angle)};
    }
  });

  applyForces(ids,edges,W,H);

  // edges
  edges.forEach(function(e){
    var src=gpos[e.source],tgt=gpos[e.target];
    if(!src||!tgt) return;
    ctx.save();
    ctx.globalAlpha=Math.max(0.15,e.weight||0.3);
    ctx.strokeStyle='#58a6ff';
    ctx.lineWidth=Math.max(1,(e.weight||0.3)*2.5);
    ctx.beginPath(); ctx.moveTo(src.x,src.y); ctx.lineTo(tgt.x,tgt.y); ctx.stroke();
    var dx=tgt.x-src.x,dy=tgt.y-src.y,len=Math.sqrt(dx*dx+dy*dy);
    if(len>0){
      var ux=dx/len,uy=dy/len,nr=26;
      var ax=tgt.x-ux*nr,ay=tgt.y-uy*nr,p=5;
      ctx.beginPath(); ctx.moveTo(ax,ay);
      ctx.lineTo(ax-ux*9+uy*p,ay-uy*9-ux*p);
      ctx.lineTo(ax-ux*9-uy*p,ay-uy*9+ux*p);
      ctx.closePath(); ctx.fillStyle='#58a6ff'; ctx.fill();
    }
    ctx.restore();
    if(e.error_rate>0.01){
      var mx=(src.x+tgt.x)/2,my=(src.y+tgt.y)/2;
      ctx.save(); ctx.font='10px monospace'; ctx.fillStyle='#f85149';
      ctx.textAlign='center';
      ctx.fillText((e.error_rate*100).toFixed(1)+'%err',mx,my-5);
      ctx.restore();
    }
  });

  // nodes
  ids.forEach(function(id){
    var svc=gnode[id],pos=gpos[id];
    if(!pos) return;
    var zone=svc.collapse_zone||'safe';
    var col={safe:'#3fb950',warning:'#d29922',collapse:'#f85149'}[zone]||'#3fb950';
    var r=24+(svc.is_keystone?5:0)+(svc.is_bottleneck?4:0);
    if(svc.is_keystone||svc.is_bottleneck){
      ctx.save(); ctx.beginPath(); ctx.arc(pos.x,pos.y,r+5,0,Math.PI*2);
      ctx.strokeStyle=svc.is_bottleneck?'#f85149':'#bc8cff';
      ctx.lineWidth=1.5; ctx.setLineDash([4,3]); ctx.stroke(); ctx.restore();
    }
    ctx.save();
    ctx.beginPath(); ctx.arc(pos.x,pos.y,r,0,Math.PI*2);
    ctx.fillStyle=col+'22'; ctx.fill();
    ctx.strokeStyle=col; ctx.lineWidth=2; ctx.stroke();
    var rho=Math.min(svc.utilisation||0,1);
    if(rho>0){
      ctx.beginPath();
      ctx.arc(pos.x,pos.y,r+3,-Math.PI/2,-Math.PI/2+rho*2*Math.PI);
      ctx.strokeStyle=col; ctx.lineWidth=3; ctx.globalAlpha=0.7; ctx.stroke();
    }
    ctx.restore();
    ctx.save();
    var lbl=id.length>12?id.slice(0,10)+'..':id;
    ctx.font=Math.min(11,110/id.length)+'px monospace';
    ctx.fillStyle='#e6edf3'; ctx.textAlign='center'; ctx.textBaseline='middle';
    ctx.fillText(lbl,pos.x,pos.y);
    ctx.font='10px monospace'; ctx.fillStyle=col;
    ctx.fillText(pct(svc.utilisation||0),pos.x,pos.y+r+11);
    ctx.restore();
  });
}

function applyForces(ids,edges,W,H){
  var vel={};
  ids.forEach(function(id){vel[id]={x:0,y:0}});
  for(var i=0;i<ids.length;i++){
    for(var j=i+1;j<ids.length;j++){
      var a=gpos[ids[i]],b=gpos[ids[j]];
      var dx=a.x-b.x,dy=a.y-b.y;
      var dist=Math.max(Math.sqrt(dx*dx+dy*dy),1);
      var f=3000/(dist*dist),nx=dx/dist*f,ny=dy/dist*f;
      vel[ids[i]].x+=nx; vel[ids[i]].y+=ny;
      vel[ids[j]].x-=nx; vel[ids[j]].y-=ny;
    }
  }
  edges.forEach(function(e){
    var src=gpos[e.source],tgt=gpos[e.target];
    if(!src||!tgt) return;
    var dx=tgt.x-src.x,dy=tgt.y-src.y;
    var dist=Math.max(Math.sqrt(dx*dx+dy*dy),1);
    var f=0.05*(dist-150),nx=dx/dist*f,ny=dy/dist*f;
    if(vel[e.source]){vel[e.source].x+=nx;vel[e.source].y+=ny}
    if(vel[e.target]){vel[e.target].x-=nx;vel[e.target].y-=ny}
  });
  ids.forEach(function(id){
    if(dragN===id) return;
    var pos=gpos[id];
    vel[id].x+=(W/2-pos.x)*0.01; vel[id].y+=(H/2-pos.y)*0.01;
    pos.x+=vel[id].x*0.24; pos.y+=vel[id].y*0.24;
    pos.x=Math.max(38,Math.min(W-38,pos.x));
    pos.y=Math.max(38,Math.min(H-38,pos.y));
  });
}

var gc=document.getElementById('gc');
gc.addEventListener('mousedown',function(e){
  var r=gc.getBoundingClientRect(),mx=e.clientX-r.left,my=e.clientY-r.top;
  Object.keys(gpos).forEach(function(id){
    var p=gpos[id],dx=mx-p.x,dy=my-p.y;
    if(Math.sqrt(dx*dx+dy*dy)<30){dragN=id;dragOff={x:dx,y:dy}}
  });
});
gc.addEventListener('mousemove',function(e){
  if(!dragN) return;
  var r=gc.getBoundingClientRect();
  gpos[dragN]={x:e.clientX-r.left-dragOff.x,y:e.clientY-r.top-dragOff.y};
  if(state) renderGraph(state.services||[],state.edges||[]);
});
gc.addEventListener('mouseup',function(){dragN=null});
gc.addEventListener('mouseleave',function(){dragN=null});

// ── Simulator ──────────────────────────────────────────────────────
document.getElementById('sl').addEventListener('input',function(){
  document.getElementById('slv').textContent=this.value+' ms';
});
document.getElementById('st').addEventListener('input',function(){
  document.getElementById('stv').textContent=(this.value/100).toFixed(1)+'x';
});

document.getElementById('sb').addEventListener('click',function(){
  var btn=document.getElementById('sb');
  btn.disabled=true; btn.textContent='\u23f3 Simulating...';
  var req={
    target_service:document.getElementById('ss').value,
    latency_injection_ms:parseFloat(document.getElementById('sl').value),
    traffic_multiplier:parseFloat(document.getElementById('st').value)/100,
    node_failure:document.getElementById('sf').checked,
    simulation_steps:200
  };
  fetch('/api/simulate',{
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify(req)
  }).then(function(r){
    if(!r.ok) return r.text().then(function(t){throw new Error(t)});
    return r.json();
  }).then(function(r){
    renderSimResult(r);
  }).catch(function(e){
    document.getElementById('sr').innerHTML='<div class="nd" style="color:#f85149">'+esc(e.message)+'</div>';
  }).finally(function(){
    btn.disabled=false; btn.textContent='\u25b6 Run Simulation';
  });
});

function renderSimResult(r){
  if(!r) return;
  var el=document.getElementById('sr');
  var cpB=((r.baseline_collapse_prob||0)*100).toFixed(1);
  var cpA=((r.collapse_prob||0)*100).toFixed(1);
  var delta=r.collapse_prob-r.baseline_collapse_prob;
  var dcls=delta>0.05?'du':delta<-0.05?'dd':'df';
  var dsign=delta>0?'+':'';
  var satStr=r.saturation_horizon_sec===0?'NOW':r.saturation_horizon_sec<0?'\u221e':fmtSec(r.saturation_horizon_sec);
  var path=r.dangerous_path||[];
  var pathHtml='';
  for(var i=0;i<path.length;i++){
    if(i>0) pathHtml+='<span class="pa">\u2192</span>';
    pathHtml+='<span class="pn">'+esc(path[i])+'</span>';
  }
  var svcs=[];
  if(r.services){Object.values(r.services).forEach(function(s){svcs.push(s)})}
  svcs.sort(function(a,b){return b.predicted_rho-a.predicted_rho});
  svcs=svcs.slice(0,5);
  var svcsHtml='';
  svcs.forEach(function(s){
    svcsHtml+='<div class="sr-svc">'+
      '<span>'+esc(s.service_id)+'</span>'+
      '<span class="'+(s.predicted_rho>=1?'td':s.predicted_rho>=0.85?'tw':'tg')+'">'+'&#961;='+pct(s.predicted_rho)+'</span>'+
      '<span class="ts">'+f1(s.predicted_latency_ms)+'ms</span>'+
      '<span class="'+(s.collapse_risk>0.7?'td':s.collapse_risk>0.4?'tw':'')+'">'+pct(s.collapse_risk)+' risk</span>'+
      '</div>';
  });
  el.innerHTML=
    '<div class="sr-hdr">Scenario: '+esc(r.scenario_description||'')+'</div>'+
    '<div class="sr-grid">'+
      '<div><div class="sr-stat-lbl">Collapse Probability</div>'+
      '<div class="sr-stat-val '+(delta>0.1?'td':delta>0.05?'tw':'')+'">'+cpA+'%</div>'+
      '<div class="sr-delta '+dcls+'">'+dsign+(delta*100).toFixed(1)+'% vs baseline ('+cpB+'%)</div></div>'+
      '<div><div class="sr-stat-lbl">Saturation Horizon</div>'+
      '<div class="sr-stat-val '+(satStr==='NOW'?'td':satStr==='\u221e'?'tg':'tw')+'">'+satStr+'</div>'+
      '<div class="sr-delta df">Earliest across all services</div></div>'+
      '<div><div class="sr-stat-lbl">Peak Queue Depth</div>'+
      '<div class="sr-stat-val">'+f1(r.peak_queue_depth)+'</div>'+
      '<div class="sr-delta ts">Hazard Z: '+f3(r.hazard_accumulated)+'</div></div>'+
      '<div><div class="sr-stat-lbl">Network Mass (PDE)</div>'+
      '<div class="sr-stat-val">'+f3(r.network_mass)+'</div>'+
      '<div class="sr-delta ts">Fluid congestion density</div></div>'+
    '</div>'+
    '<div style="margin-top:8px"><div class="sr-stat-lbl" style="margin-bottom:5px">Dangerous Path</div>'+
    '<div class="path-chain">'+(pathHtml||'<span class="ts">—</span>')+'</div></div>'+
    '<div style="margin-top:8px"><div class="sr-stat-lbl" style="margin-bottom:5px">Top Services by Predicted &#961;</div>'+
    svcsHtml+'</div>';
}

// ── Utilities ──────────────────────────────────────────────────────
function esc(s){
  return String(s||'').replace(/[<>&"]/g,function(c){
    return{'<':'&lt;','>':'&gt;','&':'&amp;','"':'&quot;'}[c];
  });
}
function pct(v){return((v||0)*100).toFixed(1)+'%'}
function f1(v){return(v||0).toFixed(1)}
function f2(v){return(v||0).toFixed(2)}
function f3(v){return(v||0).toFixed(3)}
function fmtH(sec){
  if(sec===0) return'NOW';
  if(sec<0||sec===null||sec===undefined) return'\u221e';
  return fmtSec(sec);
}
function fmtSec(sec){
  if(sec<60) return Math.round(sec)+'s';
  if(sec<3600) return(sec/60).toFixed(1)+'m';
  return(sec/3600).toFixed(1)+'h';
}
function satCls(sec){
  if(sec===0) return'td';
  if(sec>0&&sec<60) return'td';
  if(sec>0&&sec<300) return'tw';
  return'';
}
</script>
</body>
</html>`
