// The node's page: its hubs, the services it reaches and its tunnels.
// What it changes is written into tunnel.node in fit's config.
let state={hubs:[],services:[],tunnels:[]},servicesDirty=false;
function render(){
 const st=state;$('name').textContent=st.name;
 $('meta').textContent=`${st.version} · ${st.open}/${st.max_tunnels} tunnels`;
 $('hubs').innerHTML=table(['Name','Protocols','State','Latency','Hub','Peer ID','Tunnels',''],st.hubs.map(s=>`<tr><td>${esc(s.name)}</td><td>${endpointList(s)}</td>
  <td>${stateCell(s)}${linkProtos(s)}${s.body_encrypted?'<div class="muted">bodies encrypted</div>':''}</td><td>${rtt(s.rtt)}</td><td>${esc(s.hub)}</td><td>${s.peer_id||''}<div class="muted">${s.pool}/${s.pool_size} links</div></td><td>${s.tunnels}</td>
  <td><button ${on('editHub',s.name)}>Edit</button><button class="danger" ${on('delHub',s.name)}>Remove</button></td></tr>`));
 $('tunnels').innerHTML=table(['Hub','ID','Proxy','Service','Address','Connect','Local','In','Out','Age',''],st.tunnels.map(t=>`<tr><td>${esc(t.hub)}</td><td>${t.id}</td><td>${esc(t.peer)}</td>
  <td>${esc(t.service)}</td><td>${esc(t.addr)}</td><td>${t.connect_rtt?`<span class="rtt">${ms(t.connect_rtt)}</span>`:''}</td><td>${esc(t.local)}</td><td>${bytes(t.in)}</td><td>${bytes(t.out)}</td><td>${ago(t.created)}</td>
  <td><button ${on('openFlow',t.id)}>Details</button><button class="danger" ${on('closeTunnel',t.hub,t.id)}>Close</button></td></tr>`));
 wbBadge('hubs',st.hubs.length);wbBadge('services',st.services.length);wbBadge('tunnels',st.tunnels.length);
 flowRefresh();
}
// The tunnel diagrams: proxies, hubs, this node and its services. A
// node's tunnel end sends the service's bytes (out) down and gets the
// proxy's (in).
const tunnelOf=t=>({id:t.id,name:`${t.peer} → ${t.hub} → ${t.service}`,up:t.in,down:t.out,created:t.created});
const serviceRTT=name=>state.service_rtt&&state.service_rtt[name];
const transport=a=>'link over '+(PROTO_NAMES[protoOf(a)]||protoOf(a));
function flowMe(g,col){const st=state;return g.node('me',col,{kind:'pod',label:st.name,sub:`${st.services.length} services`,self:true,state:'ok',
 facts:[['Hubs',st.hubs.length],['Services',st.services.length],['Version',st.version]]})}
function flowHub(g,col,name){const s=state.hubs.find(s=>s.name===name);
 return g.node('s:'+name,col,{kind:'hub',label:name,sub:s?s.addr:'',state:s?flowState(s.state):'wait',
  facts:s?[['Address',s.addr],['State',s.error||s.state],['Latency',rttText(s.rtt)],['Protocol',transport(s.addr)],['Body encryption',s.body_encrypted?'AES-256-GCM':'none'],['Peer ID',s.peer_id||''],['Links',`${s.pool}/${s.pool_size}`],['Tunnels',s.tunnels]]:[]})}
function flowService(g,col,name){const s=state.services.find(s=>s.name===name)||{name};
 return g.node('n:'+name,col,{kind:'node',icon:flowNodeIcon(name),label:name,sub:s.address||'',facts:[['Address',s.address],['Description',s.description],['Connect',rttText(serviceRTT(name))]]})}
function flowAll(){const st=state,g=flowGraph(['Proxies','Hubs','Node','Services']);flowMe(g,2);
 st.hubs.forEach(s=>{flowHub(g,1,s.name);g.link('s:'+s.name,'me',{rtt:s.rtt})});
 st.services.forEach(s=>{flowService(g,3,s.name);g.link('me','n:'+s.name,{rtt:serviceRTT(s.name)})});
 st.tunnels.forEach(t=>{g.node('c:'+t.peer,0,{kind:'proxy',label:t.peer,state:'ok'});flowHub(g,1,t.hub);flowService(g,3,t.service);
  g.tunnel(tunnelOf(t),['c:'+t.peer,'s:'+t.hub,'me','n:'+t.service])});
 return g}
function flowOne(id){const st=state,t=st.tunnels.find(t=>t.id===id);if(!t)return null;
 const g=flowGraph(['Local app','Proxy','Hub','Node','Service']),s=st.hubs.find(s=>s.name===t.hub)||{},enc=s.body_encrypted;
 g.node('app',0,{kind:'app',label:'Local app',sub:'not visible here',unknown:true,note:'Only the proxy sees the local app.'});
 g.node('c:'+t.peer,1,{kind:'proxy',label:t.peer,state:'ok'});flowHub(g,2,t.hub);
 flowMe(g,3).facts.push(['Local',t.local]);flowService(g,4,t.service);
 g.tunnel(tunnelOf(t),['app','c:'+t.peer,'s:'+t.hub,'me','n:'+t.service],[{label:'TCP'},{label:'link',lock:enc},{label:transport(s.addr||''),lock:enc,rtt:s.rtt},{label:'TCP',rtt:t.connect_rtt?{last:t.connect_rtt,avg:t.connect_rtt,min:t.connect_rtt,loss:0,samples:1}:null}]);
 return g}
function loadServices(){$('services').innerHTML='';state.services.forEach(s=>serviceRow($('services'),s,rtt(serviceRTT(s.name))));servicesDirty=false}
function addService(){serviceRow($('services'),{},'');servicesDirty=true}
async function load(first){try{state=await api('/api/status');render();if(first===true||!servicesDirty)loadServices()}catch(e){toast(e.message,true)}}
function onLogin(){load(true)}
for(const ev of['input','click'])document.addEventListener(ev,e=>{if(e.target.closest('#services'))servicesDirty=true});
async function saveServices(e){e.preventDefault();if(await call('/api/services',readServices($('services')),'Services saved')){servicesDirty=false;setTimeout(()=>load(true),300)}}
function closeTunnel(hub,id){call('/api/tunnels/close',{hub,id},'Tunnel closed')}
every(()=>{if(!$('srvEd')||!$('srvEd').open)load(false)},3000);
