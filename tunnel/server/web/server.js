// The server's page: its nodes, clients, tunnels, listeners and settings.
// What it changes is written into tunnel.server in fit's config.
let state={peers:[],tunnels:[],listeners:[]};
function render(){
 const st=state;$('name').textContent=st.name;
 $('meta').textContent=`${st.version} · peers ${(st.listen||[]).join(' ')||'none'}${st.socket?' · console socket '+st.socket:''}${st.body_encrypted?' · bodies encrypted':''} · up ${ago(st.started)}`;
 const nodes=st.peers.filter(p=>p.info.type==='node'),clients=st.peers.filter(p=>p.info.type==='client');
 const places=p=>esc((p.places||[]).map(i=>'#'+i).join(' '));
 $('nodes').innerHTML=table(['ID','Name','Address','Latency','Host','Services','Tunnels','Since',''],nodes.map(p=>`<tr><td>${p.id}</td><td>${esc(p.info.name)}</td><td>${esc(p.addr)}<div class="muted" title="${places(p)}">${p.conns} links</div></td><td>${rtt(p.rtt)}</td>
  <td>${esc(p.info.hostname)}<div class="muted">${esc(p.info.os)} ${esc(p.info.version)}</div></td>
  <td>${(p.info.services||[]).map(s=>`<div class="node">${esc(s.name)} → ${esc(s.address)}${s.description?` <span class="muted">${esc(s.description)}</span>`:''}${serviceRTT(p,s.name)?' · '+rtt(serviceRTT(p,s.name)):''}</div>`).join('')||'<span class="muted">none</span>'}</td>
  <td>${p.tunnels}</td><td>${ago(p.since)}</td>
  <td><button ${on('refresh',p.id)}>Query</button><button ${on('edit',p.id)}>Edit services</button><button class="danger" ${on('kick',p.id)}>Kick</button></td></tr>`));
 $('clients').innerHTML=table(['ID','Name','Address','Latency','Host','Info','Tunnels','Since',''],clients.map(p=>`<tr><td>${p.id}</td><td>${esc(p.info.name)}</td><td>${esc(p.addr)}<div class="muted" title="${places(p)}">${p.conns} links</div></td><td>${rtt(p.rtt)}</td>
  <td>${esc(p.info.hostname)}<div class="muted">${esc(p.info.os)} ${esc(p.info.version)}</div></td>
  <td>${Object.entries(p.info.meta||{}).map(([k,v])=>`<div class="node">${esc(k)}=${esc(v)}</div>`).join('')}</td>
  <td>${p.tunnels}</td><td>${ago(p.since)}</td><td><button class="danger" ${on('kick',p.id)}>Kick</button></td></tr>`));
 $('tunnels').innerHTML=table(['ID','Client','Node','Service','Address','Latency','Up','Down','Age',''],st.tunnels.map(t=>`<tr><td>${t.id}</td><td>${esc(t.client)}</td><td>${esc(t.node)}</td>
  <td>${esc(t.service)}</td><td>${esc(t.addr)}</td><td>${pathCell(t)}</td><td>${bytes(t.up)}</td><td>${bytes(t.down)}</td><td>${ago(t.created)}</td>
  <td><button ${on('openFlow',t.id)}>Details</button><button class="danger" ${on('closeTunnel',t.id)}>Close</button></td></tr>`));
 $('listeners').innerHTML=table(['Protocol','Address','Path','Peers dial',''],(st.listeners||[]).map(l=>{const k={protocol:l.protocol,address:l.address,path:l.path||''};
  return `<tr><td><span class="proto proto-${esc(l.protocol)}">${esc(PROTO_NAMES[l.protocol]||l.protocol)}</span></td><td class="node">${esc(l.address)}</td><td class="node">${esc(l.path||'')}</td>
  <td>${l.error?`<span class="state-failed">${esc(l.error)}</span>`:`<span class="node">${esc(l.url)}</span>`}</td>
  <td><button ${on('editListener',k)}>Edit</button><button class="danger" ${on('delListener',k)}>Remove</button></td></tr>`}));
 const set=st.settings||{},f=$('setForm');
 f.password.placeholder=set.has_password?'•••••••• leave blank to keep':'not set: peers cannot register';
 f.body.placeholder=set.has_body_password?'•••••••• leave blank to keep':'optional';f.clearBody.closest('label').hidden=!set.has_body_password;
 f.socket.placeholder=set.has_socket_password?'•••••••• leave blank to keep':'default: the registration password';f.clearSocket.closest('label').hidden=!set.has_socket_password;
 if(!setLoaded){f.socketAddr.value=set.socket_address||'';setLoaded=true}
 $('quicFp').innerHTML=st.quic_fingerprint?`<div class="stack-row"><span class="fp" id="fpText">${esc(st.quic_fingerprint)}</span> <button ${on('copyFp')}>Copy</button></div><p class="muted note">Nodes and clients pin this fingerprint for the server's QUIC listeners.</p>`:'<p class="muted note">Made, and written into the config, when a QUIC listener is added.</p>';
 wbBadge('nodes',nodes.length);wbBadge('clients',clients.length);wbBadge('tunnels',st.tunnels.length);wbBadge('listeners',(st.listeners||[]).length);
 flowRefresh();
}
// The tunnel diagrams: clients, this server, nodes and their services.
const peerOf=(type,name)=>state.peers.find(p=>p.info.type===type&&p.info.name===name);
const serviceRTT=(p,name)=>p&&p.service_rtt&&p.service_rtt[name];
const rttOf=(type,name)=>{const p=peerOf(type,name);return p&&p.rtt};
// pathCell is a tunnel's latency: client to server to node, and the node's
// connects to the service, the parts in the tooltip.
function pathCell(t){const c=rttOf('client',t.client),p=rttOf('node',t.node),n=serviceRTT(peerOf('node',t.node),t.service),sum=rttSum(c,p);
 return sum?rtt(n&&n.samples?rttSum(c,p,n):sum,`${t.client} ↔ server ${rttText(c)} · server ↔ ${t.node} ${rttText(p)}${n&&n.samples?` · ${t.node} → ${t.service} ${rttText(n)}`:''}`):rtt(null)}
const peerFacts=p=>p?[['Address',p.addr],['Latency',rttText(p.rtt)],['Host',p.info.hostname],['OS',p.info.os],['Version',p.info.version],['Peer ID',p.id],['Since',ago(p.since)]]:[];
const tunnelOf=t=>({id:t.id,name:`${t.client} → ${t.node}/${t.service}`,up:t.up,down:t.down,created:t.created});
function flowMe(g,col){const st=state;return g.node('me',col,{kind:'server',label:st.name,sub:(st.listen||[])[0]||'',self:true,state:'ok',
 facts:[['Peer addresses',(st.listen||[]).join(' ')],['Console socket',st.socket||''],['Body encryption',st.body_encrypted?'AES-256-GCM':'none'],['Version',st.version],['Uptime',ago(st.started)]]})}
function flowPeer(g,type,col,name){const p=peerOf(type,name);
 return g.node((type==='node'?'p:':'c:')+name,col,{kind:type==='node'?'pod':'client',label:name,sub:p?p.info.hostname||p.addr:'',state:p?'ok':'wait',facts:peerFacts(p)})}
function flowService(g,col,node,name){const p=peerOf('node',node),s=p&&(p.info.services||[]).find(s=>s.name===name)||{name};
 return g.node(`n:${node}/${name}`,col,{kind:'node',icon:flowNodeIcon(name),label:name,sub:s.address||'',facts:[['Node',node],['Address',s.address],['Description',s.description],['Connect',rttText(serviceRTT(p,name))]]})}
function flowAll(){const st=state,g=flowGraph(['Clients','Server','Nodes','Services']);flowMe(g,1);
 st.peers.forEach(p=>{if(p.info.type==='client'){flowPeer(g,'client',0,p.info.name);g.link('c:'+p.info.name,'me',{rtt:p.rtt})}});
 st.peers.forEach(p=>{if(p.info.type!=='node')return;flowPeer(g,'node',2,p.info.name);g.link('me','p:'+p.info.name,{rtt:p.rtt});
  (p.info.services||[]).forEach(s=>{flowService(g,3,p.info.name,s.name);g.link('p:'+p.info.name,`n:${p.info.name}/${s.name}`,{rtt:serviceRTT(p,s.name)})})});
 st.tunnels.forEach(t=>{flowPeer(g,'client',0,t.client);flowPeer(g,'node',2,t.node);flowService(g,3,t.node,t.service);
  g.tunnel(tunnelOf(t),['c:'+t.client,'me','p:'+t.node,`n:${t.node}/${t.service}`])});
 return g}
function flowOne(id){const st=state,t=st.tunnels.find(t=>t.id===id);if(!t)return null;
 const g=flowGraph(['Local app','Client','Server','Node','Service']),enc=st.body_encrypted;
 g.node('app',0,{kind:'app',label:'Local app',sub:'not visible here',unknown:true,note:'Only the client sees the local app.'});
 flowPeer(g,'client',1,t.client);flowMe(g,2);flowPeer(g,'node',3,t.node);flowService(g,4,t.node,t.service);
 g.tunnel(tunnelOf(t),['app','c:'+t.client,'me','p:'+t.node,`n:${t.node}/${t.service}`],[{label:'TCP'},{label:'link',lock:enc,rtt:rttOf('client',t.client)},{label:'link',lock:enc,rtt:rttOf('node',t.node)},{label:'TCP',rtt:serviceRTT(peerOf('node',t.node),t.service)}]);
 return g}
async function load(){try{state=await api('/api/status');render()}catch(e){toast(e.message,true)}}
function onLogin(){load()}
function refresh(id){call('/api/peers/refresh',{id},'Node queried')}
function kick(id){if(confirm('Kick this peer?'))call('/api/peers/kick',{id},'Kicked')}
function closeTunnel(id){call('/api/tunnels/close',{id},'Tunnel closed')}
let editing=0,setLoaded=false,editingListener=null;
function lsPath(){const f=$('lsEd').querySelector('form');$('lsPathRow').hidden=f.protocol.value!=='ws'}
function editListener(l){const d=$('lsEd'),f=d.querySelector('form');f.reset();editingListener=l;$('lsTitle').textContent=l?'Edit listener':'Add listener';
 if(l){f.protocol.value=l.protocol;f.address.value=l.address;f.path.value=l.path||''}lsPath();d.showModal()}
async function saveListener(e){e.preventDefault();const f=e.target,l={protocol:f.protocol.value,address:f.address.value.trim(),path:f.path.value.trim()};
 const ok=editingListener?await call('/api/listeners/update',{old:editingListener,new:l},'Listener saved'):await call('/api/listeners',l,'Listener saved');
 if(ok)$('lsEd').close()}
function delListener(l){if(confirm('Remove this listener?'))call('/api/listeners/delete',l,'Listener removed')}
async function saveSettings(e){e.preventDefault();const f=e.target;
 if(await call('/api/settings',{password:f.password.value,body_password:f.body.value,clear_body_password:f.clearBody.checked,socket_password:f.socket.value,clear_socket_password:f.clearSocket.checked,socket_address:f.socketAddr.value.trim()},'Settings saved')){
  f.password.value=f.body.value=f.socket.value='';f.clearBody.checked=f.clearSocket.checked=false}}
function copyFp(){const t=$('fpText').textContent;(navigator.clipboard?navigator.clipboard.writeText(t):Promise.reject()).then(()=>toast('Copied'),()=>{const r=document.createRange();r.selectNodeContents($('fpText'));getSelection().removeAllRanges();getSelection().addRange(r);toast('Select and copy it')})}
function addRow(){serviceRow($('edRows'),{})}
function edit(id){const p=state.peers.find(p=>p.id===id);if(!p)return;editing=id;$('edTitle').textContent='Services of node '+p.info.name;$('edRows').innerHTML='';(p.info.services||[]).forEach(s=>serviceRow($('edRows'),s));if(!(p.info.services||[]).length)addRow();$('editor').showModal()}
async function saveServices(){if(await call('/api/peers/services',{id:editing,services:readServices($('edRows'))},'Services saved on the node'))$('editor').close()}
every(()=>{if(!$('editor').open&&!$('lsEd').open)load()},3000);
