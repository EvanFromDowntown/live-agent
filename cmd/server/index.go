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
<title>live-agent // neural interface</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Orbitron:wght@500;700;900&family=Rajdhani:wght@400;500;600;700&family=Share+Tech+Mono&display=swap" rel="stylesheet">
<style>
  :root{
    --bg:#06070f; --side:#080a15; --panel:#0d1022; --panel2:#141834; --line:#26315c;
    --fg:#d6e6ff; --muted:#7a85bd; --accent:#05d9e8; --accent2:#ff2a6d; --purple:#b967ff;
    --ok:#00ff9f; --err:#ff2a6d; --warn:#fcee0a; --term:#04050e;
    --glow:0 0 10px rgba(5,217,232,.55); --glowp:0 0 10px rgba(255,42,109,.5);
    --mono:"Share Tech Mono",ui-monospace,SFMono-Regular,Menlo,monospace;
    --disp:"Orbitron",-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
    --ui:"Rajdhani",-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
  }
  *{box-sizing:border-box}
  html,body{height:100%}
  body{margin:0;font:15px/1.5 var(--ui);background:var(--bg);color:var(--fg);
    background-image:
      radial-gradient(1200px 620px at 82% -12%, rgba(255,42,109,.12), transparent 60%),
      radial-gradient(1000px 720px at -12% 112%, rgba(5,217,232,.13), transparent 55%),
      linear-gradient(rgba(5,217,232,.04) 1px, transparent 1px),
      linear-gradient(90deg, rgba(5,217,232,.04) 1px, transparent 1px);
    background-size:auto,auto,44px 44px,44px 44px;background-attachment:fixed}
  /* CRT scanlines + faint flicker */
  body::after{content:"";position:fixed;inset:0;pointer-events:none;z-index:9998;
    background:repeating-linear-gradient(to bottom,rgba(0,0,0,0) 0,rgba(0,0,0,0) 2px,rgba(0,0,0,.10) 3px,rgba(0,0,0,0) 4px);
    mix-blend-mode:overlay;opacity:.45;animation:flicker 4.5s infinite}
  @keyframes flicker{0%,100%{opacity:.45}48%{opacity:.45}50%{opacity:.32}52%{opacity:.45}}
  ::selection{background:rgba(5,217,232,.35);color:#fff}
  ::-webkit-scrollbar{width:9px;height:9px}
  ::-webkit-scrollbar-thumb{background:rgba(5,217,232,.28);border-radius:9px}
  ::-webkit-scrollbar-thumb:hover{background:rgba(5,217,232,.5)}
  .app{display:grid;grid-template-columns:264px 1fr 320px;height:100vh;overflow:hidden;position:relative;z-index:1}
  .app.railHidden{grid-template-columns:264px 1fr 0}
  /* sidebar */
  .side{background:linear-gradient(180deg,rgba(13,16,34,.7),rgba(8,10,21,.7));border-right:1px solid var(--line);display:flex;flex-direction:column;min-height:0;backdrop-filter:blur(4px)}
  .brand{display:flex;align-items:center;gap:10px;padding:15px 12px;border-bottom:1px solid var(--line)}
  .brand .logo{width:24px;height:24px;border-radius:5px;background:linear-gradient(135deg,var(--accent),var(--accent2));
    box-shadow:0 0 14px rgba(5,217,232,.7),0 0 22px rgba(255,42,109,.4);transform:rotate(45deg);animation:spin 8s linear infinite}
  @keyframes spin{to{transform:rotate(405deg)}}
  .brand b{font-family:var(--disp);font-weight:900;font-size:15px;letter-spacing:.14em;text-transform:uppercase;
    background:linear-gradient(90deg,var(--accent),var(--purple),var(--accent2));-webkit-background-clip:text;background-clip:text;color:transparent}
  .brand .st{margin-left:auto;font-family:var(--mono);font-size:10.5px;color:var(--accent);text-shadow:var(--glow);letter-spacing:.05em}
  .newbtn{display:block;width:calc(100% - 24px);margin:12px;padding:10px 12px;border-radius:9px;border:1px solid var(--line);background:rgba(5,217,232,.05);
          color:var(--fg);cursor:pointer;font-weight:600;text-align:left;font-family:var(--disp);letter-spacing:.08em;text-transform:uppercase;font-size:12px;transition:.15s}
  .newbtn:hover{border-color:var(--accent);box-shadow:var(--glow),inset 0 0 12px rgba(5,217,232,.12);color:var(--accent)}
  .seclbl{padding:8px 24px;font-family:var(--mono);font-size:10.5px;text-transform:uppercase;letter-spacing:.14em;color:var(--muted)}
  .sessions{flex:1;overflow:auto;padding:0 12px 12px}
  .srow{display:flex;gap:10px;align-items:flex-start;padding:9px 12px;border-radius:9px;cursor:pointer;transition:.12s}
  .srow:hover{background:rgba(5,217,232,.06)}
  .srow.active{background:rgba(5,217,232,.09);box-shadow:inset 2px 0 0 var(--accent),inset 0 0 16px rgba(5,217,232,.08)}
  .srow .dot{width:8px;height:8px;border-radius:50%;margin-top:6px;background:var(--muted);flex:none}
  .srow .dot.run{background:var(--warn);box-shadow:0 0 8px var(--warn);animation:pulse 1s infinite}
  .srow .dot.ok{background:var(--ok);box-shadow:0 0 8px var(--ok)} .srow .dot.err{background:var(--err);box-shadow:0 0 8px var(--err)}
  .srow .t{min-width:0}
  .srow .t .ttl{font-size:13.5px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .srow .t .sub{font-family:var(--mono);font-size:10.5px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  @keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}
  /* chat */
  .chat{display:flex;flex-direction:column;min-width:0;min-height:0}
  .chead{display:flex;align-items:center;gap:10px;padding:14px 22px;border-bottom:1px solid var(--line);
    background:linear-gradient(90deg,rgba(5,217,232,.06),transparent)}
  .chead .ttl{font-family:var(--disp);font-weight:700;letter-spacing:.03em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .chead .badge{font-family:var(--mono);font-size:10.5px;padding:3px 10px;border-radius:4px;background:var(--panel2);color:var(--muted);
    text-transform:uppercase;letter-spacing:.08em;border:1px solid var(--line)}
  .chead .badge.ok{background:rgba(0,255,159,.12);color:var(--ok);border-color:rgba(0,255,159,.4);box-shadow:0 0 10px rgba(0,255,159,.25)}
  .chead .badge.err{background:rgba(255,42,109,.12);color:var(--err);border-color:rgba(255,42,109,.4);box-shadow:0 0 10px rgba(255,42,109,.25)}
  .chead .badge.run{background:rgba(252,238,10,.12);color:var(--warn);border-color:rgba(252,238,10,.4);box-shadow:0 0 10px rgba(252,238,10,.2)}
  .chead .railtgl{margin-left:auto;background:none;border:1px solid var(--line);color:var(--muted);border-radius:6px;cursor:pointer;padding:5px 10px;font-family:var(--mono);font-size:11px;transition:.12s}
  .chead .railtgl:hover{border-color:var(--accent);color:var(--accent);box-shadow:var(--glow)}
  .thread{flex:1;overflow:auto;padding:22px;display:flex;flex-direction:column;gap:13px}
  .empty{margin:auto;color:var(--muted);text-align:center;max-width:440px}
  .empty h3{font-family:var(--disp);font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:var(--accent);text-shadow:var(--glow);margin:0 0 8px}
  .user{align-self:flex-end;max-width:76%;background:linear-gradient(135deg,var(--accent),var(--accent2));color:#050510;
        padding:9px 14px;border-radius:12px 12px 3px 12px;font-weight:600;white-space:pre-wrap;word-break:break-word;
        box-shadow:0 0 18px rgba(255,42,109,.35),0 0 12px rgba(5,217,232,.25)}
  .assist{align-self:flex-start;max-width:88%;display:flex;flex-direction:column;gap:10px;width:100%}
  .msg{background:rgba(13,16,34,.7);border:1px solid var(--line);border-left:2px solid var(--accent);border-radius:4px 12px 12px 4px;padding:10px 14px;white-space:pre-wrap;word-break:break-word}
  /* markdown-rendered prose: block layout instead of raw pre-wrap */
  .msg.md{white-space:normal}
  .msg.md>*:first-child{margin-top:0} .msg.md>*:last-child{margin-bottom:0}
  .msg.md p{margin:8px 0;word-break:break-word}
  .msg.md h1,.msg.md h2,.msg.md h3,.msg.md h4,.msg.md h5,.msg.md h6{font-family:var(--disp);color:#eaf3ff;margin:12px 0 6px;line-height:1.25}
  .msg.md .mh1{font-size:19px} .msg.md .mh2{font-size:17px} .msg.md .mh3{font-size:15px} .msg.md .mh4,.msg.md .mh5,.msg.md .mh6{font-size:13.5px}
  .msg.md ul,.msg.md ol{margin:6px 0;padding-left:22px} .msg.md li{margin:3px 0}
  .msg.md blockquote{margin:8px 0;padding:2px 12px;border-left:3px solid var(--purple);color:#c3b8e6;background:rgba(185,103,255,.06)}
  .msg.md a{color:var(--accent);text-decoration:none} .msg.md a:hover{text-shadow:var(--glow)}
  .msg.md code{font-family:var(--mono);font-size:12.5px;background:rgba(5,217,232,.1);border:1px solid rgba(5,217,232,.22);border-radius:4px;padding:1px 5px;color:#9fe8ee}
  .msg.md .codeblock{position:relative;margin:10px 0;border:1px solid var(--line);border-radius:8px;overflow:hidden;background:var(--term)}
  .msg.md .codeblock pre{margin:0;padding:12px 14px;overflow:auto;max-height:360px}
  .msg.md .codeblock code{display:block;background:none;border:0;padding:0;color:#cbd3e6;white-space:pre;font-size:12.5px}
  .msg.md .codeblock .copy{position:absolute;top:6px;right:6px;font-family:var(--mono);font-size:10px;text-transform:uppercase;letter-spacing:.08em;
    background:rgba(20,24,52,.85);color:var(--muted);border:1px solid var(--line);border-radius:5px;padding:3px 8px;cursor:pointer;opacity:0;transition:.12s}
  .msg.md .codeblock:hover .copy{opacity:1} .msg.md .codeblock .copy:hover{border-color:var(--accent);color:var(--accent)}
  /* live streaming stdout while a shell/python command runs */
  details.tool.live>summary .cmd{color:var(--warn);animation:pulse 1.2s ease-in-out infinite}
  @keyframes pulse{0%,100%{opacity:.55}50%{opacity:1}}
  pre.liveout{margin:0;padding:10px 12px;background:var(--term);white-space:pre-wrap;word-break:break-word;font-family:var(--mono);font-size:12.5px;max-height:280px;overflow:auto;color:#9fe8ee}
  /* thinking + prose are always shown expanded — never hidden behind a toggle */
  .think{background:rgba(185,103,255,.06);border:1px solid rgba(185,103,255,.28);border-left:2px solid var(--purple);border-radius:4px 8px 8px 4px;padding:9px 13px}
  .think .lbl{font-family:var(--mono);color:var(--purple);font-size:10px;text-transform:uppercase;letter-spacing:.16em;display:block;margin-bottom:4px}
  .think pre{margin:0;white-space:pre-wrap;word-break:break-word;color:#c3b8e6;font-size:12.5px;font-style:italic;font-family:var(--ui)}
  /* tool calls are collapsed by default: header shows kind + command, expand for output */
  details.tool{border:1px solid var(--line);border-radius:6px;overflow:hidden;background:rgba(13,16,34,.6)}
  details.tool[open]{border-color:rgba(5,217,232,.45);box-shadow:0 0 14px rgba(5,217,232,.12)}
  details.tool.err{border-color:var(--err);box-shadow:0 0 14px rgba(255,42,109,.2)}
  details.tool>summary{display:flex;align-items:center;gap:9px;padding:8px 12px;background:rgba(20,24,52,.7);cursor:pointer;list-style:none;user-select:none}
  details.tool>summary::-webkit-details-marker{display:none}
  details.tool>summary::before{content:"\25B8";color:var(--accent);font-size:11px;flex:none}
  details.tool[open]>summary::before{content:"\25BE"}
  details.tool>summary .kind{font-family:var(--mono);font-size:10px;text-transform:uppercase;letter-spacing:.1em;padding:2px 8px;border-radius:4px;background:rgba(5,217,232,.12);color:var(--accent);border:1px solid rgba(5,217,232,.35)}
  details.tool.err>summary .kind{color:var(--err);background:rgba(255,42,109,.12);border-color:rgba(255,42,109,.35)}
  details.tool>summary .cmd{font-family:var(--mono);font-size:12.5px;color:var(--fg);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  details.tool>summary .n{margin-left:auto;font-family:var(--mono);color:var(--muted);font-size:11px;font-variant-numeric:tabular-nums}
  details.tool pre{margin:0;padding:10px 12px;background:var(--term);white-space:pre-wrap;word-break:break-word;font-family:var(--mono);font-size:12.5px;max-height:300px;overflow:auto;color:#9fe8ee}
  details.tool pre.code{color:#cbd3e6;border-bottom:1px solid var(--line)}
  details.tool .raw{padding:0 12px 10px}
  details.tool .raw summary{cursor:pointer;color:var(--muted);font-family:var(--mono);font-size:11px;padding:6px 0}
  .note{color:var(--muted);font-family:var(--mono);font-size:12px;padding:1px 2px}
  .note.ok{color:var(--ok)} .note.warn{color:var(--warn)}
  .result{border-radius:6px;padding:12px 15px;position:relative;overflow:hidden}
  .result::before{content:"";position:absolute;left:0;top:0;bottom:0;width:3px}
  .result.ok{background:rgba(0,255,159,.07);border:1px solid rgba(0,255,159,.45);box-shadow:0 0 18px rgba(0,255,159,.14)}
  .result.ok::before{background:var(--ok);box-shadow:0 0 12px var(--ok)}
  .result.warn{background:rgba(252,238,10,.07);border:1px solid rgba(252,238,10,.45);box-shadow:0 0 18px rgba(252,238,10,.12)}
  .result.warn::before{background:var(--warn);box-shadow:0 0 12px var(--warn)}
  .result.err{background:rgba(255,42,109,.07);border:1px solid rgba(255,42,109,.45);box-shadow:0 0 18px rgba(255,42,109,.14)}
  .result.err::before{background:var(--err);box-shadow:0 0 12px var(--err)}
  .result .rt{font-family:var(--disp);font-weight:700;letter-spacing:.05em;text-transform:uppercase;margin-bottom:4px}
  .result.ok .rt{color:var(--ok)} .result.warn .rt{color:var(--warn)} .result.err .rt{color:var(--err)}
  .result .rs{white-space:pre-wrap;word-break:break-word}
  /* composer — a single unified input bar */
  .composer{border-top:1px solid var(--line);padding:14px 22px;background:linear-gradient(0deg,rgba(5,217,232,.05),transparent)}
  .inbox{display:flex;align-items:flex-end;gap:6px;background:rgba(4,5,14,.72);border:1px solid var(--line);border-radius:14px;padding:6px;transition:.15s}
  .inbox:focus-within{border-color:var(--accent);box-shadow:var(--glow),inset 0 0 16px rgba(5,217,232,.07)}
  .inbox textarea{flex:1;min-width:0;border:0;background:transparent;color:var(--fg);resize:none;max-height:170px;height:38px;padding:8px;font:inherit;line-height:1.45;outline:none}
  .inbox textarea::placeholder{color:var(--muted)}
  .attach{flex:none;width:38px;height:38px;display:flex;align-items:center;justify-content:center;background:transparent;border:0;border-radius:9px;color:var(--muted);cursor:pointer;transition:.12s}
  .attach:hover{color:var(--accent);background:rgba(5,217,232,.1)}
  .inbox .send,.inbox .stop{flex:none;height:38px;border:0;border-radius:9px;padding:0 16px;font-family:var(--disp);letter-spacing:.06em;text-transform:uppercase;font-size:12.5px;font-weight:700;cursor:pointer;transition:.15s}
  .inbox .send{background:linear-gradient(135deg,var(--accent),var(--accent2));color:#050510;box-shadow:0 0 14px rgba(5,217,232,.35)}
  .inbox .send:hover:not(:disabled){box-shadow:0 0 22px rgba(255,42,109,.5)}
  .inbox .stop{background:var(--panel2);color:var(--fg);border:1px solid var(--line)}
  .inbox .stop:hover:not(:disabled){border-color:var(--err);color:var(--err);box-shadow:var(--glowp)}
  .inbox button:disabled{opacity:.4;cursor:default}
  .attachrow{display:flex;flex-wrap:wrap;gap:8px;padding:0 22px}
  .attachrow:not(:empty){padding-top:11px}
  .chip{display:flex;align-items:center;gap:7px;background:var(--panel2);border:1px solid rgba(5,217,232,.35);border-radius:6px;padding:4px 6px 4px 8px;max-width:220px;box-shadow:0 0 10px rgba(5,217,232,.12)}
  .chip img{width:30px;height:30px;object-fit:cover;border-radius:4px}
  .chip .cn{font-family:var(--mono);font-size:11.5px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .chip .cx{cursor:pointer;color:var(--muted);border:0;background:none;font-size:15px;padding:0 2px}
  .chip .cx:hover{color:var(--err)}
  .app.drag .thread{outline:2px dashed var(--accent);outline-offset:-10px;background:rgba(5,217,232,.04)}
  /* inline attachments in the chat */
  .atts{display:flex;flex-wrap:wrap;gap:8px;margin-top:8px}
  .att-img{max-width:min(320px,80%);border-radius:6px;border:1px solid rgba(5,217,232,.4);display:block;cursor:pointer;box-shadow:0 0 14px rgba(5,217,232,.2)}
  .att-file{display:inline-flex;align-items:center;gap:8px;background:var(--panel);border:1px solid var(--line);border-radius:6px;padding:8px 12px;color:var(--fg);text-decoration:none;font-family:var(--mono);font-size:12.5px;transition:.12s}
  .att-file:hover{border-color:var(--accent);color:var(--accent);box-shadow:var(--glow)}
  .att-file .fi{font-size:16px}
  .filemsg{display:flex;flex-direction:column;gap:6px}
  .filemsg .cap{color:var(--muted);font-size:12.5px}
  /* rail */
  .rail{background:linear-gradient(180deg,rgba(13,16,34,.7),rgba(8,10,21,.7));border-left:1px solid var(--line);overflow:auto;padding:16px;backdrop-filter:blur(4px)}
  .app.railHidden .rail{display:none}
  .rail h2{font-family:var(--mono);font-size:10.5px;text-transform:uppercase;letter-spacing:.16em;color:var(--accent);margin:16px 0 9px;
    padding-bottom:5px;border-bottom:1px solid rgba(5,217,232,.2)}
  .rail h2:first-child{margin-top:0}
  ol.plan{margin:0;padding-left:18px} ol.plan li{margin:5px 0}
  ol.plan li .stx{font-family:var(--mono);font-size:9.5px;text-transform:uppercase;letter-spacing:.06em;padding:1px 7px;border-radius:4px;margin-left:6px}
  .stx.pending{background:var(--panel2);color:var(--muted)} .stx.in_progress{background:rgba(5,217,232,.18);color:var(--accent);box-shadow:0 0 8px rgba(5,217,232,.25)} .stx.done{background:rgba(0,255,159,.18);color:var(--ok)}
  ul.files{list-style:none;margin:0;padding:0} ul.files li{padding:4px 0;font-family:var(--mono);font-size:12px}
  ul.files a{color:var(--accent);cursor:pointer;text-decoration:none} ul.files a:hover{text-shadow:var(--glow)}
  ul.files .sz{color:var(--muted);font-size:10.5px;margin-left:6px}
  .kv{display:flex;justify-content:space-between;gap:12px;font-family:var(--mono);font-size:12px;padding:4px 0;color:var(--muted)}
  .kv span{flex:none}
  .kv b{color:var(--fg);font-weight:400;text-align:right;flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .kv b:only-child{color:var(--accent)}
  .rail h2 .svcref{float:right;background:none;border:1px solid var(--line);color:var(--muted);border-radius:5px;padding:0 7px;font-size:12px;line-height:1.4;cursor:pointer}
  .rail h2 .svcref:hover{border-color:var(--accent);color:var(--accent)}
  ul.svcs{list-style:none;margin:0;padding:0}
  ul.svcs li{padding:7px 0;border-bottom:1px solid rgba(38,49,92,.5)} ul.svcs li:last-child{border-bottom:0}
  .svc-top{display:flex;align-items:center;gap:7px}
  .svc-dot{width:7px;height:7px;border-radius:50%;background:var(--muted);flex:none} .svc-dot.on{background:var(--ok);box-shadow:0 0 8px var(--ok)}
  .svc-name{font-family:var(--mono);font-size:12.5px;color:var(--fg);flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .svcstop{padding:2px 9px;font-size:10.5px} .svcstop:hover{border-color:var(--err);color:var(--err)}
  .svc-sub{font-family:var(--mono);font-size:10.5px;color:var(--muted);margin-top:3px;padding-left:14px}
  .modal{position:fixed;inset:0;background:rgba(2,3,10,.75);display:none;align-items:center;justify-content:center;padding:30px;z-index:20;backdrop-filter:blur(3px)}
  .modal.show{display:flex}
  .modal .box{background:var(--panel);border:1px solid rgba(5,217,232,.4);border-radius:8px;max-width:900px;width:100%;max-height:80vh;display:flex;flex-direction:column;box-shadow:0 0 40px rgba(5,217,232,.2),0 0 80px rgba(255,42,109,.1)}
  .modal .mh{display:flex;align-items:center;padding:12px 16px;border-bottom:1px solid var(--line)}
  .modal .mh b{font-family:var(--disp);letter-spacing:.06em;text-transform:uppercase;font-size:13px;color:var(--accent)}
  .modal .mh button{margin-left:auto;background:var(--panel2);color:var(--fg);border:1px solid var(--line);border-radius:6px;cursor:pointer;padding:5px 12px;font-family:var(--mono);font-size:11.5px}
  .modal .mh button:hover{border-color:var(--accent)}
  .modal pre{margin:0;padding:14px;overflow:auto;white-space:pre-wrap;word-break:break-word;font-family:var(--mono);font-size:12.5px;color:#9fe8ee}
  /* model bar under the composer */
  .combar{display:flex;align-items:center;gap:10px;padding:0 22px 14px}
  .combar .lbl{font-family:var(--mono);color:var(--accent);font-size:10.5px;text-transform:uppercase;letter-spacing:.14em}
  .combar select{background:rgba(4,5,14,.7);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:6px 10px;font-family:var(--mono);font-size:12px;max-width:300px}
  .combar select:focus{outline:none;border-color:var(--accent);box-shadow:var(--glow)}
  .mini{background:var(--panel2);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:6px 11px;cursor:pointer;font-family:var(--mono);font-size:11.5px;transition:.12s}
  .mini:hover{border-color:var(--accent);color:var(--accent);box-shadow:var(--glow)}
  .cfg-field{display:flex;flex-direction:column;gap:5px;margin-bottom:13px}
  .cfg-field label{font-family:var(--mono);font-size:11px;color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
  .cfg-field input,.cfg-field textarea{background:rgba(4,5,14,.7);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:8px 11px;font:inherit}
  .cfg-field input:focus,.cfg-field textarea:focus{outline:none;border-color:var(--accent);box-shadow:var(--glow)}
  .cfg-field textarea{min-height:96px;resize:vertical;font-family:var(--mono);font-size:12.5px}
  .cfg-hint{color:var(--muted);font-size:11.5px;margin-top:-2px}
  .cfg-msg{font-family:var(--mono);font-size:12px;margin-top:4px;color:var(--accent)}
  .src-card{border:1px solid var(--line);border-radius:6px;padding:13px;margin-bottom:12px;background:rgba(20,24,52,.5)}
  .src-card .src-head{display:flex;align-items:center;gap:8px;margin-bottom:10px}
  .src-card .src-title{font-family:var(--disp);font-size:13px;font-weight:600;letter-spacing:.03em;flex:1;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--purple)}
  .src-card .cfg-field{margin-bottom:10px}
  .src-card .cfg-field textarea{min-height:64px}
  .src-adv{margin-top:4px}
  .src-adv>summary{cursor:pointer;font-family:var(--mono);font-size:11px;color:var(--muted);text-transform:uppercase;letter-spacing:.06em;padding:4px 0;user-select:none}
  .src-adv>summary:hover{color:var(--accent)}
  .cfg-row2{display:grid;grid-template-columns:1fr 1fr;gap:10px}
  .cfg-check{display:flex;align-items:flex-start;gap:8px;font-size:12px;color:var(--muted);line-height:1.4;margin-top:4px;cursor:pointer}
  .cfg-check input{margin-top:2px}
  .src-card .cfg-field select{background:rgba(4,5,14,.7);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:8px 11px;font:inherit}
  .modal .box.cfg{max-width:600px}
  .modal .box .body{padding:16px;overflow:auto}
  .modal .mh .actions{margin-left:auto;display:flex;gap:8px}
  .modal .mh .actions .save{background:linear-gradient(135deg,var(--accent),var(--accent2));color:#050510;border:0;font-weight:700;box-shadow:0 0 14px rgba(5,217,232,.4)}
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
    <div class="attachrow" id="attachRow"></div>
    <form class="composer" id="form">
      <input type="file" id="fileInput" multiple hidden>
      <div class="inbox">
        <button type="button" class="attach" id="attachBtn" title="Attach files (or drag &amp; drop / paste)" aria-label="Attach files"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg></button>
        <textarea id="task" rows="1" placeholder="Ask a question or describe a task.  (Enter to send, Shift+Enter for newline)"></textarea>
        <button type="button" class="stop" id="stopBtn" disabled>Stop</button>
        <button type="submit" class="send" id="sendBtn">Run</button>
      </div>
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
    <h2>Usage</h2>
    <div id="railUsage"><div class="note">No usage yet.</div></div>
    <h2>Plan</h2>
    <div id="railPlan"><div class="note">No plan yet.</div></div>
    <h2>Produced files</h2>
    <div id="railFiles"><div class="note">Files appear when a run ends.</div></div>
    <h2>Services <button class="mini svcref" id="svcRefresh" title="Refresh services">↻</button></h2>
    <div id="railServices"><div class="note">No background services.</div></div>
  </aside>
</div>

<div id="modal" class="modal"><div class="box">
  <div class="mh"><b id="modalName"></b><button onclick="closeModal()">Close</button></div>
  <pre id="modalBody"></pre>
</div></div>

<div id="cfgModal" class="modal"><div class="box cfg">
  <div class="mh"><b>Model sources</b>
    <span class="actions">
      <button class="mini save" id="cfgSave" type="button">Save</button>
      <button class="mini" id="cfgClose" type="button">Close</button>
    </span>
  </div>
  <div class="body">
    <div id="cfgSources"></div>
    <button class="mini" id="cfgAdd" type="button">+ Add source</button>
    <div class="cfg-field" style="margin-top:16px">
      <label>Default model (used when nothing is picked under the composer)</label>
      <select id="cfgDefault"></select>
    </div>
    <div class="src-card" style="margin-top:16px">
      <div class="src-head"><span class="src-title">Embeddings (semantic recall)</span></div>
      <div class="cfg-hint" style="margin-bottom:10px">Independent endpoint used to recall past lessons by meaning. Leave the model blank to disable (falls back to lexical matching). Blank base URL / key reuse the chat connection.</div>
      <div class="cfg-field"><label>Embed base URL</label><input id="embBase" placeholder="https://host/v1  (blank = same as chat)"></div>
      <div class="cfg-field"><label>Embed API key</label><input id="embKey" type="password" placeholder="blank = keep current"><div class="cfg-hint" id="embHint"></div></div>
      <div class="cfg-field"><label>Embed model</label><input id="embModel" placeholder="e.g. embedding-3 / text-embedding-3-small"></div>
      <div style="display:flex;align-items:center;gap:10px"><button class="mini" id="embTest" type="button">Test connection</button><span class="cfg-hint" id="embTestMsg" style="margin:0"></span></div>
    </div>
    <div class="cfg-field" style="margin-top:16px">
      <label><input type="checkbox" id="cfgVision" style="width:auto;margin-right:8px;vertical-align:middle"> Send attached images to the model (vision)</label>
      <div class="cfg-hint">When on, small uploaded images are sent to the model as pictures (needs a vision-capable model). When off — or if the model rejects them — images are still saved as files the agent can read.</div>
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

/* ---------- lightweight, safe markdown (input escaped first) ---------- */
var BT=String.fromCharCode(96), FENCE=BT+BT+BT;
function mdInline(s){
  var code=new RegExp(BT+'([^'+BT+']+)'+BT,'g');
  s=s.replace(code,function(_,c){return '<code>'+c+'</code>'});
  s=s.replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>');
  s=s.replace(/(^|[^*])\*([^*\n]+)\*/g,'$1<em>$2</em>');
  s=s.replace(/\[([^\]]+)\]\((https?:[^\s)]+)\)/g,'<a href="$2" target="_blank" rel="noopener">$1</a>');
  return s;
}
function codeBlockHTML(escCode){
  return '<div class="codeblock"><button class="copy" type="button" onclick="copyCode(this)">copy</button><pre><code>'+escCode+'</code></pre></div>';
}
function copyCode(btn){
  var code=btn.parentNode.querySelector('code'); var txt=(code&&code.textContent)||'';
  if(navigator.clipboard) navigator.clipboard.writeText(txt);
  var old=btn.textContent; btn.textContent='copied'; setTimeout(function(){btn.textContent=old},1200);
}
function renderMarkdown(text){
  var lines=esc(text==null?'':String(text)).split('\n'), out=[], i=0;
  function isBlock(l){ return l.trim().slice(0,3)===FENCE||/^(#{1,6})\s+/.test(l)||/^\s*[-*]\s+/.test(l)||/^\s*\d+\.\s+/.test(l)||/^\s*>\s?/.test(l); }
  while(i<lines.length){
    var line=lines[i];
    if(line.trim().slice(0,3)===FENCE){
      var buf=[]; i++;
      while(i<lines.length&&lines[i].trim().slice(0,3)!==FENCE){ buf.push(lines[i]); i++; }
      i++; out.push(codeBlockHTML(buf.join('\n'))); continue;
    }
    var hm=/^(#{1,6})\s+(.*)$/.exec(line);
    if(hm){ var lv=hm[1].length; out.push('<h'+lv+' class="mh'+lv+'">'+mdInline(hm[2])+'</h'+lv+'>'); i++; continue; }
    if(/^\s*[-*]\s+/.test(line)){ var it=[]; while(i<lines.length&&/^\s*[-*]\s+/.test(lines[i])){ it.push('<li>'+mdInline(lines[i].replace(/^\s*[-*]\s+/,''))+'</li>'); i++; } out.push('<ul>'+it.join('')+'</ul>'); continue; }
    if(/^\s*\d+\.\s+/.test(line)){ var ot=[]; while(i<lines.length&&/^\s*\d+\.\s+/.test(lines[i])){ ot.push('<li>'+mdInline(lines[i].replace(/^\s*\d+\.\s+/,''))+'</li>'); i++; } out.push('<ol>'+ot.join('')+'</ol>'); continue; }
    if(/^\s*>\s?/.test(line)){ var q=[]; while(i<lines.length&&/^\s*>\s?/.test(lines[i])){ q.push(mdInline(lines[i].replace(/^\s*>\s?/,''))); i++; } out.push('<blockquote>'+q.join('<br>')+'</blockquote>'); continue; }
    if(line.trim()===''){ i++; continue; }
    var para=[]; while(i<lines.length&&lines[i].trim()!==''&&!isBlock(lines[i])){ para.push(lines[i]); i++; }
    out.push('<p>'+mdInline(para.join('\n')).replace(/\n/g,'<br>')+'</p>');
  }
  return out.join('');
}
function fmtMs(ms){ ms=ms||0; if(ms<1000)return ms+' ms'; var s=ms/1000; return s<60?s.toFixed(1)+' s':Math.floor(s/60)+'m '+Math.round(s%60)+'s'; }

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
        +'<div class="t"><div class="ttl">'+esc(e.title||e.task)+'</div><div class="sub">'+sub+'</div></div>';
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

function addUser(text,atts){
  $('emptyState') && $('emptyState').remove();
  var u=document.createElement('div'); u.className='user';
  if(text){ var t=document.createElement('div'); t.textContent=text; u.appendChild(t); }
  if(atts&&atts.length){ u.appendChild(attsEl(currentSession,atts)); }
  thread.appendChild(u); scroll();
}
/* build an inline preview element for a list of attachments (images shown, other files linked) */
function attsEl(run,atts){
  var wrap=document.createElement('div'); wrap.className='atts';
  atts.forEach(function(a){
    var url='/file?run='+encodeURIComponent(run||'')+'&name='+encodeURIComponent(a.name);
    if(a.image){
      var im=document.createElement('img'); im.className='att-img'; im.src=url;
      im.title=a.base||a.name; im.onclick=function(){window.open(url,'_blank')};
      wrap.appendChild(im);
    }else{
      var link=document.createElement('a'); link.className='att-file'; link.href=url+'&download=1';
      link.setAttribute('download',''); link.target='_blank';
      link.innerHTML='<span class="fi">📎</span><span>'+esc(a.base||a.name)+'</span>';
      wrap.appendChild(link);
    }
  });
  return wrap;
}
/* a file the agent sent back to the user (from a 'file' event) */
function addFileMsg(ev){
  var name=(ev.args&&ev.args.name)||''; if(!name)return;
  var run=ev.run||currentSession;
  var box=document.createElement('div'); box.className='msg filemsg';
  var cap=(ev.args&&ev.args.caption)||'';
  if(cap){ var c=document.createElement('div'); c.className='cap'; c.textContent=cap; box.appendChild(c); }
  box.appendChild(attsEl(run,[{name:name,base:name.split('/').pop(),image:!!(ev.args&&ev.args.image)}]));
  turn().appendChild(box); scroll();
}
function addThink(ev){
  var d=document.createElement('div'); d.className='think';
  d.innerHTML='<span class="lbl">thinking</span><pre>'+esc(ev.text||'')+'</pre>';
  turn().appendChild(d); scroll();
}
function addMessage(ev){
  var m=document.createElement('div'); m.className='msg md'; m.innerHTML=renderMarkdown(ev.text||'');
  turn().appendChild(m); scroll();
}

/* streaming: append token chunks to a per-step think/message element */
var deltaEls={};
function makeThinkEl(){
  var d=document.createElement('div'); d.className='think';
  d.innerHTML='<span class="lbl">thinking</span><pre></pre>';
  turn().appendChild(d); return d.querySelector('pre');
}
function makeMsgEl(){
  var m=document.createElement('div'); m.className='msg md';
  turn().appendChild(m); return m;
}
function addDelta(ev){
  var key=(ev.step||0)+':'+(ev.kind||'message');
  var el=deltaEls[key];
  if(ev.kind==='think'){
    if(!el){ el=makeThinkEl(); deltaEls[key]=el; }
    el.textContent+=(ev.text||'');
  }else{
    if(!el){ el=makeMsgEl(); el._raw=''; deltaEls[key]=el; }
    el._raw+=(ev.text||''); el.innerHTML=renderMarkdown(el._raw);
  }
  scroll();
}

/* live stdout streamed from run_shell/run_python before the final tool card */
var liveOuts={};
function addStdout(ev){
  var el=liveOuts[ev.step];
  if(!el){
    var card=document.createElement('details'); card.className='tool live'; card.open=true;
    var kind=KIND[ev.tool]||ev.tool;
    card.innerHTML='<summary><span class="kind">'+esc(kind)+'</span><span class="cmd">running…</span><span class="n">#'+(ev.step||'')+'</span></summary><pre class="liveout"></pre>';
    turn().appendChild(card);
    el=card.querySelector('.liveout'); el._card=card; liveOuts[ev.step]=el;
  }
  el.textContent+=(ev.text||''); el.scrollTop=el.scrollHeight; scroll();
}
function clearLive(step){
  var el=liveOuts[step]; if(el&&el._card&&el._card.parentNode) el._card.parentNode.removeChild(el._card);
  delete liveOuts[step];
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
    +(o.episode?'<div class="kv"><span>episode</span><b title="'+escA(o.episode)+'">'+esc(o.episode)+'</b></div>':'');
}

function renderUsage(o){
  var el=$('railUsage');
  if(!o||(!o.total_tokens&&!o.prompt_tokens&&!o.completion_tokens&&!o.elapsed_ms)){ el.innerHTML='<div class="note">No usage yet.</div>'; return; }
  el.innerHTML='<div class="kv"><span>tokens</span><b>'+(o.total_tokens||0)+'</b></div>'
    +'<div class="kv"><span>prompt</span><b>'+(o.prompt_tokens||0)+'</b></div>'
    +'<div class="kv"><span>completion</span><b>'+(o.completion_tokens||0)+'</b></div>'
    +'<div class="kv"><span>llm calls</span><b>'+(o.llm_calls||0)+'</b></div>'
    +'<div class="kv"><span>elapsed</span><b>'+fmtMs(o.elapsed_ms)+'</b></div>';
}
/* ---------- background services panel ---------- */
function loadServices(session){
  session=session||currentSession;
  if(!session){ $('railServices').innerHTML='<div class="note">No background services.</div>'; return; }
  fetch('/services?session='+encodeURIComponent(session)).then(function(r){return r.json()})
    .then(function(d){ renderServices(session,(d&&d.services)||[]); }).catch(function(){});
}
function renderServices(session,svcs){
  var el=$('railServices');
  if(!svcs||!svcs.length){ el.innerHTML='<div class="note">No background services.</div>'; return; }
  var h='<ul class="svcs">';
  svcs.forEach(function(s){
    var nm=escA(s.name);
    h+='<li><div class="svc-top"><span class="svc-dot '+(s.alive?'on':'')+'"></span><span class="svc-name" title="'+escA(s.cmd||s.name)+'">'+esc(s.name)+'</span>';
    if(s.alive) h+='<button class="mini svcstop" type="button" onclick="stopService(\''+escA(session)+'\',\''+nm+'\')">Stop</button>';
    h+='</div><div class="svc-sub">'+(s.port?('port '+s.port+' · '):'')+'pid '+(s.pid||0)+' · '+(s.alive?'alive':'exited')+'</div></li>';
  });
  el.innerHTML=h+'</ul>';
}
function stopService(session,name){
  fetch('/services?session='+encodeURIComponent(session)+'&name='+encodeURIComponent(name),{method:'POST'})
    .then(function(r){return r.json()}).then(function(d){ renderServices(session,(d&&d.services)||[]); }).catch(function(){});
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
  stopRun(); currentSession=null; curTurn=null; deltaEls={}; liveOuts={};
  pendingAtts=[]; renderChips();
  thread.innerHTML=emptyHTML();
  $('chatTitle').textContent='New session'; setBadge('');
  renderPlan(null); renderFiles(null); renderUsage(null); renderServices(null,[]);
  $('railStatus').innerHTML='<div class="note">No active run.</div>';
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
  var atts=pendingAtts.slice(); pendingAtts=[]; renderChips();
  addUser(task,atts);
  curTurn=newAssistTurn(); deltaEls={}; liveOuts={};
  setRunning(true); setBadge('running','run');
  if(!currentSession){ $('chatTitle').textContent=task; renderRailStatus({status:'running',steps:0}); }
  var pk=splitPick($('modelSel').value||'');
  var url='/stream?task='+encodeURIComponent(task)
    +(currentSession?('&session='+encodeURIComponent(currentSession)):'')
    +(pk.model?('&model='+encodeURIComponent(pk.model)):'')
    +(pk.source?('&source='+encodeURIComponent(pk.source)):'');
  atts.forEach(function(a){ url+='&att='+encodeURIComponent(a.name); });
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
      case 'delta': addDelta(ev); break;
      case 'stdout': addStdout(ev); break;
      case 'usage': renderUsage(ev); break;
      case 'think': addThink(ev); break;
      case 'message': addMessage(ev); break;
      case 'title':
        if(ev.text){ $('chatTitle').textContent=ev.text; loadSessions(currentSession); }
        break;
      case 'step':
        clearLive(ev.step);
        if(ev.tool==='update_plan'||ev.tool==='set_title')break;
        addToolCard(ev.step,ev.tool,ev.args,ev.output,ev.is_error);
        renderRailStatus({status:'running',steps:ev.step,episode:currentSession});
        if(ev.tool==='start_service'||ev.tool==='stop_service') loadServices(currentSession);
        break;
      case 'plan': renderPlan(ev.plan); break;
      case 'file': addFileMsg(ev); break;
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
        /* usage is kept live via 'usage' events (which include the post-run title call); avoid downgrading it here */
        renderFiles(ev.run,ev.files);
        loadServices(ev.session||currentSession);
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
  stopRun(); currentSession=id; curTurn=null; deltaEls={}; liveOuts={};
  pendingAtts=[]; renderChips();
  clr(thread);
  loadSessions(id); loadServices(id); renderUsage(null);
  fetch('/episode?id='+encodeURIComponent(id)).then(function(r){return r.json()}).then(function(d){
    var ep=d.episode||{}; var events=d.events||[];
    $('chatTitle').textContent=ep.title||ep.task||id;
    var haveUser=false;
    events.forEach(function(e){
      var args={}; try{args=JSON.parse(e.args||'{}')}catch(_){args={}}
      if(e.tool==='(user)'){ addUser(e.result||''); curTurn=newAssistTurn(); haveUser=true; return; }
      if(e.tool==='(assistant)'){ addMessage({text:e.result||''}); return; }
      if(e.tool==='reply'){ addMessage({text:(args&&args.text)||e.result||''}); return; }
      if(e.tool==='send_file'){ var p=(args&&args.path)||''; if(p) addFileMsg({run:id,args:{name:p,image:/\.(png|jpe?g|gif|webp|bmp)$/i.test(p),caption:(args&&args.caption)||''}}); return; }
      if(e.tool==='update_plan'||e.tool==='finish'||e.tool==='set_title'){ return; }
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

/* ---------- model picker (grouped by source) ---------- */
var $$=function(sel){return Array.prototype.slice.call(document.querySelectorAll(sel))};
function escA(s){return esc(s).replace(/"/g,'&quot;')}
function splitPick(v){v=v||'';var i=v.indexOf('||');return i>=0?{source:v.slice(0,i),model:v.slice(i+2)}:{source:'',model:v}}

function fillModelSelect(sources, model, source){
  var sel=$('modelSel'); clr(sel);
  var want=localStorage.getItem('liveagent_pick');
  if(!want && source && model) want=source+'||'+model;
  var matched=false;
  (sources||[]).forEach(function(s){
    if(!(s.models&&s.models.length)) return;
    var og=document.createElement('optgroup'); og.label=s.name||s.base_url;
    s.models.forEach(function(m){
      var o=document.createElement('option'); o.value=s.base_url+'||'+m; o.textContent=m;
      if(o.value===want){o.selected=true;matched=true;}
      og.appendChild(o);
    });
    sel.appendChild(og);
  });
  if(!sel.options.length){ var o=document.createElement('option'); o.value=''; o.textContent='(add a source in ⚙)'; sel.appendChild(o); }
  else if(!matched){ sel.selectedIndex=0; localStorage.setItem('liveagent_pick', sel.value); }
}
function loadModels(){
  fetch('/models').then(function(r){return r.json()}).then(function(d){
    fillModelSelect(d.sources, d.model, d.source);
  }).catch(function(){});
}
$('modelSel') && $('modelSel').addEventListener('change',function(){
  localStorage.setItem('liveagent_pick', this.value);
});
$('fetchBtn').addEventListener('click',function(){
  var pk=splitPick($('modelSel').value||''); var b=this;
  if(!pk.source){ openCfg(); return; }
  b.textContent='...';
  fetch('/models?fetch=1&source='+encodeURIComponent(pk.source)).then(function(r){return r.json()}).then(function(d){
    fillModelSelect(d.sources); b.textContent='Refresh';
    if(d.error) alert('Fetch failed: '+d.error);
  }).catch(function(){ b.textContent='Refresh'; });
});

/* ---------- multi-source settings ---------- */
function srcCardHTML(s){
  s=s||{name:'',base_url:'',models:[],has_key:false};
  var eff=s.reasoning_effort||'';
  function effOpt(v,label){ return '<option value="'+v+'"'+(eff===v?' selected':'')+'>'+label+'</option>'; }
  var tempVal=(s.temperature!==undefined&&s.temperature!==null)?String(s.temperature):'';
  return '<div class="src-card">'
    +'<div class="src-head"><span class="src-title">'+esc(s.name||s.base_url||'New source')+'</span>'
      +'<button class="mini src-fetch" type="button">Fetch models</button>'
      +'<button class="mini src-remove" type="button">Remove</button></div>'
    +'<div class="cfg-field"><label>Name</label><input class="s-name" value="'+escA(s.name||'')+'" placeholder="my-gateway"></div>'
    +'<div class="cfg-field"><label>Base URL (OpenAI-compatible)</label><input class="s-base" value="'+escA(s.base_url||'')+'" placeholder="https://host/v1"></div>'
    +'<div class="cfg-field"><label>API key</label><input class="s-key" type="password" placeholder="'+(s.has_key?'leave blank to keep current':'sk-...')+'"><div class="cfg-hint">'+(s.has_key?'A key is set; leave blank to keep it.':'No key set.')+'</div></div>'
    +'<div class="cfg-field"><label>Models (one per line)</label><textarea class="s-models">'+esc((s.models||[]).join('\n'))+'</textarea></div>'
    +'<details class="src-adv"><summary>Advanced (per-endpoint)</summary>'
      +'<div class="cfg-row2">'
        +'<div class="cfg-field"><label>API path</label><input class="s-path" value="'+escA(s.api_path||'')+'" placeholder="/chat/completions"></div>'
        +'<div class="cfg-field"><label>Max tokens</label><input class="s-maxtok" type="number" min="0" value="'+(s.max_tokens?String(s.max_tokens):'')+'" placeholder="model default"></div>'
      +'</div>'
      +'<div class="cfg-row2">'
        +'<div class="cfg-field"><label>Temperature override</label><input class="s-temp" type="number" step="0.1" value="'+escA(tempVal)+'" placeholder="use per-call"></div>'
        +'<div class="cfg-field"><label>Reasoning effort</label><select class="s-effort">'+effOpt('','default')+effOpt('low','low')+effOpt('medium','medium')+effOpt('high','high')+'</select></div>'
      +'</div>'
      +'<label class="cfg-check"><input type="checkbox" class="s-omittemp"'+(s.omit_temperature?' checked':'')+'> Omit temperature (for models that only accept their default, e.g. GPT-5 / o-series)</label>'
    +'</details>'
    +'</div>';
}
function renderSources(sources){
  var c=$('cfgSources'); c.innerHTML='';
  if(sources&&sources.length){ sources.forEach(function(s){ c.insertAdjacentHTML('beforeend', srcCardHTML(s)); }); }
  else { c.insertAdjacentHTML('beforeend', srcCardHTML(null)); }
}
function sourcesFromDOM(){
  return $$('#cfgSources .src-card').map(function(card){
    var o={
      name: card.querySelector('.s-name').value.trim(),
      base_url: card.querySelector('.s-base').value.trim(),
      api_key: card.querySelector('.s-key').value,
      models: card.querySelector('.s-models').value.split('\n').map(function(x){return x.trim()}).filter(Boolean),
      api_path: (card.querySelector('.s-path').value||'').trim(),
      omit_temperature: card.querySelector('.s-omittemp').checked,
      reasoning_effort: card.querySelector('.s-effort').value
    };
    var mt=parseInt(card.querySelector('.s-maxtok').value,10); if(!isNaN(mt)&&mt>0) o.max_tokens=mt;
    var tv=card.querySelector('.s-temp').value.trim();
    if(tv!==''){ var t=parseFloat(tv); if(!isNaN(t)) o.temperature=t; }
    return o;
  }).filter(function(s){return s.base_url});
}
function rebuildDefault(sources, model, source){
  var list=sources||sourcesFromDOM();
  var sel=$('cfgDefault'); var want=(source||'')+'||'+(model||''); clr(sel);
  list.forEach(function(s){
    if(!(s.models&&s.models.length)) return;
    var og=document.createElement('optgroup'); og.label=s.name||s.base_url;
    s.models.forEach(function(m){ var o=document.createElement('option'); o.value=s.base_url+'||'+m; o.textContent=m; if(o.value===want)o.selected=true; og.appendChild(o); });
    sel.appendChild(og);
  });
}
function postSettings(extra){
  var body=Object.assign({sources:sourcesFromDOM()}, extra||{});
  return fetch('/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(r){return r.json()});
}
function openCfg(){
  fetch('/settings').then(function(r){return r.json()}).then(function(d){
    renderSources(d.sources);
    rebuildDefault(d.sources, d.model, d.source);
    var e=d.embed||{};
    $('embBase').value=e.base_url||'';
    $('embModel').value=e.model||'';
    $('embKey').value='';
    $('embHint').textContent=e.has_key?'A key is set; leave blank to keep it.':'No key set (blank reuses the chat key).';
    $('cfgVision').checked=(d.vision!==false);
    $('cfgMsg').textContent='';
    $('cfgModal').className='modal show';
  });
}
function closeCfg(){ $('cfgModal').className='modal'; }
function saveCfg(){
  var dv=splitPick($('cfgDefault').value||'');
  var embed={base_url:$('embBase').value.trim(), api_key:$('embKey').value, model:$('embModel').value.trim()};
  $('cfgMsg').textContent='Saving...';
  postSettings({model:dv.model, source:dv.source, embed:embed, vision:$('cfgVision').checked}).then(function(d){
    $('cfgMsg').textContent='Saved.';
    renderSources(d.sources); rebuildDefault(d.sources, d.model, d.source);
    fillModelSelect(d.sources, d.model, d.source);
    if(d.source&&d.model) localStorage.setItem('liveagent_pick', d.source+'||'+d.model);
    setTimeout(closeCfg,500);
  }).catch(function(){ $('cfgMsg').textContent='Save failed.'; });
}
$('cfgSources').addEventListener('click',function(e){
  var card=e.target.closest('.src-card'); if(!card) return;
  if(e.target.classList.contains('src-remove')){ card.remove(); rebuildDefault(); return; }
  if(e.target.classList.contains('src-fetch')){
    var base=card.querySelector('.s-base').value.trim();
    if(!base){ $('cfgMsg').textContent='Enter a base URL first.'; return; }
    $('cfgMsg').textContent='Saving & fetching...';
    postSettings({}).then(function(){
      return fetch('/models?fetch=1&source='+encodeURIComponent(base)).then(function(r){return r.json()});
    }).then(function(d){
      if(d.error){ $('cfgMsg').textContent='Fetch failed: '+d.error; return; }
      renderSources(d.sources); rebuildDefault(d.sources);
      $('cfgMsg').textContent='Fetched '+((d.fetched||[]).length)+' models.';
    }).catch(function(){ $('cfgMsg').textContent='Fetch failed.'; });
  }
});
$('cfgAdd').addEventListener('click',function(){ $('cfgSources').insertAdjacentHTML('beforeend', srcCardHTML(null)); });
$('embTest').addEventListener('click',function(){
  var m=$('embTestMsg'); m.style.color='var(--muted)'; m.textContent='Save first if you changed anything. Testing…';
  fetch('/embed/test').then(function(r){return r.json()}).then(function(d){
    if(d.ok){ m.style.color='var(--ok)'; m.textContent='OK — '+(d.model||'')+' ('+d.dims+' dims)'; }
    else { m.style.color='var(--err)'; m.textContent='Failed: '+(d.error||'unknown'); }
  }).catch(function(e){ m.style.color='var(--err)'; m.textContent='Failed: '+e; });
});
$('cfgBtn').addEventListener('click',openCfg);
$('cfgClose').addEventListener('click',closeCfg);
$('cfgSave').addEventListener('click',saveCfg);
$('svcRefresh').addEventListener('click',function(){ loadServices(currentSession); });

/* ---------- attachments ---------- */
var pendingAtts=[];
function renderChips(){
  var row=$('attachRow'); row.innerHTML='';
  pendingAtts.forEach(function(a,i){
    var chip=document.createElement('div'); chip.className='chip';
    if(a.image){ var im=document.createElement('img'); im.src='/file?run='+encodeURIComponent(currentSession||'')+'&name='+encodeURIComponent(a.name); chip.appendChild(im); }
    var nm=document.createElement('span'); nm.className='cn'; nm.textContent=a.base||a.name; nm.title=a.base||a.name; chip.appendChild(nm);
    var x=document.createElement('button'); x.className='cx'; x.type='button'; x.textContent='×';
    x.onclick=function(){ pendingAtts.splice(i,1); renderChips(); }; chip.appendChild(x);
    row.appendChild(chip);
  });
}
function uploadFiles(files){
  if(!files||!files.length||running)return;
  var fd=new FormData();
  for(var i=0;i<files.length;i++) fd.append('files',files[i]);
  var url='/upload'+(currentSession?('?session='+encodeURIComponent(currentSession)):'');
  $('attachBtn').textContent='…';
  fetch(url,{method:'POST',body:fd}).then(function(r){
    if(!r.ok)return r.text().then(function(t){throw new Error(t||('HTTP '+r.status))});
    return r.json();
  }).then(function(d){
    if(!currentSession && d.session) currentSession=d.session;
    (d.files||[]).forEach(function(f){ pendingAtts.push(f); });
    renderChips();
  }).catch(function(e){ addNote({text:'Upload failed: '+(e.message||e)},'warn'); })
  .then(function(){ $('attachBtn').textContent='+'; });
}
$('attachBtn').addEventListener('click',function(){ if(!running)$('fileInput').click(); });
$('fileInput').addEventListener('change',function(){ uploadFiles(this.files); this.value=''; });
$('task').addEventListener('paste',function(e){
  var items=(e.clipboardData||{}).items||[]; var imgs=[];
  for(var i=0;i<items.length;i++){ if(items[i].kind==='file'){ var f=items[i].getAsFile(); if(f)imgs.push(f); } }
  if(imgs.length){ e.preventDefault(); uploadFiles(imgs); }
});
(function(){
  var app=$('app'), depth=0;
  ['dragenter','dragover'].forEach(function(ev){ document.addEventListener(ev,function(e){ e.preventDefault(); }); });
  document.addEventListener('dragenter',function(e){ if(e.dataTransfer&&Array.prototype.indexOf.call(e.dataTransfer.types||[],'Files')>=0){ depth++; app.classList.add('drag'); } });
  document.addEventListener('dragleave',function(){ depth=Math.max(0,depth-1); if(!depth)app.classList.remove('drag'); });
  document.addEventListener('drop',function(e){ e.preventDefault(); depth=0; app.classList.remove('drag'); if(e.dataTransfer&&e.dataTransfer.files&&e.dataTransfer.files.length) uploadFiles(e.dataTransfer.files); });
})();

/* ---------- wire up ---------- */
function autoGrow(){ var ta=$('task'); ta.style.height='38px'; ta.style.height=Math.min(ta.scrollHeight,170)+'px'; }
$('task').addEventListener('input',autoGrow);
$('form').addEventListener('submit',function(e){
  e.preventDefault();
  if(running)return;
  var t=$('task').value.trim();
  if(!t&&!pendingAtts.length)return;
  if(!t) t='(see attached file'+(pendingAtts.length>1?'s':'')+')';
  $('task').value=''; autoGrow();
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
