// The page's theme, picked at the top right: System, the default, is light
// or dark as the system is; a pick of another theme holds until changed. It
// is kept in a cookie, which every tunnel page on the same host shares
// whatever its port, so a pick made on one page applies to all.
(()=>{
const themes=[['auto','System'],['light','Light'],['dark','Dark'],['ghibli','Ghibli'],['mecha','Mecha']];
const saved=()=>{const m=document.cookie.match(/(?:^|;\s*)fit-tunnel-theme=([a-z]+)/);return m&&themes.some(t=>t[0]===m[1])?m[1]:'auto'};
let cur=saved();
function apply(t){cur=t;document.documentElement.dataset.theme=t;const s=document.getElementById('theme');if(s)s.value=t}
apply(cur);
document.addEventListener('DOMContentLoaded',()=>{const h=document.querySelector('header');if(!h)return;
 const l=document.createElement('label');l.className='pref theme';
 l.innerHTML='<span>Theme</span><select id="theme">'+themes.map(([v,n])=>`<option value="${v}">${n}</option>`).join('')+'</select>';
 h.appendChild(l);const s=l.querySelector('select');s.value=cur;
 s.onchange=()=>{document.cookie='fit-tunnel-theme='+s.value+';path=/;max-age=31536000;samesite=lax';apply(s.value)}});
// Follow a pick made on another page.
setInterval(()=>{const t=saved();if(t!==cur)apply(t)},1500);
})();
