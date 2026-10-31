const $=id=>document.getElementById(id);
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const ago=t=>{const s=Math.max(0,(Date.now()-new Date(t))/1000|0);return s<60?s+'s':s<3600?(s/60|0)+'m':s<86400?(s/3600|0)+'h':(s/86400|0)+'d'};
const bytes=n=>n<1024?n+' B':n<1048576?(n/1024).toFixed(1)+' KiB':n<1073741824?(n/1048576).toFixed(1)+' MiB':(n/1073741824).toFixed(2)+' GiB';
function toast(t,bad){const m=$('msg');m.textContent=t;m.style.background=bad?'var(--bad)':'';m.style.display='block';clearTimeout(m._t);m._t=setTimeout(()=>m.style.display='none',bad?6000:3000)}

// The console's Content-Security-Policy refuses inline scripts and
// handlers, so elements name the function a click, submit or change calls
// in attributes: on(fn, ...args) is the attributes of an element whose
// click calls window[fn](...args), with the element as this; a form's
// data-submit and a field's data-change name the function called with the
// event. The functions are declared with function, which makes them the
// window's.
const on=(fn,...a)=>`data-click="${esc(fn)}" data-args="${esc(JSON.stringify(a))}"`;
const handler=name=>typeof window[name]==='function'?window[name]:null;
document.addEventListener('click',e=>{const el=e.target.closest('[data-click]');if(!el)return;const f=handler(el.dataset.click);if(!f)return;
 let a=[];try{a=JSON.parse(el.dataset.args||'[]')}catch(_){}f.apply(el,a)});
document.addEventListener('submit',e=>{const f=handler(e.target.dataset.submit||'');if(f)f.call(e.target,e);else e.preventDefault()});
document.addEventListener('change',e=>{const el=e.target.closest('[data-change]');const f=el&&handler(el.dataset.change);if(f)f.call(el,e)});
// removeRow removes the table row of the button clicked.
function removeRow(){this.closest('tr').remove()}

// Logins: the pages are behind the console's, a session cookie and a CSRF
// token that every request but a GET sends in X-Fit-Csrf. A request that
// finds no session shows the login form; a page's onLogin runs once the
// user is in, and its refresh only while one is.
let csrf='';
async function api(path,body){const post=body!==undefined,headers={};
 if(post){headers['Content-Type']='application/json';headers['X-Fit-Csrf']=csrf}
 const r=await fetch(path,{method:post?'POST':'GET',headers,body:post?JSON.stringify(body):undefined,credentials:'same-origin'});
 const j=await r.json().catch(()=>({}));
 if(r.status===401){csrf='';showLogin();throw new Error(j.error||'log in first')}
 if(!r.ok)throw new Error(j.error||r.statusText);
 return j}
// call posts body to path, and says ok, or the config's warnings, or why it
// failed; then it loads the page again. It reports whether it worked.
async function call(path,body,ok){try{const j=await api(path,body);
  if(j&&j.warnings&&j.warnings.length)toast(j.warnings.join('\n'),true);else toast(ok);
  if(typeof load==='function')setTimeout(load,200);return true}catch(e){toast(e.message,true);return false}}
function showLogin(){let d=$('loginDlg');
 if(!d){d=document.createElement('dialog');d.id='loginDlg';
  d.innerHTML=`<form class="stack" data-submit="logIn"><h3>Log in</h3><label><span>User</span><input name="username" autocomplete="username" required></label>
  <label><span>Password</span><input name="password" type="password" autocomplete="current-password" required></label><div class="end"><button class="primary">Log in</button></div></form>`;
  d.addEventListener('cancel',e=>e.preventDefault());document.body.appendChild(d)}
 if(!d.open)d.showModal()}
async function logIn(e){e.preventDefault();const f=e.target;
 try{const r=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({username:f.username.value,password:f.password.value})});
  const j=await r.json().catch(()=>({}));if(!r.ok)throw new Error(j.error||r.statusText);
  csrf=j.csrf;f.reset();$('loginDlg').close();if(typeof onLogin==='function')onLogin()}catch(err){toast(err.message,true)}}
async function logOut(){try{await fetch('/api/logout',{method:'POST',headers:{'X-Fit-Csrf':csrf},credentials:'same-origin'})}catch(_){}csrf='';showLogin()}
// boot asks whether a session is open, and runs onLogin if one is.
async function boot(){try{const r=await fetch('/api/session',{credentials:'same-origin'});
  if(r.ok){csrf=(await r.json()).csrf;if(typeof onLogin==='function')onLogin();return}}catch(_){}
 showLogin()}
// every runs fn every ms while a user is logged in.
function every(fn,ms){setInterval(()=>{if(csrf)fn()},ms)}
document.addEventListener('DOMContentLoaded',()=>{const h=document.querySelector('header');if(!h)return;
 const b=document.createElement('button');b.className='logout';b.textContent='Log out';b.dataset.click='logOut';h.appendChild(b);boot()});

// Round trips, as latency.Stats ({last, avg, min, loss, samples}, in
// milliseconds). ms is one as text; rttGrade rates a link good, fair or poor
// by its average and loss; rttText is a link as text, '' before it is
// measured; rtt is a link as a cell's HTML, its details in the tooltip;
// rttSum is the stats of a path made of links one after the other, unknown
// if any of them is.
const ms=v=>v<1?v.toFixed(2)+' ms':v<10?v.toFixed(1)+' ms':Math.round(v)+' ms';
const rttGrade=s=>s.loss>=.2||s.avg>=150?'poor':s.loss>=.05||s.avg>=50?'fair':'good';
const rttLoss=s=>s.loss>=.01?Math.round(s.loss*100)+'% loss':'';
const rttText=s=>s&&s.samples?ms(s.avg)+(rttLoss(s)?' · '+rttLoss(s):''):'';
function rtt(s,tip){if(!s||!s.samples)return '<span class="muted">—</span>';
 return `<span class="rtt rtt-${rttGrade(s)}" title="${esc(tip||`last ${ms(s.last)} · min ${ms(s.min)} · avg ${ms(s.avg)}`)}">${ms(s.avg)}</span>${rttLoss(s)?` <span class="muted">${rttLoss(s)}</span>`:''}`}
function rttSum(...ls){if(!ls.length||ls.some(s=>!s||!s.samples))return null;
 return {last:ls.reduce((a,s)=>a+s.last,0),avg:ls.reduce((a,s)=>a+s.avg,0),min:ls.reduce((a,s)=>a+s.min,0),loss:1-ls.reduce((a,s)=>a*(1-s.loss),1),samples:Math.min(...ls.map(s=>s.samples))}}
function table(head,rows){return rows.length?`<table><thead><tr>${head.map(h=>`<th>${h}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table>`:'<div class="empty">None</div>'}
// serviceRow adds an editable row of service s to tbody, with extra, if
// given, as the HTML of a cell before the row's ✕.
function serviceRow(tbody,s={},extra){const tr=document.createElement('tr');tr.innerHTML=`<td><input placeholder="name" value="${esc(s.name)}"></td><td><input placeholder="10.0.0.5:22" value="${esc(s.address)}"></td><td><input placeholder="optional" value="${esc(s.description)}"></td>${extra===undefined?'':`<td>${extra}</td>`}<td><button class="danger" ${on('removeRow')}>✕</button></td>`;tbody.appendChild(tr)}
function readServices(tbody){return [...tbody.querySelectorAll('tr')].map(tr=>{const i=tr.querySelectorAll('input');return{name:i[0].value.trim(),address:i[1].value.trim(),description:i[2].value.trim()}}).filter(s=>s.name||s.address)}
function stateCell(s){return `<span class="state-${esc(s.state)}">${esc(s.state)}</span>${s.error?`<div class="muted">${esc(s.error)}</div>`:''}`}
const js=v=>esc(JSON.stringify(v));
// pageURL is the http URL of a listen address; one on every address is
// reached at the host this page came from.
function pageURL(addr){const m=String(addr).match(/^(.*):(\d+)$/);let h=(m?m[1]:addr).replace(/^\[|\]$/g,'');
 if(!h||h==='0.0.0.0'||h==='::')h=location.hostname.replace(/^\[|\]$/g,'');return 'http://'+(h.includes(':')?'['+h+']':h)+(m?':'+m[2]:'')}
// openPage opens the page at addr in a new tab, where it asks for its own
// login.
function openPage(addr){window.open(pageURL(addr)+'/','_blank','noopener')}
