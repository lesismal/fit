// config.js is what the node's and client's pages share for their
// settings: the editor of a server they link to, with its endpoints by
// protocol in the order they are tried. Load it after base.js. Passwords
// never come back from the pages' API: a password field left empty keeps
// the one set. What the editor saves is written into the role's member of
// fit's config.

// protoOf is the protocol of an address, host:port being TCP.
const protoOf=a=>{const m=String(a||'').match(/^([a-z0-9]+):\/\//i);return m?m[1].toLowerCase():'tcp'};
const PROTO_NAMES={tcp:'TCP',ws:'WebSocket',quic:'QUIC'};
// linkProtos is what protocols an uplink's links took, as HTML.
function linkProtos(s){const e=Object.entries(s.protos||{});if(!e.length)return '';
 return `<div class="muted">${e.map(([p,n])=>`<span class="proto proto-${esc(p)}">${esc(PROTO_NAMES[p]||p)}</span> ×${n}`).join(' · ')}</div>`}

// The server editor: a dialog, made once, for a new server or for the one
// named editingServer.
let editingServer=null;
const EP_HINT={tcp:'host:7000',ws:'ws://host:7001/fibhub',quic:'quic://host:7443'};
function serverEditor(){let d=$('srvEd');if(d)return d;d=document.createElement('dialog');d.id='srvEd';
 d.innerHTML=`<form method="dialog" data-submit="saveServer"><h3 id="srvTitle">Server</h3>
 <div class="grid2"><label><span>Name</span><input name="name" placeholder="name (optional)"></label>
 <label><span>Pool size</span><input name="pool" type="number" min="1" max="100" placeholder="10"></label>
 <label><span>Password</span><input name="password" type="password" autocomplete="new-password"></label>
 <label><span>Body AES password</span><input name="body" type="password" autocomplete="new-password" placeholder="optional"></label></div>
 <label class="check" id="srvClearBodyRow"><input type="checkbox" name="clearBody"> <span>Remove the body password</span></label>
 <h4>Protocols, in the order they are tried</h4>
 <table><thead><tr><th>On</th><th>Protocol</th><th>Address</th><th></th></tr></thead><tbody id="srvEps"></tbody></table>
 <p class="muted">Each link dials the first protocol on, and falls back to the next when that fails.</p>
 <div class="grid2"><label><span>QUIC certificate pin (SHA-256)</span><input name="pin" placeholder="from the server's page" autocomplete="off"></label>
 <label class="check"><input type="checkbox" name="insecure"> <span>Accept any QUIC certificate (testing only)</span></label></div>
 <p class="end"><button type="button" ${on('closeDialog','srvEd')}>Cancel</button> <button class="primary">Save and connect</button></p></form>`;
 document.body.appendChild(d);return d}
// closeDialog closes the dialog of id.
function closeDialog(id){$(id).close()}
// epRow adds the row of endpoint e to the editor.
function epRow(e){const tr=document.createElement('tr');tr.dataset.proto=e.protocol;
 tr.innerHTML=`<td><input type="checkbox" ${e.enabled?'checked':''}></td><td>${esc(PROTO_NAMES[e.protocol]||e.protocol)}</td>
  <td><input placeholder="${esc(EP_HINT[e.protocol]||'')}" value="${esc(e.address||'')}"></td>
  <td><button type="button" title="Up" ${on('epMove',-1)}>↑</button><button type="button" title="Down" ${on('epMove',1)}>↓</button></td>`;
 $('srvEps').appendChild(tr)}
function epMove(dir){const tr=this.closest('tr'),o=dir<0?tr.previousElementSibling:tr.nextElementSibling;if(o)dir<0?o.before(tr):o.after(tr)}
// editServer opens the editor on the uplink status of the server named
// name, or on a new server.
function editServer(name){const s=name===null?null:(state.servers||[]).find(s=>s.name===name),d=serverEditor(),f=d.querySelector('form');f.reset();editingServer=s?s.name:null;
 $('srvTitle').textContent=s?'Server '+s.name:'Add a server';f.name.value=s?s.name:'';f.pool.value=s&&s.pool_size?s.pool_size:'';
 f.password.placeholder=s&&s.has_password?'•••••••• leave blank to keep':'password';f.password.required=!s;
 f.body.placeholder=s&&s.has_body_password?'•••••••• leave blank to keep':'optional';$('srvClearBodyRow').hidden=!(s&&s.has_body_password);
 f.pin.value=s&&s.quic_pin||'';f.insecure.checked=!!(s&&s.quic_insecure);
 const eps=(s&&s.endpoints||[]).slice();for(const p of['quic','ws','tcp'])if(!eps.some(e=>e.protocol===p))eps.push({protocol:p,address:'',enabled:false});
 $('srvEps').innerHTML='';eps.forEach(epRow);d.showModal()}
async function saveServer(e){e.preventDefault();const f=e.target;
 const endpoints=[...$('srvEps').querySelectorAll('tr')].map(tr=>{const i=tr.querySelectorAll('input');return{protocol:tr.dataset.proto,address:i[1].value.trim(),enabled:i[0].checked}}).filter(x=>x.address);
 const server={name:f.name.value.trim(),endpoints,password:f.password.value,body_password:f.body.value,pool_size:+f.pool.value||0,quic_pin:f.pin.value.trim(),quic_insecure:f.insecure.checked};
 const ok=editingServer===null?await call('/api/servers',server,'Server saved'):await call('/api/servers/update',{name:editingServer,server,clear_body_password:f.clearBody.checked},'Server saved');
 if(ok)$('srvEd').close()}
// endpointList is a server's endpoints as HTML, those off struck out.
function endpointList(s){return (s.endpoints||[]).map((e,i)=>`<div class="node ep${e.enabled?'':' off'}">${i+1}. <span class="proto proto-${esc(e.protocol)}">${esc(PROTO_NAMES[e.protocol]||e.protocol)}</span> ${esc(String(e.address).replace(/^[a-z0-9]+:\/\//i,''))}</div>`).join('')||`<div class="node">${esc(s.addr)}</div>`}
function delServer(name){if(confirm('Disconnect from '+name+'?'))call('/api/servers/delete',{name},'Removed')}
