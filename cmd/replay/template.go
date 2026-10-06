package main

// pageTemplate 是回放页面的模板，__DATA__ 会被替换为步骤 JSON。
// 纯原生 HTML/CSS/JavaScript，不引入任何框架或图形库。
const pageTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DAO 治理与时间锁回放</title>
<style>
:root{
  --bg:#0f1420; --panel:#1a2233; --panel2:#222c42; --text:#e6ebf5; --muted:#8b98b3;
  --accent:#5b8cff; --ok:#3ecf8e; --bad:#ff6b6b; --warn:#ffc857; --line:#2c3a58;
}
@media (prefers-color-scheme: light){
  :root{ --bg:#f4f6fb; --panel:#ffffff; --panel2:#eef1f8; --text:#1c2436; --muted:#5b6b8c;
    --accent:#2f5fd0; --ok:#0d9463; --bad:#d33; --warn:#b07d10; --line:#d8dfee; }
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);
  font-family:-apple-system,"PingFang SC","Helvetica Neue",sans-serif;line-height:1.6}
.wrap{max-width:920px;margin:0 auto;padding:24px 16px 64px}
h1{font-size:22px;margin:8px 0 4px}
.sub{color:var(--muted);font-size:13px;margin-bottom:20px}
.controls{display:flex;align-items:center;gap:10px;flex-wrap:wrap;
  background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:14px 16px;
  position:sticky;top:0;z-index:2}
button{background:var(--accent);color:#fff;border:0;border-radius:8px;
  padding:8px 16px;font-size:14px;cursor:pointer}
button:disabled{opacity:.35;cursor:default}
button.ghost{background:transparent;color:var(--accent);border:1px solid var(--accent)}
.progress{flex:1;min-width:120px;height:8px;background:var(--panel2);border-radius:4px;overflow:hidden}
.progress>div{height:100%;background:var(--accent);width:0;transition:width .2s}
.step-label{font-size:13px;color:var(--muted);white-space:nowrap}
.event{background:var(--panel);border:1px solid var(--line);border-radius:12px;
  padding:14px 16px;margin:16px 0}
.event .meta{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:6px}
.badge{display:inline-block;padding:2px 10px;border-radius:999px;font-size:12px;font-weight:600}
.badge.ok{background:rgba(62,207,142,.15);color:var(--ok)}
.badge.fail{background:rgba(255,107,107,.15);color:var(--bad)}
.badge.action{background:rgba(91,140,255,.15);color:var(--accent)}
.height{font-size:13px;color:var(--muted)}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:12px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:12px 14px}
.card h3{margin:0 0 6px;font-size:15px}
.card .row{display:flex;justify-content:space-between;font-size:13px;color:var(--muted);padding:1px 0}
.card .row b{color:var(--text);font-weight:600}
.state{font-size:12px;font-weight:700;padding:2px 8px;border-radius:6px}
.state.Active{background:rgba(91,140,255,.18);color:var(--accent)}
.state.Succeeded{background:rgba(62,207,142,.18);color:var(--ok)}
.state.Queued{background:rgba(255,200,87,.18);color:var(--warn)}
.state.Executed{background:rgba(62,207,142,.3);color:var(--ok)}
.state.Defeated,.state.Cancelled{background:rgba(255,107,107,.18);color:var(--bad)}
.queue{margin-top:16px;background:var(--panel);border:1px solid var(--line);
  border-radius:12px;padding:12px 16px;font-size:14px}
.queue code{background:var(--panel2);padding:2px 8px;border-radius:6px;margin-right:6px}
h2{font-size:16px;margin:22px 0 10px}
</style>
</head>
<body>
<div class="wrap">
  <h1>DAO 治理与时间锁 · 状态回放</h1>
  <div class="sub">权重快照 · 法定人数 40% · 通过阈值 50% · 时间锁延迟 10 区块 · 执行窗口 5 区块</div>

  <div class="controls">
    <button id="prev" class="ghost">← 上一步</button>
    <button id="next">下一步 →</button>
    <div class="progress"><div id="bar"></div></div>
    <span class="step-label" id="label"></span>
  </div>

  <div class="event" id="event"></div>

  <h2>提案状态</h2>
  <div class="grid" id="cards"></div>

  <div class="queue" id="queue"></div>
</div>
<script>
var STEPS = __DATA__;
var idx = -1;

function esc(s){var d=document.createElement('div');d.textContent=s;return d.innerHTML}

function render(){
  var prev=document.getElementById('prev'), next=document.getElementById('next');
  prev.disabled = idx < 0;
  next.disabled = idx >= STEPS.length-1;
  document.getElementById('bar').style.width = ((idx+1)/STEPS.length*100)+'%';
  document.getElementById('label').textContent = (idx+1)+' / '+STEPS.length;

  var ev=document.getElementById('event');
  if(idx < 0){
    ev.innerHTML='<div class="meta"><span class="badge action">初始</span></div>'+
      '初始分配：alice 400 / bob 200 / carol 400，总供应量 1000。点击「下一步」开始回放。';
  }else{
    var s=STEPS[idx];
    ev.innerHTML='<div class="meta">'+
      '<span class="badge action">'+esc(s.action)+'</span>'+
      (s.ok?'<span class="badge ok">已接受</span>':'<span class="badge fail">被拒绝</span>')+
      '<span class="height">区块高度 '+s.height+'</span></div>'+esc(s.detail);
  }

  var cards=document.getElementById('cards');
  cards.innerHTML='';
  var proposals = idx<0 ? [] : STEPS[idx].proposals;
  proposals.forEach(function(p){
    var c=document.createElement('div');c.className='card';
    c.innerHTML='<h3>#'+p.id+' '+esc(p.title)+' <span class="state '+p.state+'">'+p.state+'</span></h3>'+
      '<div class="row"><span>快照高度</span><b>'+p.snapshot+'</b></div>'+
      '<div class="row"><span>赞成 / 反对</span><b>'+p.forVotes+' / '+p.againstVotes+'</b></div>'+
      '<div class="row"><span>快照总供应</span><b>'+p.supply+'</b></div>'+
      (p.eta?'<div class="row"><span>ETA（最早可执行）</span><b>'+p.eta+'</b></div>':'');
    cards.appendChild(c);
  });

  var q=document.getElementById('queue');
  var qids = idx<0 ? [] : STEPS[idx].queue;
  q.innerHTML='<b>时间锁队列：</b>'+(qids.length
    ? qids.map(function(i){return '<code>提案 #'+i+'</code>'}).join('')
    : '<span style="color:var(--muted)">（空）</span>');
}

document.getElementById('prev').onclick=function(){ if(idx>=-1){idx--;render()} };
document.getElementById('next').onclick=function(){ if(idx<STEPS.length-1){idx++;render()} };
document.addEventListener('keydown',function(e){
  if(e.key==='ArrowLeft')document.getElementById('prev').click();
  if(e.key==='ArrowRight')document.getElementById('next').click();
});
render();
</script>
</body>
</html>
`
