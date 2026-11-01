// The page's language, picked at the top right: System is Chinese when the
// browser's language is, else English. The pages are written in English;
// in Chinese every text and placeholder that is a UI string is translated
// as it reaches the page, and put back when English is picked again. The
// pick is kept in a cookie, which every tunnel page on the same host shares
// whatever its port.
(()=>{
const zh={
 // pages and sections
 'Servers':'服务器','Clients':'客户端','Tunnels':'隧道','Nodes':'节点','This web page':'本页面',
 'Forwards':'转发','Web proxies':'Web 代理','Client info':'客户端信息','Login':'登录',
 // table heads
 'Name':'名称','Address':'地址','Address (host:port)':'地址 (host:port)','Description':'描述','User':'用户名','Password':'密码',
 'Host':'主机','Since':'已连接','Info':'信息','Client':'客户端','Node':'节点','Up':'上行','Down':'下行','Age':'时长',
 'State':'状态','Server':'服务器','Web page':'Web 页面','Listen':'监听','Web user':'Web 用户名','Web password':'Web 密码',
 'Server page over WebSocket':'经 WebSocket 的服务器页面','Local client':'本地客户端','Sent':'发送','Received':'接收','Local':'本地','In':'入','Out':'出',
 // buttons
 'Query':'查询','Edit nodes':'编辑节点','Kick':'踢出','Close':'关闭','Add node':'添加节点','Cancel':'取消',
 'Connect':'连接','Forward':'转发','Forward…':'转发…','Update servers':'更新到服务器','Save':'保存','Open':'打开','Show':'显示','Hide':'隐藏',
 'Set login':'设置登录','Remove':'移除','Revert':'还原','Save and tell servers':'保存并通知服务器',
 // states and notes
 'None':'无','none':'无','no login':'无登录','no web proxy':'无 Web 代理','web proxy only':'仅 Web 代理','Management page':'管理页面','Via':'经由','bodies encrypted':'消息体已加密','listening':'监听中','any':'任意',
 'registered':'已注册','connecting':'连接中','failed':'失败','stopped':'已停止',
 'Empty user and password: the page has no login.':'用户名和密码都留空：该页面不需要登录。',
 // the theme and language pickers
 'Theme':'主题','Language':'语言','System':'跟随系统','Light':'浅色','Dark':'深色','Ghibli':'吉卜力','Mecha':'机甲'};
// Placeholders, some of which are too plain a word to translate as text.
const zhPh={'name (optional)':'名称（可选）','password':'密码',
 'body AES password (optional)':'消息体 AES 密码（可选）','local address, 127.0.0.1:2222':'本地地址，如 127.0.0.1:2222','node':'节点',
 'server (optional: any)':'服务器（可选，默认任意）','web page user:password (optional)':'Web 页面的 用户名:密码（可选）',
 'key=value, key2=value2 — told to servers':'key=value, key2=value2 —— 告知服务器','user':'用户名','name':'名称','optional':'可选',
 '•••••••• leave blank to keep':'•••••••• 留空则保持不变','not set: peers cannot register':'未设置：对端无法注册','default: the registration password':'默认：注册密码',
 'empty: none, such as :7002':'留空则不启用，如 :7002',"from the server's page":'取自服务器页面'};
Object.assign(zh,{
 // messages
 'Kicked':'已踢出','Tunnel closed':'隧道已关闭','Nodes saved':'节点已保存',
 'Server added':'服务器已添加','Removed':'已移除','Forward added':'转发已添加',
 // tunnel diagrams
 'Details':'详情','← All tunnels':'← 全部隧道','Tunnel map':'隧道全景','Tunnel path':'隧道链路','Tunnel':'隧道','Control link':'控制连接',
 'Traffic':'流量热度','low → high':'低 → 高','Tunnel ID':'隧道 ID','Local app':'本地应用','this page':'本页','not visible here':'此处不可见',
 'Only the client sees the local app.':'只有客户端能看到本地应用。','This tunnel is closed.':'该隧道已关闭。',
 'No open tunnels; dashed lines are control links.':'暂无隧道；虚线为控制连接。','Protocol':'协议','Body encryption':'消息体加密',
 'Version':'版本','Latency':'延迟','Share':'分担','Connect':'建连','OS':'系统','Uptime':'运行时长','arpc over WebSocket':'arpc（WebSocket）','arpc over TCP':'arpc（TCP）',
 'Web page over WebSocket':'经 WebSocket 的 Web 页面',
 'Forward removed':'转发已移除','Info sent to servers':'信息已发送到服务器','Login saved':'登录已保存','Kick this peer?':'踢出该 peer？'});
// Settings: servers' protocols, the server's listeners and settings, and
// the settings store.
Object.assign(zh,{
 'Settings':'设置','Listeners':'监听','Add server':'添加服务器','Add a server':'添加服务器','Edit':'编辑','Add listener':'添加监听','Edit listener':'编辑监听','Listener':'监听',
 'Save settings':'保存设置','Save and connect':'保存并连接','Copy':'复制','Copied':'已复制','Select and copy it':'请选中后复制',
 'Change store password':'修改存储密码','Current password':'当前密码','New password':'新密码','Confirm new password':'确认新密码',
 'Settings store':'设置存储','QUIC certificate':'QUIC 证书','Protocols':'协议','Peers dial':'对端连接地址','Path':'路径','On':'启用',
 'Pool size':'连接池大小','Body AES password':'消息体 AES 密码','Remove the body password':'移除消息体密码',
 'Protocols, in the order they are tried':'协议（按尝试顺序）','QUIC certificate pin (SHA-256)':'QUIC 证书指纹（SHA-256）',
 'Accept any QUIC certificate (testing only)':'接受任意 QUIC 证书（仅用于测试）',
 'Each connection dials the first protocol on, and falls back to the next when that fails.':'每个连接先拨号第一个启用的协议，失败时依次回退到下一个。',
 'Registration password':'注册密码','Web page WebSocket password':'Web 页面 WebSocket 密码','Remove it, using the registration password':'移除它，改用注册密码',
 'Web page over WebSocket, address':'经 WebSocket 的 Web 页面地址',
 'Changes apply at once, without a restart. Removing a QUIC listener closes the connections it carries; other listeners\' connections stay open until they close.':'修改立即生效，无需重启。移除 QUIC 监听会关闭其上的连接；其他监听上的连接保持到自行关闭。',
 'Applied at once, without a restart. Registered peers stay connected; a new body password applies to the connections opened from now on.':'立即生效，无需重启。已注册的对端保持连接；新的消息体密码只用于此后新建的连接。',
 'No settings store: changes are not saved.':'没有设置存储：修改不会被保存。',
 'The settings are saved encrypted in':'设置加密保存在',', unlocked with this password at every start.':'，每次启动时用此密码解锁。',
 'Store password changed':'存储密码已修改','Server saved':'服务器已保存','Listener saved':'监听已保存','Listener removed':'监听已移除','Settings saved':'设置已保存',
 'Remove this listener?':'移除该监听？','link':'链路'});
// The tunnel's nodes and services, and the console's logins.
Object.assign(zh,{
 'tunnel server':'隧道服务器','tunnel node':'隧道节点','tunnel client':'隧道客户端',
 'Services':'服务','Service':'服务','Nodes and services':'节点与服务','Services this node reaches':'本节点可访问的服务',
 'Edit services':'编辑服务','Add service':'添加服务','Save to node':'保存到节点','Query nodes':'查询节点',
 'Nodes queried':'已查询节点','Node queried':'已查询节点','Services saved':'服务已保存','Services saved on the node':'服务已保存到节点',
 'Log in':'登录','Log out':'退出登录','Console':'控制台','Connections':'连接数','Links':'链路','Peer addresses':'对端地址',
 'Console socket':'控制台 WebSocket','Console socket password':'控制台 WebSocket 密码','Console over an encrypted WebSocket, address':'经加密 WebSocket 的控制台地址',
 'Changes are written into fit\'s config and apply at once, without a restart. Removing a QUIC listener closes the links it carries; other listeners\' links stay open until they close.':'修改写入 fit 的配置并立即生效，无需重启。移除 QUIC 监听会关闭其上的链路；其他监听上的链路保持到自行关闭。',
 'Written into fit\'s config and applied at once. Registered peers stay linked; a new body password applies to the links opened from now on.':'写入 fit 的配置并立即生效。已注册的对端保持连接；新的消息体密码只用于此后新建的链路。',
 "Nodes and clients pin this fingerprint for the server's QUIC listeners.":'节点和客户端用此指纹固定本服务器 QUIC 监听的证书。',
 'Made, and written into the config, when a QUIC listener is added.':'添加 QUIC 监听时生成，并写入配置。',
 'Each link dials the first protocol on, and falls back to the next when that fails.':'每条链路先拨号第一个启用的协议，失败时依次回退到下一个。'});
Object.assign(zhPh,{'service':'服务','node':'节点'});
// Texts made of a UI string and names.
const zhRe=[[/^Services of node (.+) \(via (.+)\)$/,'节点 $1 的服务（经由 $2）'],[/^Services of node (.+)$/,'节点 $1 的服务'],[/^(\d+) services$/,'$1 个服务'],[/^(\d+)\/(\d+) tunnels$/,'$1/$2 条隧道'],
 [/^Login of the web page of server (.+)$/,'服务器 $1 的 Web 页面登录'],[/^Login of the web page of (.+)$/,'$1 的 Web 页面登录'],
 [/^Disconnect from (.+)\?$/,'断开与 $1 的连接？'],[/^via (.+)$/,'经由 $1'],[/^Server (.+)$/,'服务器 $1'],[/^link over (.+)$/,'链路（$1）'],[/^Tunnels \((\d+)\)$/,'隧道（$1）'],[/^(\d+) forwards$/,'$1 个转发'],[/^(\d+) nodes$/,'$1 个节点'],[/^(\d+)% loss$/,'丢包 $1%'],[/^(.+) · (\d+)% loss$/,'$1 · 丢包 $2%']];
// Parts of the header's status line.
const zhMeta=[[' · bodies encrypted',' · 消息体已加密'],[' · up ',' · 已运行 '],[' · settings not saved (no -config)',' · 设置未保存（未指定 -config）'],[' · settings not saved (no store)',' · 设置未保存（没有设置存储）'],[' · peers ',' · 对端地址 ']];
// A table cell's own text, the name in the header and a .raw element's text
// are data (a name, an address), except these.
const cellOK=new Set(['any','none']);

const langs=[['auto','System'],['en','English'],['zh','中文']];
const saved=()=>{const m=document.cookie.match(/(?:^|;\s*)fit-tunnel-lang=([a-z]+)/);return m&&langs.some(l=>l[0]===m[1])?m[1]:'auto'};
const system=()=>/^zh\b/i.test(navigator.language||'')?'zh':'en';
let pref=saved(),lang=pref==='auto'?system():pref;

// tr is s in Chinese, or null if it is no UI string; el holds it, as text,
// or as a placeholder if ph.
function tr(s,el,ph){const k=s.trim(),d=ph?zhPh:zh;if(!k)return null;
 if(Object.hasOwn(d,k)&&!(el&&(el.tagName==='TD'||el.id==='name'||el.classList.contains('raw'))&&!cellOK.has(k)))return s.replace(k,d[k]);
 for(const[re,r]of zhRe)if(re.test(k))return s.replace(k,k.replace(re,r));
 // A diagram's columns, "Clients → Server → Nodes".
 const parts=k.split(' → ');if(parts.length>1&&parts.every(p=>Object.hasOwn(zh,p)))return s.replace(k,parts.map(p=>zh[p]).join(' → '));
 if(el&&el.id==='meta'){let o=s;for(const[a,b]of zhMeta)o=o.split(a).join(b);if(o!==s)return o}
 return null}
// Each translated text node and placeholder keeps its English, to be put
// back, as long as it still holds the translation.
function text(n){const el=n.parentElement;if(!el||el.closest('script,style'))return;
 const v=n.nodeValue,en=n._en!==undefined&&v===n._zh?n._en:v,t=lang==='zh'?tr(en,el):null;
 if(t!==null){n._en=en;n._zh=t;if(v!==t)n.nodeValue=t}else if(en!==v)n.nodeValue=en}
function placeholder(el){const v=el.getAttribute('placeholder');if(v===null)return;
 const en=el._en!==undefined&&v===el._zh?el._en:v,t=lang==='zh'?tr(en,null,true):null;
 if(t!==null){el._en=en;el._zh=t;if(v!==t)el.setAttribute('placeholder',t)}else if(en!==v)el.setAttribute('placeholder',en)}
function walk(root){if(root.nodeType===3)return text(root);if(root.nodeType!==1)return;
 if(root.hasAttribute('placeholder'))placeholder(root);
 const w=document.createTreeWalker(root,NodeFilter.SHOW_TEXT|NodeFilter.SHOW_ELEMENT);
 for(let n=w.nextNode();n;n=w.nextNode())n.nodeType===3?text(n):n.hasAttribute('placeholder')&&placeholder(n)}
function apply(){document.documentElement.lang=lang==='zh'?'zh-CN':'en';walk(document.documentElement);
 const s=document.getElementById('lang');if(s)s.value=pref}

new MutationObserver(ms=>{if(lang!=='zh'&&!ms.some(m=>m.target._en!==undefined))return;
 for(const m of ms){if(m.type==='childList')m.addedNodes.forEach(walk);else if(m.type==='characterData')text(m.target);else placeholder(m.target)}})
 .observe(document.documentElement,{childList:true,subtree:true,characterData:true,attributes:true,attributeFilter:['placeholder']});
const confirm0=window.confirm;window.confirm=m=>confirm0.call(window,lang==='zh'&&tr(String(m))||m);
apply();

document.addEventListener('DOMContentLoaded',()=>{apply();const h=document.querySelector('header');if(!h)return;
 const l=document.createElement('label');l.className='pref lang';
 l.innerHTML='<span>Language</span><select id="lang">'+langs.map(([v,n])=>`<option value="${v}">${n}</option>`).join('')+'</select>';
 h.appendChild(l);const s=l.querySelector('select');s.value=pref;
 s.onchange=()=>{document.cookie='fit-tunnel-lang='+s.value+';path=/;max-age=31536000;samesite=lax';pref=s.value;lang=pref==='auto'?system():pref;apply()}});
// Follow a pick made on another page, or a change of the system's language.
setInterval(()=>{const p=saved(),l=p==='auto'?system():p;if(p!==pref||l!==lang){pref=p;lang=l;apply()}},1500);
})();
