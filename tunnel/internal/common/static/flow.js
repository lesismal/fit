// flow.js draws tunnels as a diagram in a dialog. openFlow() shows every
// open tunnel across the page's columns (clients, servers, nodes, services),
// over the control links (dashed) that carry them; openFlow(id) shows one
// tunnel's whole path, from the local app to the service. A page defines
// flowAll(), the graph of its tunnels, and flowOne(id), the graph of
// tunnel id or null once it is closed, both made with flowGraph, and calls
// flowRefresh() after every render so an open diagram stays current.
// Clicking a tunnel on the map (a link only it runs over, or its row in the
// side list) shows its path in place of the map; closing the path (✕, Esc
// or outside the dialog) shows the map again, as it was. A link given an
// rtt (latency.Stats) shows its latency, colored good, fair or poor, on the
// diagram unless it is quiet, and on the side and in its tooltip anyway.

const FLOW_ICONS={
 app:'<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 8.5h18M7 12.5l2.5 2-2.5 2M12 16.5h4.5"/>',
 client:'<rect x="4" y="5" width="16" height="11" rx="1.5"/><path d="M2 19.5h20M10 8.5l-2 2 2 2M14 8.5l2 2-2 2"/>',
 forward:'<path d="M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4"/>',
 server:'<circle cx="12" cy="5.5" r="2.5"/><circle cx="5" cy="18" r="2.5"/><circle cx="19" cy="18" r="2.5"/><path d="M10.8 7.7 6.2 15.8M13.2 7.7l4.6 8.1M7.5 18h9"/>',
 pod:'<path d="M12 2.8 20 7.3v9.4L12 21.2 4 16.7V7.3z"/><path d="M4 7.3 12 12l8-4.7M12 12v9.2"/>',
 node:'<rect x="6" y="6" width="12" height="12" rx="2"/><rect x="9.5" y="9.5" width="5" height="5" rx="1"/><path d="M9 2.5V6M15 2.5V6M9 18v3.5M15 18v3.5M2.5 9H6M2.5 15H6M18 9h3.5M18 15h3.5"/>',
 web:'<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.6 2.6 3.9 5.6 3.9 9s-1.3 6.4-3.9 9c-2.6-2.6-3.9-5.6-3.9-9S9.4 5.6 12 3z"/>',
 ssh:'<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9.5l3 2.5-3 2.5M12.5 15H17"/>',
 lock:'<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>'};
// The flow kinds: a tunnel node has the kind pod, and a service the kind
// node.
const FLOW_KINDS={app:'Local app',client:'Client',forward:'Forward',server:'Server',pod:'Node',node:'Service'};
// flowNodeIcon picks a service's icon by its name.
const flowNodeIcon=name=>/ssh|shell|term/i.test(name)?'ssh':/web|http|page|ui/i.test(name)?'web':'node';
const flowState=s=>s==='registered'?'ok':s==='failed'?'bad':s?'wait':'';

// flowGraph is an empty graph with columns cols. node adds a node to a
// column once (later calls return it as it is); link adds a control link;
// tunnel runs tunnel t ({id, name, up, down, created}) along the nodes of
// path, making their links tunnel links, with labels[i] on link i.
function flowGraph(cols){const g={cols,nodes:[],links:[],tunnels:{},byId:{}};
 g.node=(id,col,p)=>g.byId[id]||(g.nodes.push(g.byId[id]={id,col,kind:'node',label:id,sub:'',facts:[],tunnels:new Set(),...p}),g.byId[id]);
 g.link=(from,to,p)=>{const key=from+'\n'+to;let l=g.links.find(l=>l.key===key);
  if(!l)g.links.push(l={key,from,to,tunnels:new Set(),up:0,down:0,dashed:true});return Object.assign(l,p)};
 g.tunnel=(t,path,labels)=>{g.tunnels[t.id]=t;path.forEach(id=>g.byId[id]&&g.byId[id].tunnels.add(t.id));
  for(let i=0;i+1<path.length;i++){const l=g.link(path[i],path[i+1],labels&&labels[i]);l.dashed=false;l.tunnels.add(t.id);l.up+=t.up;l.down+=t.down}};
 return g}

let flowView=null,flowSel=null,flowBack=false,flowMap=null;
function flowDialog(){let d=$('flowDlg');if(d)return d;
 d=document.createElement('dialog');d.id='flowDlg';d.className='flow-dlg';
 d.innerHTML=`<div class="flow-head"><button class="flow-back" data-click="openFlow">← All tunnels</button><div class="flow-title"><h3></h3><div class="muted"></div></div>
  <div class="flow-legend"><span><i class="lg-line"></i>Tunnel</span><span><i class="lg-dash"></i>Control link</span><span>Traffic</span><span class="lg-heat"><i class="h1"></i><i class="h2"></i><i class="h3"></i></span><span>low → high</span></div>
  <button class="flow-x" data-click="flowClose" aria-label="Close">✕</button></div>
  <div class="flow-chips"></div><div class="flow-body"><div class="flow-canvas"></div><aside class="flow-side"></aside></div>`;
 d.addEventListener('click',e=>{if(e.target===d)flowClose()});
 d.addEventListener('cancel',e=>{if(flowBack){e.preventDefault();openFlow()}});
 document.body.appendChild(d);return d}
// openFlow shows every tunnel, or tunnel id's path. A path opened from the
// map keeps what the map had selected and where it was scrolled, for when
// the map comes back.
function openFlow(id){const d=flowDialog(),fromMap=d.open&&flowView&&flowView.id===undefined,c=d.querySelector('.flow-canvas');
 if(id!==undefined&&fromMap)flowMap={sel:flowSel,top:c.scrollTop,left:c.scrollLeft};
 const back=id===undefined&&flowBack&&flowMap;flowBack=id!==undefined&&fromMap;
 flowView={id};flowSel=back?flowMap.sel:null;flowRender();
 if(back){c.scrollTop=flowMap.top;c.scrollLeft=flowMap.left}else c.scrollTop=c.scrollLeft=0;
 if(!d.open)d.showModal()}
// flowClose goes back to the map from a path opened on it, and otherwise
// closes the dialog.
function flowClose(){if(flowBack)openFlow();else $('flowDlg').close()}
function flowRefresh(){const d=$('flowDlg');if(d&&d.open)flowRender()}

function flowRender(){const d=$('flowDlg'),one=flowView.id!==undefined,g=one?flowOne(flowView.id):flowAll();
 d.querySelector('.flow-back').style.display=one&&flowBack?'':'none';
 d.querySelector('h3').textContent=one?'Tunnel path':'Tunnel map';
 if(!g){d.querySelector('.flow-title .muted').textContent='';d.querySelector('.flow-chips').innerHTML='';d.querySelector('.flow-side').innerHTML='';
  d.querySelector('.flow-canvas').innerHTML='<div class="empty">This tunnel is closed.</div>';return}
 const ts=Object.values(g.tunnels),up=ts.reduce((a,t)=>a+t.up,0),down=ts.reduce((a,t)=>a+t.down,0),t=ts[0];
 d.querySelector('.flow-title .muted').textContent=one?t.name:g.cols.join(' → ');
 const chips=one?[['Tunnel ID',t.id],['Age',ago(t.created)],['Up',bytes(t.up)],['Down',bytes(t.down)]]:[['Tunnels',ts.length],['Up',bytes(up)],['Down',bytes(down)]];
 d.querySelector('.flow-chips').innerHTML=chips.map(([k,v])=>`<span class="chip"><span class="muted">${esc(k)}</span><b>${esc(v)}</b></span>`).join('')+
  (!one&&!ts.length?'<span class="muted">No open tunnels; dashed lines are control links.</span>':'');
 g.chain=one;flowLayout(g);d.querySelector('.flow-canvas').innerHTML=flowSVG(g);flowStyle(d);flowWire(d,g);flowSide(d,g)}
// flowStyle sets the styles flowSVG names in data attributes: the
// Content-Security-Policy refuses style attributes in markup, but not styles
// set from a script.
function flowStyle(d){d.querySelectorAll('[data-delay]').forEach(e=>e.style.animationDelay=e.dataset.delay);
 d.querySelectorAll('[data-minw]').forEach(e=>e.style.minWidth=e.dataset.minw+'px')}

// flowLayout orders each column to keep links from crossing (a few sweeps
// of the barycenter heuristic) and gives every node its x and y.
function flowLayout(g){const n=g.cols.length,cols=g.cols.map((_,i)=>g.nodes.filter(x=>x.col===i)),pos={};
 const place=c=>c.forEach((x,i)=>pos[x.id]=(i+.5)/c.length);cols.forEach(place);
 const sweep=(ci,back)=>{const c=cols[ci],b={};
  c.forEach(x=>{const ns=g.links.filter(l=>back?l.from===x.id:l.to===x.id).map(l=>pos[back?l.to:l.from]).filter(v=>v!==undefined);
   b[x.id]=ns.length?ns.reduce((a,v)=>a+v,0)/ns.length:pos[x.id]});
  c.sort((p,q)=>b[p.id]-b[q.id]);place(c)};
 for(let it=0;it<3;it++){for(let i=1;i<n;i++)sweep(i,false);for(let i=n-2;i>=0;i--)sweep(i,true)}
 const rowH=g.chain?170:96,maxK=Math.max(1,...cols.map(c=>c.length));
 g.W=Math.max(820,n*200);g.H=64+maxK*rowH+(g.chain?20:6);const pad=g.chain?100:90;
 cols.forEach((c,ci)=>c.forEach((x,i)=>{x.x=n===1?g.W/2:pad+ci*(g.W-2*pad)/(n-1);x.y=64+(maxK-c.length)*rowH/2+i*rowH+(g.chain?66:30)}))}

const flowCut=(s,n)=>(s=String(s??'')).length>n?s.slice(0,n-1)+'…':s;
function flowSVG(g){const theme=document.documentElement.dataset.theme,rx=theme==='mecha'?3:theme==='ghibli'?15:12;
 const T=g.chain?54:44,max=Math.max(1,...g.links.map(l=>g.chain?Math.max(l.up,l.down):l.up+l.down)),phase=`-${(performance.now()/1000%1.4).toFixed(2)}s`;
 const heat=l=>{const r=Math.log1p(l.up+l.down)/Math.log1p(max);return max<1024?1:r<.45?1:r<.8?2:3};
 const head=g.cols.map((c,i)=>{const x=g.nodes.find(x=>x.col===i);return x?`<text class="colh" x="${x.x}" y="26" text-anchor="middle">${esc(c)}</text>`:''}).join('');
 const links=g.links.map(l=>{const a=g.byId[l.from],b=g.byId[l.to];if(!a||!b)return '';
  const x1=a.x+T/2+8,x2=b.x-T/2-8,dx=(x2-x1)*.5,curve=o=>`M${x1} ${a.y+o}C${x1+dx} ${a.y+o},${x2-dx} ${b.y+o},${x2} ${b.y+o}`;
  const h=heat(l),w=l.dashed?1.4:(g.chain?2.6:1.8+2.4*Math.log1p(l.up+l.down)/Math.log1p(max));
  let paths;
  if(l.dashed)paths=`<path class="lk-base" d="${curve(0)}" stroke-width="${w}"/>`;
  // A tunnel's path has a lane each way, up above and down below, each colored by its own bytes.
  else if(g.chain)paths=[[-6,l.up,''],[6,l.down,' rev']].map(([o,n,rev])=>`<g class="h${heat({up:n,down:0})}"><path class="lk-base" d="${curve(o)}" stroke-width="${w}"/><path class="lk-flow${rev}" d="${curve(o)}" stroke-width="${w}" data-delay="${phase}"/></g>`).join('');
  else paths=`<path class="lk-base" d="${curve(0)}" stroke-width="${w}"/><path class="lk-flow" d="${curve(0)}" stroke-width="${w}" data-delay="${phase}"/>`;
  const mx=(x1+x2)/2,my=(a.y+b.y)/2,lat=l.rtt&&l.rtt.samples&&!l.quiet?`<text class="lk-rtt rtt-${rttGrade(l.rtt)}" x="${mx}" y="${my+(g.chain?44:14)}" text-anchor="middle">${esc(rttText(l.rtt))}</text>`:'',label=l.label?`<g class="lk-label" transform="translate(${mx} ${my-(g.chain?36:14)})"><text text-anchor="middle">${esc(l.label)}</text>${l.lock?`<svg class="ico" x="${l.label.length*3.3+3}" y="-10" width="11" height="11" viewBox="0 0 24 24">${FLOW_ICONS.lock}</svg>`:''}</g>`+
   (g.chain&&!l.dashed?`<text class="lk-bytes" x="${mx}" y="${my-14}" text-anchor="middle">↑ ${bytes(l.up)}</text><text class="lk-bytes" x="${mx}" y="${my+24}" text-anchor="middle">↓ ${bytes(l.down)}</text>`:''):'';
  return `<g class="lk h${h}${l.dashed?' dash':''}" data-k="${esc(l.key)}"><title>${esc(a.label)} → ${esc(b.label)}${l.rtt&&l.rtt.samples?' · '+esc(rttText(l.rtt)):''}${l.dashed?'':` · ${l.tunnels.size} · ↑ ${bytes(l.up)} ↓ ${bytes(l.down)}`}</title>
   <path class="lk-hit" d="${curve(0)}"/>${paths}${label}${lat}</g>`}).join('');
 const nodes=g.nodes.map(x=>{const s=x.self?T+8:T,badge=x.state?`<g class="bdg ${x.state}" transform="translate(${x.x+s/2-3} ${x.y+s/2-3})"><circle r="7"/>${
   x.state==='ok'?'<path d="M-3 0l2 2 4-4"/>':x.state==='bad'?'<path d="M0-3.5v4M0 2.8v.4"/>':'<path d="M-3 0h6"/>'}</g>`:'';
  return `<g class="nd k-${x.kind}${x.self?' self':''}${x.unknown?' unknown':''}" data-id="${esc(x.id)}"><title>${esc(x.label)}${x.sub?'\n'+esc(x.sub):''}</title>
   ${x.self?`<text class="tag" x="${x.x}" y="${x.y-s/2-9}" text-anchor="middle">this page</text>`:''}
   <rect class="ring" x="${x.x-s/2-5}" y="${x.y-s/2-5}" width="${s+10}" height="${s+10}" rx="${rx+4}"/>
   <rect class="tile" x="${x.x-s/2}" y="${x.y-s/2}" width="${s}" height="${s}" rx="${rx}"/>
   <svg class="ico" x="${x.x-s*.27}" y="${x.y-s*.27}" width="${s*.54}" height="${s*.54}" viewBox="0 0 24 24">${FLOW_ICONS[x.icon||x.kind]||FLOW_ICONS.node}</svg>${badge}
   <text class="lb" x="${x.x}" y="${x.y+s/2+18}" text-anchor="middle">${esc(flowCut(x.label,24))}</text>
   ${x.sub?`<text class="sb" x="${x.x}" y="${x.y+s/2+33}" text-anchor="middle">${esc(flowCut(x.sub,30))}</text>`:''}</g>`}).join('');
 return `<svg class="flow-svg${g.chain?' chain':''}" viewBox="0 0 ${g.W} ${g.H}" data-minw="${Math.round(g.W*.72)}">${head}<g>${links}</g><g>${nodes}</g></svg>`}

// flowWire makes hovering a node or link light up the tunnels through it,
// and clicking one show it on the side.
function flowFocus(svg,g,ts,ids){svg.classList.add('focus');
 svg.querySelectorAll('.lk').forEach(e=>{const l=g.links.find(l=>l.key===e.dataset.k);e.classList.toggle('hi',[...l.tunnels].some(t=>ts.has(t))||l.dashed&&(ids.has(l.from)&&ids.has(l.to)))});
 svg.querySelectorAll('.nd').forEach(e=>{const x=g.byId[e.dataset.id];e.classList.toggle('hi',ids.has(x.id)||[...x.tunnels].some(t=>ts.has(t)))})}
function flowWire(d,g){const svg=d.querySelector('.flow-svg'),focus=(ts,ids)=>flowFocus(svg,g,ts,ids);
 svg.querySelectorAll('.nd').forEach(e=>{const x=g.byId[e.dataset.id];
  e.onmouseenter=()=>{const ids=new Set([x.id]);g.links.forEach(l=>{if(l.dashed&&(l.from===x.id||l.to===x.id)){ids.add(l.from);ids.add(l.to)}});focus(x.tunnels,ids)};
  e.onclick=()=>{flowSel={node:x.id};flowSide(d,g)}});
 svg.querySelectorAll('.lk').forEach(e=>{const l=g.links.find(l=>l.key===e.dataset.k);
  // On the map, a link that one tunnel alone runs over opens that tunnel.
  const only=!g.chain&&!l.dashed&&l.tunnels.size===1?[...l.tunnels][0]:undefined;if(only!==undefined)e.classList.add('one');
  e.onmouseenter=()=>focus(l.tunnels,new Set([l.from,l.to]));e.onclick=()=>{if(only!==undefined){openFlow(only);return}flowSel={link:l.key};flowSide(d,g)}});
 svg.querySelectorAll('.nd,.lk').forEach(e=>e.onmouseleave=()=>svg.classList.remove('focus'))}

function flowSide(d,g){const svg=d.querySelector('.flow-svg');
 let x=flowSel&&flowSel.node&&g.byId[flowSel.node],l=flowSel&&flowSel.link&&g.links.find(l=>l.key===flowSel.link);
 if(!x&&!l)x=g.nodes.find(x=>x.self)||g.nodes[0];
 svg.querySelectorAll('.nd').forEach(e=>e.classList.toggle('sel',!!x&&e.dataset.id===x.id));
 svg.querySelectorAll('.lk').forEach(e=>e.classList.toggle('sel',!!l&&e.dataset.k===l.key));
 const facts=fs=>`<dl>${fs.filter(f=>f[1]!==undefined&&f[1]!=='').map(([k,v])=>`<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl>`;
 const ts=[...(x?x.tunnels:l.tunnels)].map(id=>g.tunnels[id]);
 const list=g.chain?'':`<h4>${esc(`Tunnels (${ts.length})`)}</h4>`+(ts.length?ts.map(t=>`<div class="ft"><div><b>${esc(t.name)}</b><div class="muted">↑ ${bytes(t.up)} · ↓ ${bytes(t.down)} · ${ago(t.created)}</div></div>
  <button>Details</button></div>`).join(''):'<div class="muted">None</div>');
 // A row of the list lights up its tunnel on the map, and opens it.
 let html;
 if(x)html=`<div class="fs-head nd-ico k-${x.kind}"><svg viewBox="0 0 24 24">${FLOW_ICONS[x.icon||x.kind]||FLOW_ICONS.node}</svg><div><b>${esc(x.label)}</b><div class="muted">${esc(FLOW_KINDS[x.kind]||x.kind)}</div></div></div>
  ${x.note?`<p class="muted">${esc(x.note)}</p>`:''}<h4>Details</h4>${facts(x.facts)}${list}`;
 else{const a=g.byId[l.from],b=g.byId[l.to];
  html=`<div class="fs-head"><div><b>${esc(a.label)} → ${esc(b.label)}</b><div class="muted">${esc(l.dashed?'Control link':'Tunnel')}</div></div></div>
  <h4>Details</h4>${facts([['Protocol',l.label||''],['Latency',rttText(l.rtt)],['Tunnels',l.dashed?'':l.tunnels.size],['Up',l.dashed?'':bytes(l.up)],['Down',l.dashed?'':bytes(l.down)]])}${list}`}
 const side=d.querySelector('.flow-side');side.innerHTML=html;
 side.querySelectorAll('.ft').forEach((e,i)=>{const t=ts[i];
  e.onmouseenter=()=>flowFocus(svg,g,new Set([t.id]),new Set());e.onmouseleave=()=>svg.classList.remove('focus');e.onclick=()=>openFlow(t.id)})}
