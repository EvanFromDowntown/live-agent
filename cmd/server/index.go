package main

// indexHTML is the single-page UI, styled after OpenClaw's Control UI: a left
// sessions sidebar, a center chat that streams thinking + kind-aware tool
// output cards, and a right run rail (plan / files / status). Vanilla JS +
// EventSource; no build step. The JavaScript deliberately avoids backtick
// template literals so the whole page can live inside a Go raw string.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>live-agent</title>
<style>
  :root{
    --bg:#0c0e13; --side:#101319; --panel:#151922; --panel2:#1b202b; --line:#252b37;
    --fg:#e7e9f0; --muted:#8b93a7; --accent:#6aa8ff; --accent2:#9b8cff;
    --ok:#39d98a; --err:#ff6b6b; --warn:#ffcc66; --term:#0a0c11;
  }
  *{box-sizing:border-box}
  html,body{height:100%}
  body{margin:0;font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;background:var(--bg);color:var(--fg)}
  .app{display:grid;grid-template-columns:264px 1fr 320px;height:100vh;overflow:hidden}
  .app.railHidden{grid-template-columns:264px 1fr 0}
  /* sidebar */
  .side{background:var(--side);border-right:1px solid var(--line);display:flex;flex-direction:column;min-height:0}
  .brand{display:flex;align-items:center;gap:9px;padding:14px 16px;border-bottom:1px solid var(--line)}
  .brand .logo{width:22px;height:22px;border-radius:6px;background:linear-gradient(135deg,var(--accent),var(--accent2))}
  .brand b{font-size:15px}
  .brand .st{margin-left:auto;font-size:11px;color:var(--muted)}
  .newbtn{margin:12px 14px;padding:9px 12px;border-radius:10px;border:1px solid var(--line);background:var(--panel);
          color:var(--fg);cursor:pointer;font-weight:600;text-align:left}
  .newbtn:hover{border-color:var(--accent)}
  .seclbl{padding:6px 16px;font-size:11px;text-transform:uppercase;letter-spacing:.07em;color:var(--muted)}
  .sessions{flex:1;overflow:auto;padding:0 8px 12px}
  .srow{display:flex;gap:9px;align-items:flex-start;padding:9px 10px;border-radius:9px;cursor:pointer}
  .srow:hover{background:var(--panel)}
  .srow.active{background:var(--panel2)}
  .srow .dot{width:8px;height:8px;border-radius:50%;margin-top:5px;background:var(--muted);flex:none}
  .srow .dot.run{background:var(--warn);animation:pulse 1s infinite}
  .srow .dot.ok{background:var(--ok)} .srow .dot.err{background:var(--err)}
  .srow .t{min-width:0}
  .srow .t .ttl{font-size:13px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .srow .t .sub{font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  @keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}
  /* chat */
  .chat{display:flex;flex-direction:column;min-width:0;min-height:0}
  .chead{display:flex;align-items:center;gap:10px;padding:12px 18px;border-bottom:1px solid var(--line)}
  .chead .ttl{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .chead .badge{font-size:11px;padding:2px 9px;border-radius:999px;background:var(--panel2);color:var(--muted)}
  .chead .badge.ok{background:rgba(57,217,138,.15);color:var(--ok)}
  .chead .badge.err{background:rgba(255,107,107,.15);color:var(--err)}
  .chead .badge.run{background:rgba(255,204,102,.15);color:var(--warn)}
  .chead .railtgl{margin-left:auto;background:none;border:1px solid var(--line);color:var(--muted);border-radius:8px;cursor:pointer;padding:4px 9px}
  .thread{flex:1;overflow:auto;padding:20px;display:flex;flex-direction:column;gap:12px}
  .empty{margin:auto;color:var(--muted);text-align:center;max-width:420px}
  .empty h3{color:var(--fg);font-weight:600;margin:0 0 6px}
  .user{align-self:flex-end;max-width:76%;background:linear-gradient(135deg,var(--accent),var(--accent2));color:#04122e;
        padding:9px 13px;border-radius:14px 14px 4px 14px;font-weight:500;white-space:pre-wrap;word-break:break-word}
  .assist{align-self:flex-start;max-width:88%;display:flex;flex-direction:column;gap:10px;width:100%}
  .msg{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:10px 13px;white-space:pre-wrap;word-break:break-word}
  /* thinking + prose are always shown expanded — never hidden behind a toggle */
  .think{background:var(--panel2);border-left:3px solid var(--accent2);border-radius:8px;padding:8px 12px}
  .think .lbl{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.06em;display:block;margin-bottom:4px}
  .think pre{margin:0;white-space:pre-wrap;word-break:break-word;color:#c8cede;font-size:12.5px;font-style:italic}
  /* tool calls are collapsed by default: header shows kind + command, expand for output */
  details.tool{border:1px solid var(--line);border-radius:12px;overflow:hidden;background:var(--panel)}
  details.tool.err{border-color:var(--err)}
  details.tool>summary{display:flex;align-items:center;gap:9px;padding:8px 12px;background:var(--panel2);cursor:pointer;list-style:none;user-select:none}
  details.tool>summary::-webkit-details-marker{display:none}
  details.tool>summary::before{content:"\25B8";color:var(--muted);font-size:11px;flex:none}
  details.tool[open]>summary::before{content:"\25BE"}
  details.tool>summary .kind{font-size:11px;text-transform:uppercase;letter-spacing:.05em;padding:2px 8px;border-radius:6px;background:#2b3242;color:var(--accent)}
  details.tool.err>summary .kind{color:var(--err)}
  details.tool>summary .cmd{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;color:var(--fg);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  details.tool>summary .n{margin-left:auto;color:var(--muted);font-size:11px;font-variant-numeric:tabular-nums}
  details.tool pre{margin:0;padding:10px 12px;background:var(--term);white-space:pre-wrap;word-break:break-word;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;max-height:300px;overflow:auto}
  details.tool pre.code{color:#cbd3e6;border-bottom:1px solid var(--line)}
  details.tool .raw{padding:0 12px 10px}
  details.tool .raw summary{cursor:pointer;color:var(--muted);font-size:11.5px;padding:6px 0}
  .note{color:var(--muted);font-size:12.5px;padding:1px 2px}
  .note.ok{color:var(--ok)} .note.warn{color:var(--warn)}
  .result{border-radius:12px;padding:12px 14px}
  .result.ok{background:rgba(57,217,138,.1);border:1px solid var(--ok)}
  .result.warn{background:rgba(255,204,102,.1);border:1px solid var(--warn)}
  .result.err{background:rgba(255,107,107,.1);border:1px solid var(--err)}
  .result .rt{font-weight:600;margin-bottom:3px}
  .result .rs{white-space:pre-wrap;word-break:break-word}
  /* composer */
  .composer{border-top:1px solid var(--line);padding:12px 16px;display:flex;gap:10px;align-items:flex-end}
  .composer textarea{flex:1;resize:none;max-height:160px;min-height:44px;background:var(--panel);color:var(--fg);border:1px solid var(--line);border-radius:12px;padding:11px 13px;font:inherit}
  .composer button{height:44px;border:0;border-radius:12px;padding:0 18px;font-weight:600;cursor:pointer}
  .composer .send{background:var(--accent);color:#04122e}
  .composer .stop{background:var(--panel2);color:var(--fg);border:1px solid var(--line)}
  .composer button:disabled{opacity:.45;cursor:default}
  /* rail */
  .rail{background:var(--side);border-left:1px solid var(--line);overflow:auto;padding:14px}
  .app.railHidden .rail{display:none}
  .rail h2{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted);margin:14px 0 8px}
  .rail h2:first-child{margin-top:0}
  ol.plan{margin:0;padding-left:18px} ol.plan li{margin:4px 0}
  ol.plan li .stx{font-size:10.5px;padding:1px 6px;border-radius:999px;margin-left:6px}
  .stx.pending{background:#2a2f3a;color:var(--muted)} .stx.in_progress{background:rgba(106,168,255,.2);color:var(--accent)} .stx.done{background:rgba(57,217,138,.2);color:var(--ok)}
  ul.files{list-style:none;margin:0;padding:0} ul.files li{padding:3px 0}
  ul.files a{color:var(--accent);cursor:pointer;text-decoration:none} ul.files a:hover{text-decoration:underline}
  ul.files .sz{color:var(--muted);font-size:11px;margin-left:6px}
  .kv{display:flex;justify-content:space-between;font-size:12.5px;padding:3px 0;color:var(--muted)}
  .kv b{color:var(--fg);font-weight:500}
  .modal{position:fixed;inset:0;background:rgba(0,0,0,.6);display:none;align-items:center;justify-content:center;padding:30px;z-index:20}
  .modal.show{display:flex}
  .modal .box{background:var(--panel);border:1px solid var(--line);border-radius:12px;max-width:900px;width:100%;max-height:80vh;display:flex;flex-direction:column}
  .modal .mh{display:flex;align-items:center;padding:10px 14px;border-bottom:1px solid var(--line)}
  .modal .mh button{margin-left:auto;background:var(--panel2);color:var(--fg);border:1px solid var(--line);border-radius:8px;cursor:pointer;padding:4px 10px}
  .modal pre{margin:0;padding:14px;overflow:auto;white-space:pre-wrap;word-break:break-word}
  /* model bar under the composer */
  .combar{display:flex;align-items:center;gap:10px;padding:0 16px 12px}
  .combar .lbl{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.05em}
  .combar select{background:var(--panel);color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:6px 9px;font:inherit;max-width:300px}
  .mini{background:var(--panel2);color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:6px 10px;cursor:pointer;font-size:12px}
  .mini:hover{border-color:var(--accent)}
  .cfg-field{display:flex;flex-direction:column;gap:5px;margin-bottom:13px}
  .cfg-field label{font-size:12px;color:var(--muted)}
  .cfg-field input,.cfg-field textarea{background:var(--panel2);color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:8px 10px;font:inherit}
  .cfg-field textarea{min-height:96px;resize:vertical;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px}
  .cfg-hint{color:var(--muted);font-size:11.5px;margin-top:-2px}
  .cfg-msg{font-size:12px;margin-top:4px}
  .modal .box.cfg{max-width:560px}
  .modal .box .body{padding:16px;overflow:auto}
  .modal .mh .actions{margin-left:auto;display:flex;gap:8px}
  .modal .mh .actions .save{background:var(--accent);color:#04122e;border:0}
  @media (max-width:900px){ .app,.app.railHidden{grid-template-columns:1fr} .side,.rail{display:none} }
</style>
</head>
<body>
<div class="app" id="app">
  <!-- sidebar -->
  <aside class="side">
    <div class="brand"><span class="logo"></span><b>live-agent</b><span class="st" id="brandSt"></span></div>
    <button class="newbtn" id="newBtn">+  New run</button>
    <div class="seclbl">Sessions</div>
    <div class="sessions" id="sessions"></div>
  </aside>

  <!-- chat -->
  <main class="chat">
    <div class="chead">
      <span class="ttl" id="chatTitle">New session</span>
      <span class="badge" id="chatBadge" style="display:none"></span>
      <button class="railtgl" id="railTgl">Hide rail</button>
    </div>
    <div class="thread" id="thread">
      <div class="empty" id="emptyState">
        <h3>Start a conversation</h3>
        <div>Ask a question and the agent replies directly; give it a task and it works step by step. The session stays open — keep chatting to build on what it did.</div>
      </div>
    </div>
    <form class="composer" id="form">
      <textarea id="task" placeholder="Ask a question or describe a task.  (Enter to send, Shift+Enter for newline)"></textarea>
      <button type="submit" class="send" id="sendBtn">Run</button>
      <button type="button" class="stop" id="stopBtn" disabled>Stop</button>
    </form>
    <div class="combar">
      <span class="lbl">Model</span>
      <select id="modelSel"></select>
      <button type="button" class="mini" id="fetchBtn" title="Fetch models from the endpoint">Refresh</button>
      <button type="button" class="mini" id="cfgBtn">Configure</button>
    </div>
  </main>

  <!-- rail -->
  <aside class="rail" id="rail">
    <h2>Run</h2>
    <div id="railStatus"><div class="note">No active run.</div></div>
    <h2>Plan</h2>
    <div id="railPlan"><div class="note">No plan yet.</div></div>
    <h2>Produced files</h2>
    <div id="railFiles"><div class="note">Files appear when a run ends.</div></div>
  </aside>
</div>

<div id="modal" class="modal"><div class="box">
  <div class="mh"><b id="modalName"></b><button onclick="closeModal()">Close</button></div>
  <pre id="modalBody"></pre>
</div></div>

<div id="cfgModal" class="modal"><div class="box cfg">
  <div class="mh"><b>Model connection</b>
    <span class="actions">
      <button class="mini" id="cfgFetch" type="button">Fetch models</button>
      <button class="mini save" id="cfgSave" type="button">Save</button>
      <button class="mini" id="cfgClose" type="button">Close</button>
    </span>
  </div>
  <div class="body">
    <div class="cfg-field">
      <label>Base URL (OpenAI-compatible)</label>
      <input id="cfgBase" placeholder="https://host/v1" autocomplete="off">
    </div>
    <div class="cfg-field">
      <label>API key</label>
      <input id="cfgKey" type="password" placeholder="leave blank to keep current" autocomplete="off">
      <div class="cfg-hint" id="cfgKeyHint"></div>
    </div>
    <div class="cfg-field">
      <label>Default model</label>
      <input id="cfgModel" placeholder="glm-5.2" autocomplete="off">
    </div>
    <div class="cfg-field">
      <label>Model options (one per line) — shown in the picker under the composer</label>
      <textarea id="cfgModels" placeholder="glm-5.2&#10;gpt-4o"></textarea>
    </div>
    <div class="cfg-msg" id="cfgMsg"></div>
  </div>
</div></div>

<script>
var es=null, currentSession=null, running=false;
var $=function(id){return document.getElementById(id)};
var thread=$('thread'), sessionsEl=$('sessions'), dotEl=$('dot');

function esc(s){if(s==null)return '';return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')}
function clr(el){while(el.firstChild)el.removeChild(el.firstChild)}
function scroll(){thread.scrollTop=thread.scrollHeight}

/* ---------- sessions sidebar ---------- */
function dotClass(status){
  status=status||'';
  if(status==='running')return 'run';
  if(status==='success'||status==='finished')return 'ok';
  if(status.indexOf('stopped')===0||status==='finished_unverified'||status==='cancelled'||status==='interrupted')return 'err';
  return ''; // idle / other: neutral
}
function loadSessions(activeId){
  fetch('/episodes').then(function(r){return r.json()}).then(function(d){
    clr(sessionsEl);
    var eps=d.episodes||[];
    if(!eps.length){sessionsEl.innerHTML='<div class="note" style="padding:8px 10px">No runs yet.</div>';return}
    eps.forEach(function(e){
      var row=document.createElement('div');
      row.className='srow'+((e.id===activeId)?' active':'');
      var sub=(e.steps||0)+' steps · '+esc(e.status);
      row.innerHTML='<span class="dot '+dotClass(e.status)+'"></span>'
        +'<div class="t"><div class="ttl">'+esc(e.task)+'</div><div class="sub">'+sub+'</div></div>';
      row.onclick=function(){ if(!running) openEpisode(e.id) };
      sessionsEl.appendChild(row);
    });
  });
}

/* ---------- chat rendering ---------- */
function newAssistTurn(){
  var a=document.createElement('div'); a.className='assist'; thread.appendChild(a); return a;
}
var curTurn=null;
function turn(){ if(!curTurn){curTurn=newAssistTurn()} return curTurn }

function addUser(text){
  $('emptyState') && $('emptyState').remove();
  var u=document.createElement('div'); u.className='user'; u.textContent=text; thread.appendChild(u); scroll();
}
function addThink(ev){
  var d=document.createElement('div'); d.className='think';
  d.innerHTML='<span class="lbl">thinking</span><pre>'+esc(ev.text||'')+'</pre>';
  turn().appendChild(d); scroll();
}
function addMessage(ev){
  var m=document.createElement('div'); m.className='msg'; m.textContent=ev.text||'';
  turn().appendChild(m); scroll();
}
function addNote(ev,cls){
  var n=document.createElement('div'); n.className='note'+(cls?' '+cls:''); n.textContent=ev.text||'';
  turn().appendChild(n); scroll();
}
var KIND={run_shell:'shell',run_python:'python',read_file:'read',write_file:'write',http_fetch:'fetch',update_plan:'plan',finish:'finish'};
function toolPrimary(tool,args){
  if(!args)return '';
  if(tool==='run_shell')return args.command||'';
  if(tool==='read_file'||tool==='write_file')return args.path||'';
  if(tool==='http_fetch')return args.url||'';
  return '';
}
function addToolCard(step,tool,args,output,isErr){
  var card=document.createElement('details'); card.className='tool'+(isErr?' err':'');
  var kind=KIND[tool]||tool;
  var primary=toolPrimary(tool,args);
  var h='<summary><span class="kind">'+esc(kind)+'</span>';
  if(primary)h+='<span class="cmd">'+esc(primary)+'</span>';
  h+='<span class="n">#'+(step||'')+'</span></summary>';
  if(tool==='run_python'&&args&&args.code){ h+='<pre class="code">'+esc(args.code)+'</pre>'; }
  if(tool==='write_file'&&args&&args.content){ h+='<pre class="code">'+esc(String(args.content).slice(0,2000))+'</pre>'; }
  if(output){ h+='<pre>'+esc(output)+'</pre>'; }
  var argsStr=''; try{argsStr=JSON.stringify(args||{},null,2)}catch(e){}
  if(argsStr&&argsStr!=='{}'){ h+='<details class="raw"><summary>raw arguments</summary><pre class="code">'+esc(argsStr)+'</pre></details>'; }
  card.innerHTML=h;
  if(isErr) card.open=true; // surface failures without a click
  turn().appendChild(card); scroll();
}
function addResult(res){
  var cls=res.success?'ok':(res.finished?'warn':'err');
  var title=res.success?(res.verified?'Success (verified)':'Success')
        :(res.stop_reason==='finish_unverified'?'Finished but UNVERIFIED':'Stopped: '+(res.stop_reason||''));
  var r=document.createElement('div'); r.className='result '+cls;
  r.innerHTML='<div class="rt">'+esc(title)+'</div><div class="rs">'+esc(res.summary||'')+'</div>';
  turn().appendChild(r); scroll();
}

/* ---------- right rail ---------- */
function setBadge(txt,cls){var b=$('chatBadge'); if(!txt){b.style.display='none';return} b.style.display='';b.className='badge '+(cls||'');b.textContent=txt}
function renderPlan(plan){
  var el=$('railPlan');
  if(!plan||!plan.length){el.innerHTML='<div class="note">No plan yet.</div>';return}
  var h='<ol class="plan">';
  plan.forEach(function(p){h+='<li>'+esc(p.step)+'<span class="stx '+esc(p.status)+'">'+esc(p.status)+'</span></li>'});
  el.innerHTML=h+'</ol>';
}
function renderFiles(run,files){
  var el=$('railFiles');
  if(!files||!files.length){el.innerHTML='<div class="note">No files produced.</div>';return}
  var h='<ul class="files">';
  files.forEach(function(f){
    if(f.dir){h+='<li>'+esc(f.name)+'/</li>';return}
    h+='<li><a onclick="viewFile(\''+esc(run)+'\',\''+esc(f.name)+'\')">'+esc(f.name)+'</a><span class="sz">'+f.size+' B</span></li>';
  });
  el.innerHTML=h+'</ul>';
}
function renderRailStatus(o){
  var el=$('railStatus');
  el.innerHTML='<div class="kv"><span>status</span><b>'+esc(o.status||'-')+'</b></div>'
    +'<div class="kv"><span>steps</span><b>'+(o.steps||0)+'</b></div>'
    +(o.verified!=null?'<div class="kv"><span>verified</span><b>'+(o.verified?'yes':'no')+'</b></div>':'')
    +(o.episode?'<div class="kv"><span>episode</span><b>'+esc(o.episode)+'</b></div>':'');
}

/* ---------- file modal ---------- */
function viewFile(run,name){
  fetch('/file?run='+encodeURIComponent(run)+'&name='+encodeURIComponent(name)).then(function(r){return r.text()}).then(function(t){
    $('modalName').textContent=name; $('modalBody').textContent=t; $('modal').className='modal show';
  });
}
function closeModal(){$('modal').className='modal'}

/* ---------- session lifecycle ---------- */
function emptyHTML(){ return '<div class="empty" id="emptyState"><h3>Start a conversation</h3>'
  +'<div>Ask a question and the agent replies directly; give it a task and it works step by step. '
  +'The session stays open — keep chatting to build on what it did.</div></div>'; }
function newRun(){
  if(running) return;
  stopRun(); currentSession=null; curTurn=null;
  thread.innerHTML=emptyHTML();
  $('chatTitle').textContent='New session'; setBadge('');
  renderPlan(null); renderFiles(null); $('railStatus').innerHTML='<div class="note">No active run.</div>';
  $('task').value=''; $('task').focus(); loadSessions(null);
}
function setRunning(on){
  running=on; $('sendBtn').disabled=on; $('stopBtn').disabled=!on; $('task').disabled=on;
  dotEl && (dotEl.className='dot'+(on?' run':''));
  if(!on) $('task').focus();
}
function stopRun(){ if(es){es.close();es=null} setRunning(false); curTurn=null; }

function sendTask(task){
  var es0=$('emptyState'); if(es0) es0.remove();
  addUser(task);
  curTurn=newAssistTurn();
  setRunning(true); setBadge('running','run');
  if(!currentSession){ $('chatTitle').textContent=task; renderRailStatus({status:'running',steps:0}); }
  var model=$('modelSel').value||'';
  var url='/stream?task='+encodeURIComponent(task)
    +(currentSession?('&session='+encodeURIComponent(currentSession)):'')
    +(model?('&model='+encodeURIComponent(model)):'');
  es=new EventSource(url);
  es.onmessage=function(m){
    var ev; try{ev=JSON.parse(m.data)}catch(_){return}
    switch(ev.type){
      case 'start':
        currentSession=ev.episode_id;
        if(!$('chatTitle').textContent||$('chatTitle').textContent==='New session') $('chatTitle').textContent=task;
        renderRailStatus({status:'running',steps:0,episode:ev.episode_id});
        $('brandSt').textContent='os '+(ev.os||'?');
        loadSessions(currentSession);
        break;
      case 'think': addThink(ev); break;
      case 'message': addMessage(ev); break;
      case 'step':
        if(ev.tool==='update_plan')break;
        addToolCard(ev.step,ev.tool,ev.args,ev.output,ev.is_error);
        renderRailStatus({status:'running',steps:ev.step,episode:currentSession});
        break;
      case 'plan': renderPlan(ev.plan); break;
      case 'info': addNote(ev); break;
      case 'warn': addNote(ev,'warn'); break;
      case 'verify': addNote(ev, ev.is_error?'warn':'ok'); break;
      case 'busy': addNote(ev,'warn'); setRunning(false); break;
      case 'finish': case 'stop': break;
      case 'result':
        if(!ev.reply) addResult(ev);
        setBadge(ev.reply?'answered':(ev.success?(ev.verified?'verified':'success'):(ev.stop_reason||'stopped')),
                 ev.reply?'':(ev.success?'ok':'err'));
        renderRailStatus({status:ev.stop_reason||'done',steps:ev.steps,verified:ev.reply?null:ev.verified,episode:ev.session||currentSession});
        renderFiles(ev.run,ev.files);
        loadSessions(ev.session||currentSession);
        break;
      case 'done': stopRun(); break;
    }
  };
  es.onerror=function(){ addNote({text:'connection closed.'},'warn'); stopRun(); loadSessions(currentSession); };
}

/* ---------- open a session (replay history; keep chatting to continue) ---------- */
function openEpisode(id){
  if(running) return;
  stopRun(); currentSession=id; curTurn=null;
  clr(thread);
  loadSessions(id);
  fetch('/episode?id='+encodeURIComponent(id)).then(function(r){return r.json()}).then(function(d){
    var ep=d.episode||{}; var events=d.events||[];
    $('chatTitle').textContent=ep.task||id;
    var haveUser=false;
    events.forEach(function(e){
      var args={}; try{args=JSON.parse(e.args||'{}')}catch(_){args={}}
      if(e.tool==='(user)'){ addUser(e.result||''); curTurn=newAssistTurn(); haveUser=true; return; }
      if(e.tool==='(assistant)'){ addMessage({text:e.result||''}); return; }
      if(e.tool==='reply'){ addMessage({text:(args&&args.text)||e.result||''}); return; }
      if(e.tool==='update_plan'||e.tool==='finish'){ return; }
      if(e.tool && e.tool.charAt(0)==='('){ return; }
      addToolCard(e.step,e.tool,args,e.result,e.is_error);
    });
    if(!haveUser){ addUser(ep.task||''); curTurn=newAssistTurn(); }
    setBadge(ep.status, ep.status==='success'?'ok':'');
    renderRailStatus({status:ep.status,steps:ep.steps,episode:id});
    renderFiles(id, null);
    addNote({text:'Session loaded. Send a message to continue it.'},'ok');
    curTurn=null; scroll();
  });
}

/* ---------- model picker + settings ---------- */
function fillModelSelect(models, selected){
  var sel=$('modelSel'); clr(sel);
  models=models||[];
  var want=selected||localStorage.getItem('liveagent_model')||(models[0]||'');
  if(want && models.indexOf(want)<0) models=[want].concat(models);
  models.forEach(function(m){
    var o=document.createElement('option'); o.value=m; o.textContent=m;
    if(m===want) o.selected=true; sel.appendChild(o);
  });
  if(!models.length){ var o=document.createElement('option'); o.textContent='(none)'; sel.appendChild(o); }
}
function loadModels(){
  fetch('/models').then(function(r){return r.json()}).then(function(d){
    fillModelSelect(d.models, d.model);
  }).catch(function(){});
}
$('modelSel') && $('modelSel').addEventListener('change',function(){
  localStorage.setItem('liveagent_model', this.value);
});
$('fetchBtn').addEventListener('click',function(){
  this.textContent='...';
  var b=this;
  fetch('/models?fetch=1').then(function(r){return r.json()}).then(function(d){
    fillModelSelect(d.models, $('modelSel').value); b.textContent='Refresh';
    if(d.error) alert('Fetch failed: '+d.error);
  }).catch(function(){ b.textContent='Refresh'; });
});

function openCfg(){
  fetch('/settings').then(function(r){return r.json()}).then(function(d){
    $('cfgBase').value=d.base_url||'';
    $('cfgModel').value=d.model||'';
    $('cfgModels').value=(d.models||[]).join('\n');
    $('cfgKey').value='';
    $('cfgKeyHint').textContent=d.has_key?'A key is currently configured; leave blank to keep it.':'No key set (uses LLM_API_KEY env if present).';
    $('cfgMsg').textContent='';
    $('cfgModal').className='modal show';
  });
}
function closeCfg(){ $('cfgModal').className='modal'; }
function saveCfg(){
  var body={
    base_url: $('cfgBase').value.trim(),
    api_key: $('cfgKey').value,
    model: $('cfgModel').value.trim(),
    models: $('cfgModels').value.split('\n').map(function(s){return s.trim()}).filter(Boolean)
  };
  $('cfgMsg').textContent='Saving...';
  fetch('/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
    .then(function(r){return r.json()}).then(function(d){
      $('cfgMsg').textContent='Saved.';
      fillModelSelect(d.models, d.model); localStorage.setItem('liveagent_model', d.model||'');
      setTimeout(closeCfg,500);
    }).catch(function(e){ $('cfgMsg').textContent='Save failed.'; });
}
$('cfgBtn').addEventListener('click',openCfg);
$('cfgClose').addEventListener('click',closeCfg);
$('cfgSave').addEventListener('click',saveCfg);
$('cfgFetch').addEventListener('click',function(){
  $('cfgMsg').textContent='Fetching...';
  fetch('/models?fetch=1').then(function(r){return r.json()}).then(function(d){
    if(d.error){ $('cfgMsg').textContent='Fetch failed: '+d.error; return; }
    $('cfgModels').value=(d.models||[]).join('\n');
    $('cfgMsg').textContent='Fetched '+((d.fetched||[]).length)+' models.';
  });
});

/* ---------- wire up ---------- */
$('form').addEventListener('submit',function(e){
  e.preventDefault();
  var t=$('task').value.trim(); if(!t||running)return;
  $('task').value='';
  sendTask(t);
});
$('task').addEventListener('keydown',function(e){
  if(e.key==='Enter'&&!e.shiftKey){ e.preventDefault(); $('form').requestSubmit(); }
});
$('stopBtn').addEventListener('click',stopRun);
$('newBtn').addEventListener('click',newRun);
$('railTgl').addEventListener('click',function(){
  var app=$('app'); var hidden=app.classList.toggle('railHidden');
  this.textContent=hidden?'Show rail':'Hide rail';
});
loadSessions(null);
loadModels();
</script>
</body>
</html>`
