// workbench.js lays a page out as a workbench: a menu on the left, with an
// item for each <section> of <main>, and on the right a workspace showing
// the section picked, under the page's header. A section's data-view names
// it, and the URL's #/name picks it; data-icon picks its icon, and its h2
// gives the item's label and the workspace's title, the h2's buttons going
// to the header. The header's h1 and #meta move to the menu, and the page's
// <footer> to the bottom of the workspace. A page calls
// wbBadge(view, n) to show a count on an item, wbShow(view) to switch.
// Load it before i18n.js, so it reads the h2s in English and i18n.js then
// translates what it built.
(()=>{
const ICONS={
 server:'<circle cx="12" cy="5.5" r="2.5"/><circle cx="5" cy="18" r="2.5"/><circle cx="19" cy="18" r="2.5"/><path d="M10.8 7.7 6.2 15.8M13.2 7.7l4.6 8.1M7.5 18h9"/>',
 pod:'<path d="M12 2.8 20 7.3v9.4L12 21.2 4 16.7V7.3z"/><path d="M4 7.3 12 12l8-4.7M12 12v9.2"/>',
 client:'<rect x="4" y="5" width="16" height="11" rx="1.5"/><path d="M2 19.5h20M10 8.5l-2 2 2 2M14 8.5l2 2-2 2"/>',
 node:'<rect x="6" y="6" width="12" height="12" rx="2"/><rect x="9.5" y="9.5" width="5" height="5" rx="1"/><path d="M9 2.5V6M15 2.5V6M9 18v3.5M15 18v3.5M2.5 9H6M2.5 15H6M18 9h3.5M18 15h3.5"/>',
 forward:'<path d="M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4"/>',
 web:'<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.6 2.6 3.9 5.6 3.9 9s-1.3 6.4-3.9 9c-2.6-2.6-3.9-5.6-3.9-9S9.4 5.6 12 3z"/>',
 tunnel:'<path d="M3 8h14M13 4l4 4-4 4M21 16H7M11 12l-4 4 4 4"/>',
 info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5v.5"/>',
 settings:'<circle cx="12" cy="12" r="3"/><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1"/>',
 listen:'<path d="M5 12a7 7 0 0 1 14 0M8.5 12a3.5 3.5 0 0 1 7 0"/><circle cx="12" cy="12" r="1"/><path d="M12 13v8"/>',
 page:'<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18M7 6.5h.5M10 6.5h.5"/>',
 logo:'<circle cx="12" cy="12" r="3"/><circle cx="12" cy="3.5" r="1.8"/><circle cx="19.4" cy="16.3" r="1.8"/><circle cx="4.6" cy="16.3" r="1.8"/><path d="M12 5.3V9M17.8 15.3l-3.2-1.8M6.2 15.3l3.2-1.8"/>',
 fold:'<path d="M15 6l-6 6 6 6"/>'};
const svg=name=>`<svg viewBox="0 0 24 24" aria-hidden="true">${ICONS[name]||ICONS.page}</svg>`;
const store={get(k){try{return localStorage.getItem(k)}catch(e){return null}},set(k,v){try{localStorage.setItem(k,v)}catch(e){}}};
let views=[],wb,title,actions;

function show(id,user){const v=views.find(v=>v.id===id)||views[0];if(!v)return;
 views.forEach(x=>{const on=x===v;x.sec.hidden=!on;x.item.classList.toggle('on',on);on?x.item.setAttribute('aria-current','page'):x.item.removeAttribute('aria-current')});
 title.textContent=v.label;actions.replaceChildren(...v.acts);
 if(user){history.replaceState(null,'','#/'+v.id);window.scrollTo(0,0)}}
window.wbShow=id=>show(id,true);
window.wbBadge=(id,n)=>{const v=views.find(v=>v.id===id);if(v)v.badge.textContent=n?String(n):''};

document.addEventListener('DOMContentLoaded',()=>{const header=document.querySelector('body>header'),main=document.querySelector('body>main'),footer=document.querySelector('body>footer');if(!header||!main)return;
 wb=document.createElement('div');wb.className='wb'+(store.get('fit-tunnel-wb-mini')==='1'?' mini':'');
 const side=document.createElement('aside');side.className='wb-side';
 const brand=document.createElement('div');brand.className='wb-brand';brand.innerHTML=`<span class="wb-logo">${svg('logo')}</span>`;
 const h1=header.querySelector('h1');if(h1)brand.appendChild(h1);
 const nav=document.createElement('nav');nav.className='wb-nav';
 main.querySelectorAll(':scope>section').forEach((sec,i)=>{const h2=sec.querySelector(':scope>h2'),t=h2&&[...h2.childNodes].find(n=>n.nodeType===3);
  const id=sec.dataset.view||'view'+i,label=t?t.nodeValue.trim():id;
  const item=document.createElement('a');item.className='wb-item';item.href='#/'+id;item.title=label;
  item.innerHTML=`${svg(sec.dataset.icon)}<span class="wb-label"></span><span class="wb-badge"></span>`;item.querySelector('.wb-label').textContent=label;
  item.onclick=e=>{e.preventDefault();show(id,true)};nav.appendChild(item);
  views.push({id,label,sec,item,badge:item.querySelector('.wb-badge'),acts:h2?[...h2.querySelectorAll('button')]:[]})});
 const foot=document.createElement('div');foot.className='wb-foot';
 const meta=header.querySelector('#meta');if(meta)foot.appendChild(meta);
 const fold=document.createElement('button');fold.className='wb-fold';fold.setAttribute('aria-label','Collapse the menu');fold.innerHTML=svg('fold');
 fold.onclick=()=>{wb.classList.toggle('mini');store.set('fit-tunnel-wb-mini',wb.classList.contains('mini')?'1':'0')};foot.appendChild(fold);
 side.append(brand,nav,foot);
 title=document.createElement('div');title.className='wb-title';actions=document.createElement('div');actions.className='wb-actions';
 header.prepend(title,actions);
 const work=document.createElement('div');work.className='wb-work';
 header.before(wb);work.append(header,main);if(footer){footer.classList.add('wb-footer');work.append(footer)}wb.append(side,work);
 window.addEventListener('hashchange',()=>show(location.hash.slice(2)));
 show(location.hash.slice(2))});
})();
