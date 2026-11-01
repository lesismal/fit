// The proxy's page: its hubs, the nodes they have, its forwards, web
// proxies and tunnels. What it changes is written into tunnel.proxy in
// fit's config.
let state={hubs:[],forwards:[],web_proxies:[],tunnels:[]},metaLoaded=false,editing=null;
function render(){
 const st=state;$('name').textContent=st.name;$('meta').textContent=`${st.version} · ${st.open}/${st.max_tunnels} tunnels`;
 if(!metaLoaded){$('metaText').value=Object.entries(st.meta||{}).map(([k,v])=>k+'='+v).join(', ');metaLoaded=true}
 // Each hub's card has its console, through its web proxy; a web proxy
 // that is no hub's gets a card of its own.
 const lone=st.web_proxies.filter(w=>!st.hubs.some(s=>proxyOf(s)===w));
 const cards=st.hubs.map(s=>hubCard(s.name,s.addr,stateCell(s)+linkProtos(s),[['Protocols',(s.endpoints||[]).filter(e=>e.enabled).map(e=>PROTO_NAMES[e.protocol]||e.protocol).join(' → ')],['Hub',s.hub],['Latency',rttText(s.rtt)],['Peer ID',s.peer_id||''],['Links',`${s.pool}/${s.pool_size}`],['Nodes',s.nodes.length],['Tunnels',s.tunnels],
   ['Body encryption',s.body_encrypted?'AES-256-GCM':'none']],proxyOf(s),`<button ${on('refreshNodes',s.name)}>Query nodes</button><button ${on('editHub',s.name)}>Edit</button><button class="danger" ${on('delHub',s.name)}>Remove</button>`))
  .concat(lone.map(w=>hubCard(w.name||w.url,'','<span class="muted">web proxy only</span>',[],w,'')));
 $('hubs').innerHTML=cards.length?`<div class="cards">${cards.join('')}</div>`:'<div class="empty">None</div>';
 const rows=[];st.hubs.forEach(s=>s.nodes.forEach(p=>rows.push(`<tr><td>${esc(s.name)}</td><td>${esc(p.name)}<div class="muted">${esc(p.hostname)} ${esc(p.addr)}</div></td>
  <td>${pathRTT(s,p)}</td><td>${Math.round(p.share*100)}%</td>
  <td>${(p.services||[]).map(n=>`<div class="node">${esc(n.name)} → ${esc(n.address)}${n.description?` <span class="muted">${esc(n.description)}</span>`:''}${p.service_rtt&&p.service_rtt[n.name]?' · '+rtt(p.service_rtt[n.name]):''}
   <button ${on('useService',s.name,p.name,n.name)}>Forward…</button></div>`).join('')||'<span class="muted">none</span>'}</td>
  <td><button ${on('edit',s.name,p.name)}>Edit services</button></td></tr>`)));
 $('nodes').innerHTML=table(['Hub','Node','Latency','Share','Services',''],rows);
 $('forwards').innerHTML=table(['Listen','Node','Service','Hub','State',''],st.forwards.map(f=>`<tr><td>${esc(f.addr||f.listen)}</td><td>${esc(f.node)}</td><td>${esc(f.service)}</td>
  <td>${esc(f.hub||'any')}</td><td>${f.error?`<span class="state-failed">${esc(f.error)}</span>`:'<span class="state-registered">listening</span>'}</td>
  <td>${f.error?'':`<button ${on('openPage',f.addr||f.listen)}>Open</button>`}<button class="danger" ${on('delForward',f.listen)}>Remove</button></td></tr>`));
 $('tunnels').innerHTML=table(['Hub','ID','Listen','Node','Service','Address','Latency','Local proxy','Sent','Received','Age',''],st.tunnels.map(t=>`<tr><td>${esc(t.hub)}</td><td>${t.id}</td><td>${esc(t.peer)}</td>
  <td>${esc(t.node)}</td><td>${esc(t.service)}</td><td>${esc(t.addr)}</td><td>${tunnelRTT(t)}</td><td>${esc(t.remote)}</td><td>${bytes(t.out)}</td><td>${bytes(t.in)}</td><td>${ago(t.created)}</td>
  <td><button ${on('openFlow',t.id)}>Details</button><button class="danger" ${on('closeTunnel',t.hub,t.id)}>Close</button></td></tr>`));
 wbBadge('hubs',st.hubs.length);wbBadge('nodes',rows.length);wbBadge('forwards',st.forwards.length);wbBadge('tunnels',st.tunnels.length);
 flowRefresh();
}
// pathRTT is the latency to node p through hub s: proxy to hub to
// node, the parts in the tooltip.
function pathRTT(s,p){const sum=rttSum(s.rtt,p.rtt);return sum?rtt(sum,`proxy ↔ ${s.name} ${rttText(s.rtt)} · ${s.name} ↔ ${p.name} ${rttText(p.rtt)}`):rtt(null)}
// tunnelRTT is a tunnel's latency: its path, and the node's connects to
// the service.
function tunnelRTT(t){const s=state.hubs.find(s=>s.name===t.hub),p=s&&s.nodes.find(p=>p.name===t.node),n=p&&p.service_rtt&&p.service_rtt[t.service];
 if(!p)return rtt(null);const sum=rttSum(s.rtt,p.rtt);if(!sum)return rtt(null);
 return rtt(n&&n.samples?rttSum(s.rtt,p.rtt,n):sum,`proxy ↔ ${s.name} ${rttText(s.rtt)} · ${s.name} ↔ ${t.node} ${rttText(p.rtt)}${n&&n.samples?` · ${t.node} → ${t.service} ${rttText(n)}`:''}`)}
const nodeVia=(hub,node)=>{const s=state.hubs.find(s=>s.name===hub);return s&&s.nodes.find(p=>p.name===node)};
// The tunnel diagrams: this proxy, its forwards, hubs, nodes and their
// services. A proxy's tunnel end sends the local app's bytes (out) up.
const tunnelOf=t=>({id:t.id,name:`${t.peer} → ${t.node}/${t.service}`,up:t.out,down:t.in,created:t.created});
const transport=a=>'link over '+(PROTO_NAMES[protoOf(a)]||protoOf(a));
function flowMe(g,col){const st=state;return g.node('me',col,{kind:'proxy',label:st.name,sub:`${st.forwards.length} forwards`,self:true,state:'ok',
 facts:[['Hubs',st.hubs.length],['Forwards',st.forwards.length],['Version',st.version]]})}
function flowForward(g,col,listen){const f=state.forwards.find(f=>f.listen===listen)||{listen};
 return g.node('f:'+listen,col,{kind:'forward',label:f.addr||listen,sub:f.node?`→ ${f.node}/${f.service}`:'',state:f.error?'bad':'ok',
  facts:[['Listen',f.addr||listen],['Node',f.node],['Service',f.service],['Hub',f.hub||'any'],['State',f.error||'listening']]})}
function flowHub(g,col,name){const s=state.hubs.find(s=>s.name===name);
 return g.node('s:'+name,col,{kind:'hub',label:name,sub:s?[rttText(s.rtt),s.addr].filter(Boolean).join(' · '):'',state:s?flowState(s.state):'wait',
  facts:s?[['Address',s.addr],['State',s.error||s.state],['Latency',rttText(s.rtt)],['Protocol',transport(s.addr)],['Body encryption',s.body_encrypted?'AES-256-GCM':'none'],['Peer ID',s.peer_id||''],['Links',`${s.pool}/${s.pool_size}`],['Tunnels',s.tunnels]]:[]})}
function nodeOf(name){for(const s of state.hubs)for(const p of s.nodes)if(p.name===name)return p}
function flowNode(g,col,name){const p=nodeOf(name);
 return g.node('p:'+name,col,{kind:'pod',label:name,sub:p?p.hostname:'',state:p?'ok':'wait',
  facts:p?[['Address',p.addr],['Host',p.hostname],['OS',p.os],['Version',p.version]]:[]})}
function flowService(g,col,node,name){const p=nodeOf(node),n=p&&(p.services||[]).find(n=>n.name===name)||{name};
 return g.node(`n:${node}/${name}`,col,{kind:'node',icon:flowNodeIcon(name),label:name,sub:n.address||'',facts:[['Node',node],['Address',n.address],['Description',n.description],['Connect',rttText(p&&p.service_rtt&&p.service_rtt[name])]]})}
function flowAll(){const st=state,g=flowGraph(['Proxy','Forwards','Hubs','Nodes','Services']);flowMe(g,0);
 st.forwards.forEach(f=>{flowForward(g,1,f.listen);g.link('me','f:'+f.listen)});
 st.hubs.forEach(s=>{flowHub(g,2,s.name);s.nodes.forEach(p=>{flowNode(g,3,p.name);g.link('s:'+s.name,'p:'+p.name,{rtt:p.rtt});
  (p.services||[]).forEach(n=>{flowService(g,4,p.name,n.name);g.link('p:'+p.name,`n:${p.name}/${n.name}`,{rtt:p.service_rtt&&p.service_rtt[n.name]})})})});
 // A forward may take any hub that has its node. The latency to a
 // hub is on the hub, rather than on each of the links into it.
 st.forwards.forEach(f=>st.hubs.forEach(s=>{if((!f.hub||f.hub===s.name)&&s.nodes.some(p=>p.name===f.node))g.link('f:'+f.listen,'s:'+s.name,{rtt:s.rtt,quiet:true})}));
 st.tunnels.forEach(t=>{flowForward(g,1,t.peer);flowHub(g,2,t.hub);flowNode(g,3,t.node);flowService(g,4,t.node,t.service);
  g.tunnel(tunnelOf(t),['me','f:'+t.peer,'s:'+t.hub,'p:'+t.node,`n:${t.node}/${t.service}`])});
 return g}
function flowOne(id){const st=state,t=st.tunnels.find(t=>t.id===id);if(!t)return null;
 const g=flowGraph(['Local app','Proxy','Hub','Node','Service']),s=st.hubs.find(s=>s.name===t.hub)||{},enc=s.body_encrypted;
 g.node('app',0,{kind:'app',label:'Local app',sub:t.remote,state:'ok',facts:[['Address',t.remote]]});
 flowMe(g,1).facts.unshift(['Listen',t.local||t.peer]);g.byId.me.sub=t.local||t.peer;
 flowHub(g,2,t.hub);flowNode(g,3,t.node);flowService(g,4,t.node,t.service);const p=nodeVia(t.hub,t.node)||{};
 g.tunnel(tunnelOf(t),['app','me','s:'+t.hub,'p:'+t.node,`n:${t.node}/${t.service}`],[{label:'TCP'},{label:transport(s.addr||''),lock:enc,rtt:s.rtt},{label:'link',lock:enc,rtt:p.rtt},{label:'TCP',rtt:p.service_rtt&&p.service_rtt[t.service]}]);
 return g}
// hostOf is the host of a hub or web proxy address, host:port or a URL.
const hostOf=a=>{try{return new URL(String(a).includes('://')?a:'tcp://'+a).hostname}catch(e){return ''}};
// proxyOf is the web proxy that serves hub s's console: the one named
// after s, or else the one whose URL is on s's host.
function proxyOf(s){const ws=state.web_proxies||[];return ws.find(w=>w.name===s.name)||ws.find(w=>!w.name&&hostOf(w.url)&&hostOf(w.url)===hostOf(s.addr))}
// hubCard is a hub's card: its name, address and state, facts, the
// console that web proxy w serves (address and Open) and the hub's
// buttons.
function hubCard(name,addr,state,facts,w,buttons){const dl=fs=>`<dl class="facts">${fs.map(([k,v])=>`<dt>${esc(k)}</dt><dd class="raw">${esc(v)}</dd>`).join('')}</dl>`;
 let web='<div class="muted">no web proxy</div>';
 if(w){const a=w.addr||w.listen;
  web=w.error?`<div class="state-failed">${esc(w.error)}</div>`:`<dl class="facts"><dt>Address</dt><dd class="raw">${esc(pageURL(a))}</dd><dt>Via</dt><dd class="raw">${esc(w.url)}</dd><dt>Connections</dt><dd class="raw">${w.conns}</dd></dl>
   <div class="card-btns"><button class="primary" ${on('openPage',a)}>Open</button></div>`}
 return `<div class="card"><div class="card-head"><span class="card-ico">${svgIcon(FLOW_ICONS.hub)}</span><div class="card-name"><b class="raw">${esc(name)}</b><div class="muted raw">${esc(addr)}</div></div><div class="card-state">${state}</div></div>
  ${facts.length?dl(facts):''}<div class="card-web"><h4>Console</h4>${web}</div>${buttons?`<div class="card-btns end">${buttons}</div>`:''}</div>`}
const svgIcon=p=>`<svg viewBox="0 0 24 24" aria-hidden="true">${p}</svg>`;
async function load(){try{state=await api('/api/status');render()}catch(e){toast(e.message,true)}}
function onLogin(){load()}
function refreshNodes(hub){call('/api/nodes/refresh',{hub},'Nodes queried')}
function useService(hub,node,service){wbShow('forwards');$('fHub').value=hub;$('fNode').value=node;$('fService').value=service;$('fListen').focus()}
async function addForward(e){e.preventDefault();if(await call('/api/forwards',{listen:$('fListen').value.trim(),node:$('fNode').value.trim(),service:$('fService').value.trim(),hub:$('fHub').value.trim()},'Forward added'))e.target.reset()}
function delForward(listen){call('/api/forwards/delete',{listen},'Forward removed')}
function closeTunnel(hub,id){call('/api/tunnels/close',{hub,id},'Tunnel closed')}
function saveMeta(e){e.preventDefault();const meta={};$('metaText').value.split(',').map(s=>s.trim()).filter(Boolean).forEach(kv=>{const i=kv.indexOf('=');if(i>0)meta[kv.slice(0,i).trim()]=kv.slice(i+1).trim()});call('/api/meta',meta,'Info sent to hubs')}
function addRow(){serviceRow($('edRows'),{})}
function edit(hub,node){const p=nodeVia(hub,node);if(!p)return;editing={hub,node};
 $('edTitle').textContent=`Services of node ${node} (via ${hub})`;$('edRows').innerHTML='';(p.services||[]).forEach(s=>serviceRow($('edRows'),s));if(!(p.services||[]).length)addRow();$('editor').showModal()}
async function saveServices(){if(await call('/api/nodes/services',{...editing,services:readServices($('edRows'))},'Services saved on the node'))$('editor').close()}
every(()=>{if(!$('editor').open&&!($('srvEd')&&$('srvEd').open))load()},3000);
